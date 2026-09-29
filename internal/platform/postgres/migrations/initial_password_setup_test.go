package migrations

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestInitialPasswordSetupUpgradePreservesLegacyCredentialsWithoutExemptingThem(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 42); err != nil {
		t.Fatal(err)
	}
	repo, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "legacy@example.test", Password: "legacy-password-123", MustChange: true})
	if err != nil {
		t.Fatal(err)
	}
	secret, token, err := repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: principal.Principal.ID, Name: access.InitialProjectClaimPublisherTokenName("legacy-claim"), Permissions: []access.PermissionPair{}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := pool.QueryRow(t.Context(), `SELECT row_to_json(t)::text FROM access.api_token t WHERE id=$1::uuid`, token.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT row_to_json(t)::text FROM access.api_token t WHERE id=$1::uuid`, token.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration rewrote an existing token")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM access.initial_password_setup)+(SELECT count(*) FROM access.initial_publisher_origin)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invented legacy eligibility: count=%d err=%v", count, err)
	}
	credential, err := repo.CredentialForAPIToken(t.Context(), secret)
	if err != nil || credential.InitialPublisher != nil {
		t.Fatalf("legacy credential gained origin: %v", err)
	}
	if _, err := provider.Down(t.Context()); err == nil {
		t.Fatal("migration allowed destructive downgrade")
	}
}
