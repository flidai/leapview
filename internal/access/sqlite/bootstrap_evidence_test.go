package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func TestBootstrapAPITokenEvidenceRequiresDurableAdminAndExplicitPublish(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	now := time.Now().UTC().Truncate(time.Millisecond)
	admin, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: "bootstrap-evidence-admin", Email: "bootstrap-evidence@example.com", Role: access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	validSecret, valid, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: admin.ID, Name: "valid", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create valid token: %v", err)
	}
	_ = validSecret
	if got, err := repo.BootstrapAPITokenEvidence(ctx, admin.ID, valid.ID, now); err != nil || got.ID != valid.ID {
		t.Fatalf("valid evidence = %#v, %v", got, err)
	}

	dynamicSecret, dynamic, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: admin.ID, Name: "dynamic", ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create dynamic token: %v", err)
	}
	_ = dynamicSecret
	if _, err := repo.BootstrapAPITokenEvidence(ctx, admin.ID, dynamic.ID, now); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("dynamic evidence err = %v, want access.ErrForbidden", err)
	}

	_, empty, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: admin.ID, Name: "empty", Capabilities: []access.Capability{}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create empty token: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, admin.ID, empty.ID, now), access.ErrForbidden) {
		t.Fatal("empty capabilities unexpectedly authorized")
	}

	_, readOnly, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: admin.ID, Name: "read-only", Capabilities: []access.Capability{access.CapabilityResourceRead}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create read-only token: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, admin.ID, readOnly.ID, now), access.ErrForbidden) {
		t.Fatal("read-only capabilities unexpectedly authorized")
	}

	foreign, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "bootstrap-evidence-foreign", Email: "foreign@example.com"})
	if err != nil {
		t.Fatalf("create foreign principal: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, foreign.ID, valid.ID, now), sql.ErrNoRows) {
		t.Fatal("foreign actor unexpectedly resolved token evidence")
	}
	_, nonAdmin, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: foreign.ID, Name: "non-admin", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create non-admin token: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, foreign.ID, nonAdmin.ID, now), sql.ErrNoRows) {
		t.Fatal("non-admin token unexpectedly resolved token evidence")
	}

	if err := repo.RevokeAPIToken(ctx, valid.ID); err != nil {
		t.Fatalf("revoke valid token: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, admin.ID, valid.ID, now), sql.ErrNoRows) {
		t.Fatal("revoked token unexpectedly resolved token evidence")
	}

	_, expired, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: admin.ID, Name: "expired", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create expiring token: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, now.Add(-time.Minute).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, admin.ID, expired.ID, now), sql.ErrNoRows) {
		t.Fatal("expired token unexpectedly resolved token evidence")
	}

	blocked, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: "bootstrap-evidence-blocked", Email: "blocked@example.com", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatalf("set blocked platform role: %v", err)
	}
	_, blockedToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: blocked.ID, Name: "blocked", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create blocked token: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE principals SET blocked_at = CURRENT_TIMESTAMP WHERE id = ?`, blocked.ID); err != nil {
		t.Fatalf("block principal: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, blocked.ID, blockedToken.ID, now), sql.ErrNoRows) {
		t.Fatal("blocked principal unexpectedly resolved token evidence")
	}

	disabled, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: "bootstrap-evidence-disabled", Email: "disabled@example.com", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatalf("set disabled platform role: %v", err)
	}
	_, disabledToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: disabled.ID, Name: "disabled", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("create disabled token: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE principals SET disabled_at = CURRENT_TIMESTAMP WHERE id = ?`, disabled.ID); err != nil {
		t.Fatalf("disable principal: %v", err)
	}
	if !errors.Is(mustBootstrapEvidence(repo, ctx, disabled.ID, disabledToken.ID, now), sql.ErrNoRows) {
		t.Fatal("disabled principal unexpectedly resolved token evidence")
	}
}

func mustBootstrapEvidence(repo *Repository, ctx context.Context, principalID, tokenID string, now time.Time) error {
	_, err := repo.BootstrapAPITokenEvidence(ctx, principalID, tokenID, now)
	return err
}
