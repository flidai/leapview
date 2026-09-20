package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const qualificationFingerprintKey = "0123456789abcdef0123456789abcdef"

func newServiceCredentialQualification(t *testing.T) (auditDatabase, *Repository) {
	t.Helper()
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte(qualificationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	return db, repo
}

func TestServicePrincipalCredentialsPostgreSQL18DisableEnableAndRestart(t *testing.T) {
	db, repo := newServiceCredentialQualification(t)
	ctx := t.Context()
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "disable-enable-service"})
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, oldMetadata, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "before-disable", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, oldSecret); err != nil {
		t.Fatalf("initial service secret authentication: %v", err)
	}

	disabled, err := repo.DisableServicePrincipal(ctx, service.ID)
	if err != nil {
		t.Fatalf("disable service principal: %v", err)
	}
	if !disabled.AccessDisabled() || disabled.DisabledAt == "" {
		t.Fatalf("disabled service principal = %#v", disabled)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, oldSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled service secret authentication = %v, want no rows", err)
	}

	enabled, err := repo.EnableServicePrincipal(ctx, service.ID)
	if err != nil {
		t.Fatalf("enable service principal: %v", err)
	}
	if enabled.AccessDisabled() || enabled.DisabledAt != "" || enabled.BlockedAt != "" {
		t.Fatalf("enabled service principal = %#v", enabled)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, oldSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("re-enabled service secret authentication = %v, want no rows", err)
	}
	revokedMetadata, err := repo.GetServicePrincipalSecret(ctx, service.ID, oldMetadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revokedMetadata.RevokedAt == "" {
		t.Fatalf("disabled credential was not permanently revoked: %#v", revokedMetadata)
	}

	newSecret, newMetadata, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "after-enable", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, newSecret); err != nil {
		t.Fatalf("new post-enable service secret authentication: %v", err)
	}

	// Reconstruct the authority through a fresh pool to qualify durable state
	// across an application restart. The old pool remains usable until the new
	// pool is ready, matching a graceful handoff during process replacement.
	restartedPool, err := pgxpool.New(ctx, db.runtime.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restartedPool.Close)
	restarted, err := NewAccess(restartedPool, FingerprintConfig{Key: []byte(qualificationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.GetServicePrincipalSecret(ctx, service.ID, newMetadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ID != newMetadata.ID || persisted.RevokedAt != "" || persisted.Secret != "" {
		t.Fatalf("restarted service credential metadata invalid: id=%q revoked=%q secret_present=%t", persisted.ID, persisted.RevokedAt, persisted.Secret != "")
	}
	if _, err := restarted.PrincipalForServicePrincipalSecret(ctx, service.ID, newSecret); err != nil {
		t.Fatalf("restarted service secret authentication: %v", err)
	}
}

func TestServicePrincipalCredentialsPostgreSQL18OverlappingRotation(t *testing.T) {
	_, repo := newServiceCredentialQualification(t)
	ctx := t.Context()
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "rotation-service"})
	if err != nil {
		t.Fatal(err)
	}
	firstSecret, firstMetadata, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "rotation-first", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	overlap, err := repo.RotateServicePrincipalSecret(ctx, access.ServicePrincipalSecretRotationInput{
		ServicePrincipalID: service.ID,
		PreviousSecretID:   firstMetadata.ID,
		Secret:             access.ServicePrincipalSecretInput{Name: "rotation-overlap", ExpiresAt: time.Now().UTC().Add(2 * time.Hour)},
		RevokePrevious:     false,
	})
	if err != nil {
		t.Fatalf("overlapping service secret rotation: %v", err)
	}
	if overlap.Secret == "" || overlap.Created.ID == firstMetadata.ID || overlap.Created.Secret != "" || overlap.Previous.RevokedAt != "" {
		t.Fatalf("overlap rotation metadata invalid: created_id=%q previous_revoked=%q secret_present=%t", overlap.Created.ID, overlap.Previous.RevokedAt, overlap.Secret != "")
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, firstSecret); err != nil {
		t.Fatalf("previous secret after overlap rotation: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, overlap.Secret); err != nil {
		t.Fatalf("new secret after overlap rotation: %v", err)
	}

	replacement, err := repo.RotateServicePrincipalSecret(ctx, access.ServicePrincipalSecretRotationInput{
		ServicePrincipalID: service.ID,
		PreviousSecretID:   overlap.Created.ID,
		Secret:             access.ServicePrincipalSecretInput{Name: "rotation-replacement", ExpiresAt: time.Now().UTC().Add(2 * time.Hour)},
		RevokePrevious:     true,
	})
	if err != nil {
		t.Fatalf("revoking service secret rotation: %v", err)
	}
	if replacement.Secret == "" || replacement.Previous.RevokedAt == "" {
		t.Fatalf("revoking rotation metadata invalid: previous_revoked=%q secret_present=%t", replacement.Previous.RevokedAt, replacement.Secret != "")
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, overlap.Secret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("previous secret after revoking rotation = %v, want no rows", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, replacement.Secret); err != nil {
		t.Fatalf("replacement secret after revoking rotation: %v", err)
	}

	metadata, err := repo.ListServicePrincipalSecrets(ctx, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 3 {
		t.Fatalf("service secret metadata rows = %d, want 3", len(metadata))
	}
	for _, secret := range metadata {
		if secret.Secret != "" {
			t.Fatalf("service secret list returned one-time material: %#v", secret)
		}
	}
}

func TestServicePrincipalCredentialsPostgreSQL18LastUsedPersistenceAndCoalescing(t *testing.T) {
	db, repo := newServiceCredentialQualification(t)
	ctx := t.Context()
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "last-used-service"})
	if err != nil {
		t.Fatal(err)
	}
	secret, metadata, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "last-used", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.LastUsedAt != "" {
		t.Fatalf("new service secret last_used_at = %q, want empty", metadata.LastUsedAt)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, secret); err != nil {
		t.Fatal(err)
	}
	first, err := repo.GetServicePrincipalSecret(ctx, service.ID, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.LastUsedAt == "" {
		t.Fatalf("successful authentication did not persist last_used_at: %#v", first)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, secret); err != nil {
		t.Fatal(err)
	}
	second, err := repo.GetServicePrincipalSecret(ctx, service.ID, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.LastUsedAt != first.LastUsedAt {
		t.Fatalf("coalesced authentication changed last_used_at: first=%q second=%q", first.LastUsedAt, second.LastUsedAt)
	}

	// A stale evidence row is eligible for a fresh touch. This also proves the
	// one-minute coalescing predicate is database-backed rather than process
	// memory state.
	if _, err := db.admin.Exec(ctx, `UPDATE access.service_principal_secret SET last_used_at = clock_timestamp() - interval '2 minutes' WHERE id=$1::uuid`, metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, secret); err != nil {
		t.Fatal(err)
	}
	third, err := repo.GetServicePrincipalSecret(ctx, service.ID, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstUsed, err := time.Parse(time.RFC3339Nano, first.LastUsedAt)
	if err != nil {
		t.Fatal(err)
	}
	thirdUsed, err := time.Parse(time.RFC3339Nano, third.LastUsedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !thirdUsed.After(firstUsed) {
		t.Fatalf("stale last_used_at was not refreshed: first=%s third=%s", first.LastUsedAt, third.LastUsedAt)
	}

	// Reopen the pool and confirm evidence remains available after a process
	// restart, including the coalescing state.
	restartedPool, err := pgxpool.New(ctx, db.runtime.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restartedPool.Close)
	restarted, err := NewAccess(restartedPool, FingerprintConfig{Key: []byte(qualificationFingerprintKey)})
	if err != nil {
		t.Fatal(err)
	}
	restartedMetadata, err := restarted.GetServicePrincipalSecret(ctx, service.ID, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restartedMetadata.LastUsedAt != third.LastUsedAt {
		t.Fatalf("restarted last_used_at = %q, want %q", restartedMetadata.LastUsedAt, third.LastUsedAt)
	}
}

func TestServicePrincipalCredentialsPostgreSQL18RevokeAllCredentialClasses(t *testing.T) {
	db, repo := newServiceCredentialQualification(t)
	ctx := t.Context()
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "revoke-all-service"})
	if err != nil {
		t.Fatal(err)
	}
	browserToken, err := repo.CreateSession(ctx, service.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiSecret, apiToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: service.ID, Name: "revoke-all-api", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	serviceSecret, serviceSecretMetadata, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "revoke-all-secret", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	projectID, err := graph.NewResourceID("revoke-all-project")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	authoringAccessHash := strings.Repeat("a", 64)
	authoringRefreshHash := strings.Repeat("b", 64)
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertAuthoringSessionAndCredential(ctx, tx, "revoke-all-authoring-session", access.AuthoringSessionWorkload, "revoke-all-client", service.ID,
		access.AuthoringScope{TargetID: "revoke-all-target", ProjectID: projectID, Capabilities: []access.Capability{access.CapabilityResourceRead}}, now, now.Add(time.Hour),
		"revoke-all-authoring-credential", authoringAccessHash, authoringRefreshHash, now.Add(30*time.Minute), now.Add(2*time.Hour)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	oauthSignature := "revoke-all-oauth-signature"
	if err := CreateOAuthSession(ctx, db.runtime, "access_token", oauthSignature, "revoke-all-oauth-request",
		[]byte(`{"session":{"subject":"`+service.ID+`"}}`), "revoke-all-access-signature"); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.PrincipalForToken(ctx, browserToken); err != nil {
		t.Fatalf("browser credential before revoke-all: %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, apiSecret); err != nil {
		t.Fatalf("API credential before revoke-all: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, serviceSecret); err != nil {
		t.Fatalf("service secret before revoke-all: %v", err)
	}
	if _, err := repo.AuthoringCredentialByAccessTokenHash(ctx, authoringAccessHash, now); err != nil {
		t.Fatalf("authoring credential before revoke-all: %v", err)
	}
	oauthBefore, err := GetOAuthSession(ctx, db.runtime, "access_token", oauthSignature)
	if err != nil {
		t.Fatal(err)
	}
	if !oauthBefore.Active {
		t.Fatal("OAuth credential was not active before revoke-all")
	}

	if err := repo.RevokeAllServicePrincipalCredentials(ctx, service.ID); err != nil {
		t.Fatalf("revoke all service-principal credentials: %v", err)
	}
	for name, authenticate := range map[string]func() error{
		"browser": func() error { _, err := repo.PrincipalForToken(ctx, browserToken); return err },
		"api":     func() error { _, err := repo.PrincipalForAPIToken(ctx, apiSecret); return err },
		"secret": func() error {
			_, err := repo.PrincipalForServicePrincipalSecret(ctx, service.ID, serviceSecret)
			return err
		},
		"authoring": func() error {
			_, err := repo.AuthoringCredentialByAccessTokenHash(ctx, authoringAccessHash, now)
			return err
		},
	} {
		if err := authenticate(); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s credential after revoke-all = %v, want no rows", name, err)
		}
	}
	oauthAfter, err := GetOAuthSession(ctx, db.runtime, "access_token", oauthSignature)
	if err != nil {
		t.Fatal(err)
	}
	if oauthAfter.Active {
		t.Fatal("OAuth credential remained active after revoke-all")
	}
	revoked, err := repo.GetServicePrincipalSecret(ctx, service.ID, serviceSecretMetadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == "" {
		t.Fatalf("service secret revoke-all metadata = %#v", revoked)
	}
	if apiToken.ID == "" {
		t.Fatal("API token metadata did not contain durable ID")
	}
	principal, err := repo.PrincipalByID(ctx, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if principal.AccessDisabled() {
		t.Fatalf("revoke-all disabled service principal: %#v", principal)
	}
}
