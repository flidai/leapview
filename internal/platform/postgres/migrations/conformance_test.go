package migrations_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAccessBaselinePostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "leapview-conformance-secret", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	h.GrantRole(t, owner, migrator)

	database := h.NewDatabase(t, "leapview_control")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	apply := func() {
		t.Helper()
		conn, err := admin.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		if _, err := conn.Exec(ctx, "SET ROLE leapview_control_migrator"); err != nil {
			t.Fatal(err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := postgresbaseline.Apply(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply baseline: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	apply()

	var revision int64
	var migrationID, checksum string
	if err := admin.QueryRow(ctx,
		"SELECT revision, migration_id, checksum FROM platform.schema_revision WHERE revision = $1",
		postgresbaseline.BaselineRevision,
	).Scan(&revision, &migrationID, &checksum); err != nil {
		t.Fatal(err)
	}
	if revision != postgresbaseline.BaselineRevision ||
		migrationID != postgresbaseline.BaselineMigrationID ||
		checksum != postgresbaseline.Checksum() {
		t.Fatalf("baseline identity = %d/%q/%q", revision, migrationID, checksum)
	}

	for _, schema := range []string{"platform", "access", "audit", "physical_pool", "ducklake"} {
		var ownerName string
		if err := admin.QueryRow(ctx,
			"SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = $1",
			schema,
		).Scan(&ownerName); err != nil {
			t.Fatal(err)
		}
		if ownerName != owner.Name {
			t.Errorf("schema %s owner = %q, want %q", schema, ownerName, owner.Name)
		}
	}
	for _, deferred := range []string{"delivery", "jobs", "cache", "lineage", "attribute"} {
		var exists bool
		if err := admin.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)",
			deferred,
		).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("deferred schema %q was installed", deferred)
		}
	}

	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	readonly, err := pgxpool.New(ctx, database.URL(readonlyRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonly.Close)

	if _, err := runtime.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, source, operation, action, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest)
		VALUES ('00000000-0000-0000-0000-000000000001', 'test', 'append',
		        'test.append', '', 'success', 'test:append', 0,
		        'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err != nil {
		t.Fatalf("runtime audit append: %v", err)
	}
	if _, err := runtime.Exec(ctx,
		"UPDATE audit.audit_event SET action = 'tampered' WHERE audit_id = '00000000-0000-0000-0000-000000000001'",
	); err == nil {
		t.Fatal("runtime updated immutable audit evidence")
	}
	if _, err := readonly.Exec(ctx, "SELECT id FROM access.principal LIMIT 0"); err != nil {
		t.Fatalf("readonly safe metadata: %v", err)
	}
	for _, table := range []string{"session", "local_credential", "api_token", "service_principal_secret", "authoring_credential"} {
		if _, err := readonly.Exec(ctx, "SELECT * FROM access."+table+" LIMIT 0"); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("readonly credential access %s error = %v, want permission denied", table, err)
		}
	}
	if _, err := admin.Exec(ctx,
		"UPDATE platform.schema_revision SET migration_id = 'tampered' WHERE revision = $1",
		postgresbaseline.BaselineRevision,
	); err == nil {
		t.Fatal("schema revision append-only trigger accepted an update")
	}

	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := admin.Exec(ctx, `
		INSERT INTO ducklake.catalog_identity
		    (physical_pool_id, catalog_database, catalog_id, catalog_uuid,
		     metadata_schema, compatibility_digest, catalog_schema_version)
		VALUES ($1, 'leapview_ducklake', $2,
		        '00000000-0000-5000-8000-000000000001', 'leapview_catalog_test', $3, '0.3')`,
		"pool-test", "ducklake:pool-test", digest,
	); err != nil {
		t.Fatalf("insert catalog identity fixture: %v", err)
	}
	if _, err := admin.Exec(ctx, "UPDATE ducklake.catalog_identity SET catalog_id='tampered' WHERE physical_pool_id='pool-test'"); err == nil {
		t.Fatal("catalog identity immutable trigger accepted an update")
	}
	if _, err := runtime.Exec(ctx, `
		INSERT INTO ducklake.catalog_identity
		    (physical_pool_id, catalog_database, catalog_id, catalog_uuid,
		     metadata_schema, compatibility_digest, catalog_schema_version)
		VALUES ('forged', 'leapview_ducklake', 'ducklake:forged',
		        '00000000-0000-5000-8000-000000000002', 'leapview_catalog_forged', $1, '0.3')`, digest,
	); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("runtime catalog identity write error = %v, want permission denied", err)
	}
}
