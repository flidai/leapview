package platform

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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
