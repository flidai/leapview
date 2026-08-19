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
// apply/verify every migration through 090; a missing or duplicated sequence
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
// migration, then let Open apply 073..089 in one restart-safe upgrade.
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

func TestDeliveryMigrationUpgradePreservesBuildAttemptDependents(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-dependent-upgrade.db")
	legacy, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open pre-088 database: %v", err)
	}
	legacy.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		legacy.Close()
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, legacy, "migrations", 87); err != nil {
		legacy.Close()
		t.Fatalf("seed pre-088 migrations: %v", err)
	}
	sha := "sha256:" + strings.Repeat("a", 64)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO delivery_target_revisions (target_id, project_id, environment, created_at, updated_at)
			VALUES ('target-dependent', 'project-dependent', 'prod', ?, ?)`, []any{"2026-08-17T00:00:00Z", "2026-08-17T00:00:00Z"}},
		{`INSERT INTO physical_pools (
			id, identity_digest, storage_location, storage_namespace,
			storage_implementation, object_naming_contract, isolation_boundary,
			retention_authority, retention_policy_json
			) VALUES (?, ?, 's3://warehouse', 'dependent-upgrade', 's3',
			'sha256-object-names-v1', 'target-dependent', 'retention-dependent', '{}')`, []any{sha, sha}},
		{`INSERT INTO delivery_plans (
			id, target_id, project_id, environment, operation_kind, source_digest,
			base_target_revision, execution_digest, execution_inputs_json,
			provenance_json, governance_json, provenance_digest, governance_digest,
			plan_digest, expires_at, created_at
			) VALUES ('plan-dependent', 'target-dependent', 'project-dependent', 'prod',
			'code_change', ?, 0, ?, '{}', '{}', '{}', ?, ?, ?, '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, []any{sha, sha, sha, sha, sha}},
		{`INSERT INTO delivery_writer_leases (id, attempt_id, physical_pool_id, owner_id, epoch, expires_at, created_at)
			VALUES ('writer-dependent', 'attempt-dependent', ?, 'owner-dependent', 1, '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, []any{sha}},
		{`INSERT INTO delivery_build_attempts (
			id, plan_id, plan_digest, source_digest, execution_digest,
			physical_pool_id, writer_lease_id, status, revision, created_at, updated_at
			) VALUES ('attempt-dependent', 'plan-dependent', ?, ?, ?, ?, 'writer-dependent',
			'building', 1, '2026-08-17T00:00:00Z', '2026-08-17T00:00:00Z')`, []any{sha, sha, sha, sha}},
		{`INSERT INTO delivery_build_artifact_bindings (
			attempt_id, serving_artifact_id, serving_artifact_digest, serving_state_id, created_at
			) VALUES ('attempt-dependent', 'artifact-dependent', ?, 'state-dependent', '2026-08-17T00:00:00Z')`, []any{sha}},
	} {
		if _, err := legacy.ExecContext(ctx, statement.query, statement.args...); err != nil {
			legacy.Close()
			t.Fatalf("seed dependent delivery row: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-088 database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade pre-088 database with dependent rows: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM delivery_build_attempts WHERE id = 'attempt-dependent'`).Scan(&count); err != nil {
		t.Fatalf("read preserved build attempt: %v", err)
	}
	if count != 1 {
		t.Fatalf("preserved build attempt count = %d, want 1", count)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM delivery_build_artifact_bindings WHERE attempt_id = 'attempt-dependent'`).Scan(&count); err != nil {
		t.Fatalf("read preserved build artifact binding: %v", err)
	}
	if count != 1 {
		t.Fatalf("preserved build artifact binding count = %d, want 1", count)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `
		SELECT count(*)
		FROM pragma_foreign_key_list('delivery_build_artifact_bindings')
		WHERE "table" = 'delivery_build_attempts'`).Scan(&count); err != nil {
		t.Fatalf("inspect preserved build-artifact foreign key: %v", err)
	}
	if count != 1 {
		t.Fatalf("build-artifact foreign key count = %d, want 1", count)
	}
	for _, name := range []string{
		"delivery_build_attempts_sealed_plan_idx",
		"delivery_build_attempts_plan_idempotency_idx",
		"delivery_build_attempts_root_revision_insert",
		"delivery_build_attempts_root_revision_update",
		"delivery_build_attempts_root_revision_delete",
	} {
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("inspect preserved delivery object %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("delivery object %s count = %d, want 1", name, count)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `DELETE FROM delivery_build_attempts WHERE id = 'attempt-dependent'`); err != nil {
		t.Fatalf("delete preserved build attempt: %v", err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM delivery_build_artifact_bindings WHERE attempt_id = 'attempt-dependent'`).Scan(&count); err != nil {
		t.Fatalf("read cascaded build artifact binding: %v", err)
	}
	if count != 0 {
		t.Fatalf("cascaded build artifact binding count = %d, want 0", count)
	}
}

func assertDeliveryMigrationTail(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var latest int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied = 1`).Scan(&latest); err != nil {
		t.Fatalf("inspect applied Goose migrations: %v", err)
	}
	if latest != 91 {
		t.Fatalf("latest applied migration = %d, want 91", latest)
	}
	rows, err := store.SQLDB().QueryContext(ctx, `
		SELECT version_id
		FROM goose_db_version
		WHERE is_applied = 1 AND version_id BETWEEN 73 AND 91
		ORDER BY version_id`)
	if err != nil {
		t.Fatalf("inspect applied delivery migration sequence: %v", err)
	}
	defer rows.Close()
	for want := int64(73); want <= 91; want++ {
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
