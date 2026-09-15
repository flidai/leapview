package migrations

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReleaseMigrationFenceReportsUnknownSessionLock(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "migration_fence_unknown")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := releaseMigrationFence(ctx, conn); err == nil {
		t.Fatal("unlock of an unheld migration fence unexpectedly succeeded")
	}
}

func TestWithMigrationFenceClosesSessionWhenUnlockStateIsUnknown(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "migration_fence_cleanup")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	err = withMigrationFence(ctx, pool, func(conn *pgxpool.Conn) error {
		// Simulate an error path that loses the lock before the deferred
		// cleanup runs. The cleanup must observe the unknown unlock result and
		// destroy, rather than return, the dedicated session.
		_, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, AdvisoryLockKey)
		return err
	})
	if err == nil {
		t.Fatal("migration fence with externally released lock unexpectedly succeeded")
	}
}
