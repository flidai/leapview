package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

func TestAPITokenRotationRevokesPreviousMaterialImmediately(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "pat-rotation@example.com", DisplayName: "PAT Rotation", Password: "pat rotation password"})
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, oldToken, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: user.Principal.ID, Name: "old", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := repo.RotateAPIToken(t.Context(), access.APITokenRotationInput{
		PrincipalID: user.Principal.ID, PreviousTokenID: oldToken.ID,
		Token:          access.APITokenInput{Name: "new", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(2 * time.Hour)},
		RevokePrevious: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotation.Secret == "" || rotation.Created.ID == oldToken.ID || rotation.Created.Capabilities == nil || rotation.Created.Name != "new" {
		t.Fatalf("rotation = %#v", rotation)
	}
	if rotation.Created.Description != "" || rotation.Previous.RevokedAt == "" {
		t.Fatalf("rotation metadata = %#v", rotation)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), oldSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old token after revokePrevious = %v, want pgx.ErrNoRows", err)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), rotation.Secret); err != nil {
		t.Fatalf("replacement token authentication = %v", err)
	}
	stored, err := repo.apiToken(t.Context(), rotation.Created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != rotation.Created.ID || stored.RevokedAt != "" || stored.Capabilities == nil {
		t.Fatalf("stored replacement = %#v", stored)
	}
	if storedToken, err := repo.apiToken(t.Context(), oldToken.ID); err != nil || storedToken.RevokedAt == "" {
		t.Fatalf("stored previous = %#v, err=%v", storedToken, err)
	}
}

func TestAPITokenRotationOverlapRetainsPreviousMaterial(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "pat-overlap@example.com", DisplayName: "PAT Overlap", Password: "pat overlap password"})
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, oldToken, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: user.Principal.ID, Name: "old", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := repo.RotateAPIToken(t.Context(), access.APITokenRotationInput{
		PrincipalID: user.Principal.ID, PreviousTokenID: oldToken.ID,
		Token: access.APITokenInput{Name: "overlap", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rotation.Previous.RevokedAt != "" {
		t.Fatalf("overlap revoked previous token = %#v", rotation.Previous)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), oldSecret); err != nil {
		t.Fatalf("overlap previous token = %v", err)
	}
}

type apiTokenTouchFailureDB struct{ DBTX }

func (db apiTokenTouchFailureDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "UPDATE access.api_token SET last_used_at") {
		return pgconn.CommandTag{}, errors.New("injected api token touch failure")
	}
	return db.DBTX.Exec(ctx, sql, args...)
}

func TestAPITokenTouchFailureDoesNotRejectAuthenticationAndIncrementsMetric(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "pat-touch@example.com", DisplayName: "PAT Touch", Password: "pat touch password"})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: user.Principal.ID, Name: "touch", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	failingRepo, err := NewAccess(apiTokenTouchFailureDB{DBTX: db.runtime}, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	before := apiTokenTouchFailureMetricValue(t)
	if _, err := failingRepo.PrincipalForAPIToken(t.Context(), secret); err != nil {
		t.Fatalf("authentication failed after touch error: %v", err)
	}
	after := apiTokenTouchFailureMetricValue(t)
	if after != before+1 {
		t.Fatalf("api-token touch failure metric = %v, want %v", after, before+1)
	}
}

func TestAPITokenLastUsedTouchCoalescesWithinInterval(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "pat-touch-coalesce@example.com", DisplayName: "PAT Touch Coalesce", Password: "pat touch coalesce password"})
	if err != nil {
		t.Fatal(err)
	}
	secret, token, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: user.Principal.ID, Name: "touch-coalesce", Capabilities: []access.Capability{access.CapabilityResourceUse}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	first, err := repo.apiToken(t.Context(), token.ID)
	if err != nil || first.LastUsedAt == "" {
		t.Fatalf("first last-used evidence = %#v, err=%v", first, err)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	second, err := repo.apiToken(t.Context(), token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.LastUsedAt != first.LastUsedAt {
		t.Fatalf("coalesced last-used evidence changed: first=%q second=%q", first.LastUsedAt, second.LastUsedAt)
	}
}

func apiTokenTouchFailureMetricValue(t *testing.T) float64 {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(credentialTouchFailures)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "leapview_access_credential_last_used_update_failures_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "credential_class" && label.GetValue() == "api_token" {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

var _ DBTX = apiTokenTouchFailureDB{}
