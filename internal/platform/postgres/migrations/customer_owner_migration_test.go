package migrations

import (
	"database/sql"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestCustomerOwnerMigrationRunsThroughGooseAndRestrictsRoles(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "bootstrap_customer_owner_049")
	h.GrantDatabase(t, database.Name, owner, "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "SET ROLE "+owner.Name); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), `
		CREATE SCHEMA platform;
		CREATE TABLE platform.instance_identity (
			singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
			instance_id text NOT NULL UNIQUE
		);
		CREATE FUNCTION platform.reject_bootstrap_immutable_mutation()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'platform bootstrap identity and claims are immutable'; END;
		$$;
		INSERT INTO platform.instance_identity(singleton_id, instance_id)
		VALUES (1, 'lvinst_0123456789abcdefghijklmnopqrstuv');
	`)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "RESET ROLE"); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	migration, err := MigrationFS().Open("049_instance_customer_owner.sql")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(migration)
	if err != nil {
		_ = migration.Close()
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	provider, err := newProviderWithoutLock(db, fstest.MapFS{
		"049_instance_customer_owner.sql": &fstest.MapFile{Data: body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply customer owner migration through Goose: %v", err)
	}

	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('platform.instance_customer_owner') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("migration did not create platform.instance_customer_owner")
	}
	var runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert, backupSelect, backupInsert bool
	query := `SELECT has_table_privilege($1, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'INSERT'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'UPDATE'),
		has_table_privilege($1, 'platform.instance_customer_owner', 'DELETE'),
		has_table_privilege($2, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($2, 'platform.instance_customer_owner', 'INSERT'),
		has_table_privilege($3, 'platform.instance_customer_owner', 'SELECT'),
		has_table_privilege($3, 'platform.instance_customer_owner', 'INSERT')`
	if err := db.QueryRow(query, runtime.Name, readonly.Name, backup.Name).Scan(
		&runtimeSelect, &runtimeInsert, &runtimeUpdate, &runtimeDelete,
		&readonlySelect, &readonlyInsert, &backupSelect, &backupInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !runtimeSelect || !runtimeInsert || runtimeUpdate || runtimeDelete || !readonlySelect || readonlyInsert || !backupSelect || backupInsert {
		t.Fatalf("customer owner migration grants runtime=%t/%t/%t/%t readonly=%t/%t backup=%t/%t", runtimeSelect, runtimeInsert, runtimeUpdate, runtimeDelete, readonlySelect, readonlyInsert, backupSelect, backupInsert)
	}

	if _, err := db.Exec(`INSERT INTO platform.instance_customer_owner(singleton_id, instance_id, owner_id) VALUES (1, 'lvinst_0123456789abcdefghijklmnopqrstuv', 'customer_migration')`); err != nil {
		t.Fatalf("insert customer owner: %v", err)
	}
	var declaredAtOK bool
	if err := db.QueryRow(`SELECT declared_at <= clock_timestamp() AND declared_at > clock_timestamp() - interval '1 minute' FROM platform.instance_customer_owner`).Scan(&declaredAtOK); err != nil {
		t.Fatal(err)
	}
	if !declaredAtOK {
		t.Fatal("customer owner migration did not default declared_at from database time")
	}
	if _, err := db.Exec(`UPDATE platform.instance_customer_owner SET owner_id = 'customer_changed'`); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("customer owner mutation error = %v, want immutable trigger rejection", err)
	}
}
