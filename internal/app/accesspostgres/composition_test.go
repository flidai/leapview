package accesspostgres

import (
	"context"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspg "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// compositionDBStub is sufficient for constructor-only tests. OAuth service
// construction verifies the native transaction method is present but performs
// no database I/O until an OAuth request is served.
type compositionDBStub struct{}

func (compositionDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (compositionDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (compositionDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (compositionDBStub) Begin(context.Context) (pgx.Tx, error) { return nil, nil }

func newCompositionRepository(t *testing.T) *accesspg.Repository {
	t.Helper()
	repository, err := accesspg.NewAccess(compositionDBStub{}, accesspg.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestNewPersistenceLeavesOAuthNilForExternalIssuer(t *testing.T) {
	persistence, err := NewPersistence(newCompositionRepository(t), nil)
	if err != nil {
		t.Fatalf("NewPersistence(external issuer): %v", err)
	}
	if persistence.OAuth != nil {
		t.Fatal("external issuer composition unexpectedly constructed internal OAuth service")
	}
	if err := persistence.Validate(); err != nil {
		t.Fatalf("native persistence validation: %v", err)
	}
	var _ accessmodule.Persistence = persistence
}

func TestNewPersistenceConstructsPostgresOAuth(t *testing.T) {
	persistence, err := NewPersistence(newCompositionRepository(t), &InternalOAuthConfig{
		IssuerURL:   "https://leapview.example",
		ResourceURL: "https://leapview.example/mcp",
		Secret:      []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatalf("NewPersistence(internal issuer): %v", err)
	}
	if persistence.OAuth == nil || !persistence.OAuth.IsPostgresBacked() {
		t.Fatal("internal composition did not construct PostgreSQL-backed OAuth service")
	}
}

func TestNewPersistenceRejectsInvalidInternalOAuthConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  InternalOAuthConfig
		want string
	}{
		{name: "issuer", cfg: InternalOAuthConfig{ResourceURL: "https://leapview.example/mcp", Secret: []byte(strings.Repeat("s", 32))}, want: "issuer"},
		{name: "resource", cfg: InternalOAuthConfig{IssuerURL: "https://leapview.example", Secret: []byte(strings.Repeat("s", 32))}, want: "resource"},
		{name: "secret", cfg: InternalOAuthConfig{IssuerURL: "https://leapview.example", ResourceURL: "https://leapview.example/mcp", Secret: []byte("short")}, want: "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewPersistence(newCompositionRepository(t), &tc.cfg)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("error = %v, want %q validation", err, tc.want)
			}
		})
	}
}

func TestNewPersistenceRejectsNilRepository(t *testing.T) {
	if _, err := NewPersistence(nil, nil); err == nil || !strings.Contains(err.Error(), "access repository") {
		t.Fatalf("nil repository error = %v", err)
	}
}

func TestNewPersistenceWithPostgresOAuthIntegration(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "access_persistence")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspg.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply access schema: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	repository, err := accesspg.NewAccess(pool, accesspg.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPersistence(repository, &InternalOAuthConfig{
		IssuerURL:   "https://leapview.example",
		ResourceURL: "https://leapview.example/mcp",
		Secret:      []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatalf("construct native persistence: %v", err)
	}
	if persistence.OAuth == nil || !persistence.OAuth.IsPostgresBacked() {
		t.Fatal("integration persistence missing PostgreSQL-backed OAuth")
	}
}
