package platform

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestManagedDataMigrationCreatesProjectDeploymentsWithoutLegacyRollouts(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	for _, table := range []string{"project_deployments", "managed_data_environment_pointers", "managed_data_serving_state_bindings", "managed_data_serving_state_binding_sets"} {
		assertTableCount(t, ctx, store, table, 1)
	}
	for _, table := range []string{"managed_data_rollouts", "managed_data_rollout_targets"} {
		assertTableCount(t, ctx, store, table, 0)
	}
	var projectColumnCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('serving_states') WHERE name = 'project_id' AND type = 'TEXT' AND "notnull" = 1`).Scan(&projectColumnCount); err != nil {
		t.Fatalf("inspect serving state project scope: %v", err)
	}
	if projectColumnCount != 1 {
		t.Fatalf("serving state project scope column count = %d, want 1", projectColumnCount)
	}

	var pointerDDL string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'managed_data_environment_pointers'`).Scan(&pointerDDL); err != nil {
		t.Fatalf("inspect environment pointer schema: %v", err)
	}
	if !containsAll(pointerDDL, "deployment_id", "project_deployments") || containsAll(pointerDDL, "rollout_id") {
		t.Fatalf("unexpected environment pointer schema: %s", pointerDDL)
	}
}

// TestDeliveryMigrationChainIsContiguousAndRestartSafe keeps the embedded
// Goose chain's current tail explicit. A fresh install and a second Open both
// apply/verify every migration through 087; a missing or duplicated sequence
// entry would either fail the numeric assertion or leave one of the tail
// columns absent after restart.
func TestDeliveryMigrationChainIsContiguousAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-migrations.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open fresh migrated store: %v", err)
	}
	assertDeliveryMigrationTail(t, ctx, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer restarted.Close()
	assertDeliveryMigrationTail(t, ctx, restarted)
}

// TestDeliveryMigrationUpgradeFrom072 exercises the real upgrade path rather
// than only a fresh install: seed a database through the last pre-delivery
// migration, then let Open apply 073..087 in one restart-safe upgrade.
func TestDeliveryMigrationUpgradeFrom072(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open pre-delivery database: %v", err)
	}
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		legacy.Close()
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 72); err != nil {
		legacy.Close()
		t.Fatalf("seed pre-delivery migrations: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-delivery database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade pre-delivery database: %v", err)
	}
	defer store.Close()
	assertDeliveryMigrationTail(t, ctx, store)
}

func assertDeliveryMigrationTail(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var latest int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1`).Scan(&latest); err != nil {
		t.Fatalf("inspect applied Goose migrations: %v", err)
	}
	if latest != 87 {
		t.Fatalf("latest applied migration = %d, want 87", latest)
	}
	rows, err := store.SQLDB().QueryContext(ctx, `
		SELECT version_id
		FROM goose_db_version
		WHERE is_applied = 1 AND version_id BETWEEN 73 AND 87
		ORDER BY version_id`)
	if err != nil {
		t.Fatalf("inspect applied delivery migration sequence: %v", err)
	}
	defer rows.Close()
	for want := int64(73); want <= 87; want++ {
		if !rows.Next() {
			t.Fatalf("applied delivery migration sequence ended before %d", want)
		}
		var got int64
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan applied delivery migration %d: %v", want, err)
		}
		if got != want {
			t.Fatalf("applied delivery migration sequence contains %d at position %d, want %d", got, want-73, want)
		}
	}
	if rows.Next() {
		var extra int64
		if err := rows.Scan(&extra); err != nil {
			t.Fatalf("scan extra applied delivery migration: %v", err)
		}
		t.Fatalf("applied delivery migration sequence has unexpected extra version %d", extra)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate applied delivery migration sequence: %v", err)
	}
	for table, column := range map[string]string{"delivery_gc_cycles": "actor_id", "delivery_build_attempts": "idempotency_key"} {
		var count int
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&count); err != nil {
			t.Fatalf("inspect migration tail %s.%s: %v", table, column, err)
		}
		if count != 1 {
			t.Fatalf("migration tail column %s.%s count = %d, want 1", table, column, count)
		}
	}
}

func assertTableCount(t *testing.T, ctx context.Context, store *Store, table string, want int) {
	t.Helper()
	var got int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&got); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("table %s count = %d, want %d", table, got, want)
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
