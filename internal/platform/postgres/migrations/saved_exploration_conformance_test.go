package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// This is intentionally an upgrade test, not a second schema unit test. It
// applies the released control-plane migrations through revision six,
// upgrades with 007, and then exercises the exact roles used by production
// pools. The checks around the upgrade ensure the dashboard builder's released
// lock/evidence guards remain present while saved-exploration guards are added.
func TestSavedExplorationMigrationUpgradeAndRoleBoundary(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "saved-migration", Login: true})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "saved-runtime", Login: true})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "saved-readonly", Login: true})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Password: "saved-backup", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "saved-maintenance", Login: true})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "saved_exploration_upgrade")
	for _, role := range []postgrestest.Role{owner, migrator, runtime, readonly, backup, maintenance} {
		h.GrantDatabase(t, database.Name, role, "CONNECT")
	}
	h.GrantDatabase(t, database.Name, owner, "CREATE")
	h.GrantDatabase(t, database.Name, migrator, "CREATE")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), "ALTER DATABASE "+database.Name+" OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator"); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	provider, err := platformmigrations.NewProvider(migrationDB)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatalf("apply migrations through released revision six: %v", err)
	}
	if current, target, err := provider.GetVersions(ctx); err != nil {
		t.Fatal(err)
	} else if current != 6 || target != platformmigrations.CurrentRevision {
		t.Fatalf("pre-upgrade Goose versions = %d/%d, want 6/%d", current, target, platformmigrations.CurrentRevision)
	}
	assertDashboardBuilderGuards(t, ctx, admin, "released revision six")
	assertResourceUIDRegistry(t, ctx, admin, "released revision six")
	assertRecoverySuccessorV3(t, ctx, admin, "released revision six")
	if err := postgresbaseline.Apply(ctx, migrationDB); err != nil {
		t.Fatalf("upgrade to saved exploration revision seven: %v", err)
	}
	assertDashboardBuilderGuards(t, ctx, admin, "saved exploration revision seven")
	assertResourceUIDRegistry(t, ctx, admin, "saved exploration revision seven")
	assertRecoverySuccessorV3(t, ctx, admin, "saved exploration revision seven")
	assertSavedExplorationGuards(t, ctx, admin)

	// VerifyGoose is the startup read-only gate. It must succeed through the
	// maintenance authority without invoking the explicit upgrade path.
	maintenanceDB, err := sql.Open("pgx", database.URL(maintenance))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = maintenanceDB.Close() })
	if err := postgresbaseline.Verify(ctx, maintenanceDB); err != nil {
		t.Fatalf("maintenance startup verification: %v", err)
	}

	var runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert bool
	if err := admin.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'saved_exploration.saved_explorations', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'saved_exploration.saved_explorations', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'saved_exploration.saved_explorations', 'DELETE'),
		       has_table_privilege('leapview_control_readonly', 'saved_exploration.saved_explorations', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'saved_exploration.saved_explorations', 'INSERT')`).
		Scan(&runtimeInsert, &runtimeUpdate, &runtimeDelete, &readonlySelect, &readonlyInsert); err != nil {
		t.Fatal(err)
	}
	if !runtimeInsert || !runtimeUpdate || !runtimeDelete || !readonlySelect || readonlyInsert {
		t.Fatalf("saved exploration role ACL = runtime insert/update/delete %t/%t/%t, readonly select/insert %t/%t", runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert)
	}

	runtimePool, err := pgxpool.New(ctx, database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimePool.Close)
	createdAt := "2026-09-04T12:00:00.123456789Z"
	hash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	payload := []byte(`{"version":1,"spec":{"modelId":"semantic:sales"}}`)
	tx, err := runtimePool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_exploration.saved_exploration_revisions
			(project_id, exploration_id, revision_id, revision_number, spec_envelope_version,
			 spec_canonical_json, content_hash, created_by, created_at, serving_project_id,
			 serving_environment, serving_generation_id)
		VALUES ('project:sales', 'runtime-exploration', 'revision-1', 1, 1, $1, $2,
			'runtime-actor', $3, 'project:sales', 'production', 'generation-1')`, payload, hash, createdAt); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("runtime revision write: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO saved_exploration.saved_explorations
			(project_id, exploration_id, owner_principal_id, title, slug, visibility, status,
			 semantic_model_id, created_at, updated_at, current_revision_id,
			 current_revision_number, current_content_hash)
		VALUES ('project:sales', 'runtime-exploration', 'runtime-owner', 'Runtime orders',
			'runtime-orders', 'private', 'active', 'semantic:sales', $1, $1,
			'revision-1', 1, $2)`, createdAt, hash); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("runtime lifecycle write: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("runtime saved exploration commit: %v", err)
	}
	var count int
	if err := runtimePool.QueryRow(ctx, `SELECT count(*) FROM saved_exploration.saved_explorations WHERE exploration_id = 'runtime-exploration'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runtime saved exploration count = %d, want 1", count)
	}
	if _, err := runtimePool.Exec(ctx, `ALTER TABLE saved_exploration.saved_explorations DISABLE TRIGGER ALL`); err == nil {
		t.Fatal("runtime role disabled saved exploration triggers")
	} else {
		assertPostgreSQLState(t, "runtime trigger disable", err, "42501")
	}
	if _, err := runtimePool.Exec(ctx, `DROP TABLE saved_exploration.saved_explorations`); err == nil {
		t.Fatal("runtime role dropped saved exploration table")
	} else {
		assertPostgreSQLState(t, "runtime table drop", err, "42501")
	}

	readonlyPool, err := pgxpool.New(ctx, database.URL(readonly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyPool.Close)
	if err := readonlyPool.QueryRow(ctx, `SELECT count(*) FROM saved_exploration.saved_explorations`).Scan(&count); err != nil {
		t.Fatalf("readonly saved exploration read: %v", err)
	}
	if count != 1 {
		t.Fatalf("readonly saved exploration count = %d, want 1", count)
	}
	if _, err := readonlyPool.Exec(ctx, `DELETE FROM saved_exploration.saved_explorations WHERE exploration_id = 'runtime-exploration'`); err == nil {
		t.Fatal("readonly role deleted a saved exploration")
	} else {
		assertPostgreSQLState(t, "readonly delete", err, "42501")
	}
	if _, err := readonlyPool.Exec(ctx, `INSERT INTO saved_exploration.saved_explorations DEFAULT VALUES`); err == nil {
		t.Fatal("readonly role inserted a saved exploration")
	} else {
		assertPostgreSQLState(t, "readonly insert", err, "42501")
	}
}

func assertDashboardBuilderGuards(t *testing.T, ctx context.Context, db *pgxpool.Pool, stage string) {
	t.Helper()
	var builderLock, builderEvidence bool
	if err := db.QueryRow(ctx, `
		SELECT to_regprocedure('dashboard.lock_authoring_dashboard(text,text)') IS NOT NULL,
		       to_regprocedure('dashboard.guard_authoring_dashboard_evidence()') IS NOT NULL`).
		Scan(&builderLock, &builderEvidence); err != nil {
		t.Fatalf("%s migration guard query: %v", stage, err)
	}
	if !builderLock || !builderEvidence {
		t.Fatalf("%s dashboard builder guards = lock/evidence %t/%t", stage, builderLock, builderEvidence)
	}
}

func assertResourceUIDRegistry(t *testing.T, ctx context.Context, db *pgxpool.Pool, stage string) {
	t.Helper()
	var registry, generation, inventory, tombstone, restore bool
	if err := db.QueryRow(ctx, `
		SELECT to_regclass('project.resource_uid_registry') IS NOT NULL,
		       to_regclass('project.resource_uid_generation') IS NOT NULL,
		       to_regclass('project.resource_uid_inventory') IS NOT NULL,
		       to_regclass('project.resource_uid_tombstone') IS NOT NULL,
		       to_regclass('project.resource_uid_restore_authorization') IS NOT NULL`).
		Scan(&registry, &generation, &inventory, &tombstone, &restore); err != nil {
		t.Fatalf("%s ResourceUID registry query: %v", stage, err)
	}
	if !registry || !generation || !inventory || !tombstone || !restore {
		t.Fatalf("%s ResourceUID registry tables = registry/generation/inventory/tombstone/restore %t/%t/%t/%t/%t", stage, registry, generation, inventory, tombstone, restore)
	}
}

func assertRecoverySuccessorV3(t *testing.T, ctx context.Context, db *pgxpool.Pool, stage string) {
	t.Helper()
	var tableCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_class AS c
		  JOIN pg_namespace AS n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'recovery'
		   AND c.relname = ANY($1::text[])`, []string{
		"set_identity_registry",
		"successor_evidence_v2",
		"successor_evidence_locator_v2",
		"successor_manifest_binding",
		"successor_trust_generation",
		"recovery_set_v3",
		"recovery_set_v3_root",
	}).Scan(&tableCount); err != nil {
		t.Fatalf("%s RecoverySet v3 table query: %v", stage, err)
	}
	if tableCount != 7 {
		t.Fatalf("%s RecoverySet v3 table count = %d, want 7", stage, tableCount)
	}
}

func assertSavedExplorationGuards(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	var operationSnapshot, lifecycle, currentRevision, revisionInsert bool
	if err := db.QueryRow(ctx, `
		SELECT to_regprocedure('saved_exploration.validate_operation_snapshot()') IS NOT NULL,
		       to_regprocedure('saved_exploration.validate_lifecycle_mutation()') IS NOT NULL,
		       to_regprocedure('saved_exploration.validate_current_revision()') IS NOT NULL,
		       to_regprocedure('saved_exploration.validate_revision_insert()') IS NOT NULL`).
		Scan(&operationSnapshot, &lifecycle, &currentRevision, &revisionInsert); err != nil {
		t.Fatalf("saved exploration migration guard query: %v", err)
	}
	if !operationSnapshot || !lifecycle || !currentRevision || !revisionInsert {
		t.Fatalf("saved exploration guards = operation/lifecycle/current-revision/revision-insert %t/%t/%t/%t", operationSnapshot, lifecycle, currentRevision, revisionInsert)
	}
}

func assertPostgreSQLState(t *testing.T, action string, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s returned non-PostgreSQL error %T: %v", action, err, err)
	}
	if pgErr.Code != want {
		t.Fatalf("%s SQLSTATE = %s (%s), want %s", action, pgErr.Code, pgErr.Message, want)
	}
}

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 90*time.Second)
}
