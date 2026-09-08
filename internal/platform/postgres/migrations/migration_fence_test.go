package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestApplyRiverAndGooseWaitsForSharedAdvisoryFence(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "migration_fence")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, platformmigrations.AdvisoryLockKey); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDB.Close()
	blockedCtx, blockedCancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer blockedCancel()
	done := make(chan error, 1)
	go func() {
		done <- platformmigrations.ApplyRiverAndGoose(blockedCtx, pool, migrationDB, nil)
	}()

	err = <-done
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration initializer error = %v, want advisory-fence wait to honor context deadline", err)
	}
	var riverTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.river_job')`).Scan(&riverTable); err != nil {
		t.Fatal(err)
	}
	if riverTable != nil {
		t.Fatalf("River schema was initialized while another migration owner held advisory lock: %q", *riverTable)
	}
}
