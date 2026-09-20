package migrations

import (
	"database/sql"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPlatformRoleOperationMigrationUpgradesRevisionNineteen proves that a
// control database created before the replay table was added receives the
// table, keeps existing role bindings, and can use the runtime ACL afterward.
func TestPlatformRoleOperationMigrationUpgradesRevisionNineteen(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "platform-operation-migration"})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "platform-operation-runtime"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "platform_role_operation_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, runtime, "CONNECT")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `
		ALTER DATABASE platform_role_operation_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })

	// This is the relevant portion of the 001 baseline. The no-op revisions
	// model the database's Goose frontier at 019 without rerunning unrelated
	// control-plane DDL in this focused upgrade test.
	previous := fstest.MapFS{
		"001_access_fixture.sql": {Data: []byte(`-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE SCHEMA access;
CREATE TABLE access.principal (
    id uuid PRIMARY KEY,
    principal_type text NOT NULL,
    status text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE access.platform_role_binding (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    role text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
-- +goose StatementBegin
CREATE FUNCTION access.reject_access_delete()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    RAISE EXCEPTION 'access history is append-only; revoke instead of delete';
END;
$$;
-- +goose StatementEnd
INSERT INTO access.principal(id, principal_type, status)
VALUES ('00000000-0000-7000-8000-000000000001', 'user', 'active');
INSERT INTO access.platform_role_binding(id, principal_id, role)
VALUES ('00000000-0000-7000-8000-000000000002', '00000000-0000-7000-8000-000000000001', 'platform_admin');
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP SCHEMA access CASCADE;
RESET ROLE;
`)},
	}
	for revision := 2; revision <= 19; revision++ {
		name := fmt.Sprintf("%03d_placeholder.sql", revision)
		previous[name] = &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")}
	}
	provider, err := newProvider(migrationDB, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply revision-019 fixture: %v", err)
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 19 {
		t.Fatalf("fixture revision = %d, want 19", current)
	}
	var operationExists bool
	if err := admin.QueryRow(t.Context(), `SELECT to_regclass('access.platform_role_operation') IS NOT NULL`).Scan(&operationExists); err != nil {
		t.Fatal(err)
	}
	if operationExists {
		t.Fatal("revision-019 fixture already contains platform role operation table")
	}

	migration, err := fs.ReadFile(MigrationFS(), "020_platform_role_operation.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade := make(fstest.MapFS, len(previous)+1)
	for name, file := range previous {
		upgrade[name] = file
	}
	upgrade["020_platform_role_operation.sql"] = &fstest.MapFile{Data: migration}
	provider, err = newProvider(migrationDB, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade revision-019 fixture: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("replay platform role operation migration: %v", err)
	}
	current, _, err = provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 20 {
		t.Fatalf("upgraded revision = %d, want 20", current)
	}

	var bindingCount int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM access.platform_role_binding`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 {
		t.Fatalf("pre-existing platform role bindings = %d, want 1", bindingCount)
	}
	var runtimeInsert, runtimeUpdate, readonlySelect, backupSelect bool
	if err := admin.QueryRow(t.Context(), `
		SELECT has_table_privilege('leapview_control_runtime', 'access.platform_role_operation', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'access.platform_role_operation', 'UPDATE'),
		       has_table_privilege('leapview_control_readonly', 'access.platform_role_operation', 'SELECT'),
		       has_table_privilege('leapview_control_backup', 'access.platform_role_operation', 'SELECT')`).
		Scan(&runtimeInsert, &runtimeUpdate, &readonlySelect, &backupSelect); err != nil {
		t.Fatal(err)
	}
	if !runtimeInsert || runtimeUpdate || !readonlySelect || !backupSelect {
		t.Fatalf("platform role operation ACLs runtime insert/update=%t/%t readonly select=%t backup select=%t", runtimeInsert, runtimeUpdate, readonlySelect, backupSelect)
	}

	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if _, err := runtimeDB.Exec(t.Context(), `
		INSERT INTO access.platform_role_operation(
		    idempotency_key, request_digest, action, principal_id, binding_id, result_revision
		) VALUES ($1, $2, $3, $4::uuid, $5::uuid, $6)`,
		"platform-upgrade-operation", "sha256:"+fmt.Sprintf("%064d", 1), "grant",
		"00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000002", "sha256:"+fmt.Sprintf("%064d", 2)); err != nil {
		t.Fatalf("runtime insert into upgraded operation table: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE access.platform_role_operation SET action='revoke' WHERE idempotency_key='platform-upgrade-operation'`); err == nil {
		t.Fatal("platform role operation update unexpectedly succeeded")
	}
	if _, err := admin.Exec(t.Context(), `DELETE FROM access.platform_role_operation WHERE idempotency_key='platform-upgrade-operation'`); err == nil {
		t.Fatal("platform role operation delete unexpectedly succeeded")
	}
	if _, err := provider.DownTo(t.Context(), 19); err == nil {
		t.Fatal("platform role operation migration Down unexpectedly succeeded")
	}
	if err := admin.QueryRow(t.Context(), `SELECT to_regclass('access.platform_role_operation') IS NOT NULL`).Scan(&operationExists); err != nil {
		t.Fatal(err)
	}
	if !operationExists {
		t.Fatal("failed migration Down removed platform role operation table")
	}
}
