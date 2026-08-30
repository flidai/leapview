package postgresauthority

import (
	"testing"

	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestResolveInstanceIdentityIsDurableAndEnvironmentBound(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "postgres_authority_identity")
	pool, err := platformpostgres.Open(t.Context(), platformpostgres.Config{
		URL: database.AdminURL(), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: "postgres", Intent: platformpostgres.IntentReadWrite,
		MinConns: 0, MaxConns: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := platformbootstrappostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	first, err := ResolveInstanceIdentity(t.Context(), pool, "prod")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveInstanceIdentity(t.Context(), pool, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second != first {
		t.Fatalf("instance identity was not durable: first=%q second=%q", first, second)
	}
	if _, err := ResolveInstanceIdentity(t.Context(), pool, "staging"); err == nil {
		t.Fatal("instance identity accepted a different environment")
	}
}
