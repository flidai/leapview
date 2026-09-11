package app

import (
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	savedpostgres "github.com/flidai/leapview/internal/analytics/exploration/saved/postgres"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/savedexplorationaudit"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// savedPostgresFixture supplies real native persistence to the app's isolated
// routing fixtures. Other capability fixtures remain test-only; saved state
// and its canonical audit event share this PostgreSQL transaction authority.
func savedPostgresFixture(t *testing.T) (*pgxpool.Pool, analyticsmodule.SavedExplorationRepository) {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "saved_app_fixture")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := savedpostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	audit := savedexplorationaudit.NewWithRepository(accesspostgres.New())
	return pool, analyticsmodule.NewSavedExplorationPostgresRepository(pool, audit)
}
