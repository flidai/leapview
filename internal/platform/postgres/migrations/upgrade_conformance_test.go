package migrations

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func newDemoUpgradeDatabase(t *testing.T) (*pgxpool.Pool, *sql.DB, *goose.Provider) {
	t.Helper()
	h := postgrestest.Start(t)
	for _, name := range []string{"leapview_control_owner", "leapview_control_migrator", "leapview_control_runtime", "leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		h.EnsureRole(t, postgrestest.Role{Name: name})
	}
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, postgrestest.Role{Name: "leapview_control_owner"}, "CREATE")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(t.Context(), 28); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO platform.setting(key,value) VALUES ('demo-upgrade-preservation','CFO state must survive')`); err != nil {
		t.Fatal(err)
	}
	return pool, db, provider
}

func TestDemoUpgradePostgreSQL18(t *testing.T) {
	pool, db, provider := newDemoUpgradeDatabase(t)
	var err error
	denied := errors.New("admission denied")
	hookCalled := false
	after := func(context.Context, *sql.DB) error { hookCalled = true; return nil }
	err = ApplyDemoUpgrade(t.Context(), pool, db, func(context.Context) error { return denied }, after)
	if !errors.Is(err, denied) || hookCalled {
		t.Fatalf("denied admission: %v, hook=%t", err, hookCalled)
	}
	before, _, err := provider.GetVersions(t.Context())
	if err != nil || before != 28 {
		t.Fatalf("denied admission changed schema: %d %v", before, err)
	}
	if err = ApplyDemoUpgrade(t.Context(), pool, db, func(context.Context) error { return nil }, after); err != nil {
		t.Fatal(err)
	}
	if !hookCalled {
		t.Fatal("role policy hook omitted")
	}
	if err = VerifyGoose(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var value string
	if err = pool.QueryRow(t.Context(), `SELECT value FROM platform.setting WHERE key='demo-upgrade-preservation'`).Scan(&value); err != nil || value != "CFO state must survive" {
		t.Fatalf("state lost: %q %v", value, err)
	}
	var table, column bool
	if err = pool.QueryRow(t.Context(), `SELECT to_regclass('agent.configuration_revisions') IS NOT NULL, EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='access' AND table_name='session' AND column_name='client_label')`).Scan(&table, &column); err != nil || !table || !column {
		t.Fatalf("missing upgraded structures: %t %t %v", table, column, err)
	}
	hookCalled = false
	if err = ApplyDemoUpgrade(t.Context(), pool, db, func(context.Context) error { return nil }, after); err == nil || hookCalled {
		t.Fatal("already-migrated database was blindly replayed")
	}
}

func TestDemoUpgradePartialMigrationPostgreSQL18RequiresRecovery(t *testing.T) {
	pool, db, provider := newDemoUpgradeDatabase(t)
	// Fail the real 030 transaction after 029 has committed. This deliberately
	// exercises a durable partial migration, rather than a mocked SQL failure.
	_, err := pool.Exec(t.Context(), `CREATE FUNCTION public.fail_demo_session_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$
 BEGIN
  IF EXISTS(SELECT 1 FROM pg_event_trigger_ddl_commands() WHERE command_tag='ALTER TABLE' AND object_identity='access.session') THEN
   RAISE EXCEPTION 'injected migration 030 failure';
  END IF;
 END $$;
 CREATE EVENT TRIGGER fail_demo_session_ddl ON ddl_command_end EXECUTE FUNCTION public.fail_demo_session_ddl();`)
	if err != nil {
		t.Fatal(err)
	}
	hookCalled := false
	after := func(context.Context, *sql.DB) error { hookCalled = true; return nil }
	if err = ApplyDemoUpgrade(t.Context(), pool, db, func(context.Context) error { return nil }, after); err == nil {
		t.Fatal("injected migration failure accepted")
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil || current != 29 || hookCalled {
		t.Fatalf("partial boundary: %d, hook=%t, err=%v", current, hookCalled, err)
	}
	if err = ApplyDemoUpgrade(t.Context(), pool, db, func(context.Context) error { return nil }, after); err == nil {
		t.Fatal("partial migration blindly resumed")
	}
	var value string
	if err = pool.QueryRow(t.Context(), `SELECT value FROM platform.setting WHERE key='demo-upgrade-preservation'`).Scan(&value); err != nil || value != "CFO state must survive" {
		t.Fatalf("predecessor data lost: %q %v", value, err)
	}
}
