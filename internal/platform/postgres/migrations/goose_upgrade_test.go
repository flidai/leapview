package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestGooseUpgradesPreviousVersionAcrossTransactionalAndNoTransactionMigrations(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "goose_upgrade_path")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	previous := fstest.MapFS{
		"001_probe.sql": {Data: []byte(`-- +goose Up
CREATE TABLE upgrade_probe (id bigint PRIMARY KEY, value text NOT NULL);
INSERT INTO upgrade_probe(id,value) VALUES (1,'baseline');

-- +goose Down
DROP TABLE upgrade_probe;
`)},
	}
	provider, err := newProvider(db, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply previous version: %v", err)
	}

	upgrade := fstest.MapFS{
		"001_probe.sql": previous["001_probe.sql"],
		"002_transactional.sql": {Data: []byte(`-- +goose Up
ALTER TABLE upgrade_probe ADD COLUMN revision bigint NOT NULL DEFAULT 1;
UPDATE upgrade_probe SET value='transactional';

-- +goose Down
ALTER TABLE upgrade_probe DROP COLUMN revision;
`)},
		"003_no_transaction.sql": {Data: []byte(`-- +goose NO TRANSACTION
-- +goose Up
CREATE INDEX CONCURRENTLY upgrade_probe_value_idx ON upgrade_probe(value);
INSERT INTO upgrade_probe(id,value,revision) VALUES (2,'non-transactional',2);

-- +goose Down
DROP INDEX CONCURRENTLY upgrade_probe_value_idx;
DELETE FROM upgrade_probe WHERE id=2;
`)},
	}
	provider, err = newProvider(db, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("upgrade previous version: %v", err)
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != 3 || target != 3 {
		t.Fatalf("Goose versions=%d/%d, want 3/3", current, target)
	}
	var rows int
	var indexValid bool
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM upgrade_probe`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid='upgrade_probe_value_idx'::regclass`).Scan(&indexValid); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || !indexValid {
		t.Fatalf("upgrade result rows=%d index_valid=%t", rows, indexValid)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("replay upgraded version: %v", err)
	}
}

func TestAgentConversationDeleteMigrationUpgradesVersionEightAndCascades(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "migration-secret"})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "runtime-secret"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "agent_delete_migration")
	harness.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT")
	harness.GrantDatabase(t, database.Name, runtime, "CONNECT")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}

	fixture := []byte(`-- +goose Up
SET LOCAL ROLE leapview_control_owner;
CREATE SCHEMA agent;
CREATE TABLE agent.conversations (
    id text PRIMARY KEY,
    principal_id text NOT NULL,
    title text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'archived')),
    metadata_json jsonb NOT NULL DEFAULT '{}',
    transcript_json jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at timestamptz
);
CREATE TABLE agent.runs (
    id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    status text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE agent.messages (
    id text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    run_id text REFERENCES agent.runs(id) ON DELETE SET NULL,
    sequence bigint NOT NULL,
    content_text text NOT NULL DEFAULT ''
);
CREATE TABLE agent.events (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id text NOT NULL REFERENCES agent.runs(id) ON DELETE CASCADE,
    aggregate_version bigint NOT NULL
);
-- +goose StatementBegin
CREATE FUNCTION agent.reject_history_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    RAISE EXCEPTION 'agent history is immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER conversations_no_delete BEFORE DELETE ON agent.conversations FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
CREATE TRIGGER runs_no_delete BEFORE DELETE ON agent.runs FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
CREATE TRIGGER messages_no_delete BEFORE DELETE ON agent.messages FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
CREATE TRIGGER events_append_only BEFORE DELETE ON agent.events FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
GRANT USAGE ON SCHEMA agent TO leapview_control_runtime;
GRANT SELECT, INSERT, UPDATE ON agent.conversations, agent.runs TO leapview_control_runtime;
GRANT SELECT, INSERT ON agent.messages, agent.events TO leapview_control_runtime;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP SCHEMA agent CASCADE;
RESET ROLE;
`)
	previous := fstest.MapFS{"001_agent_fixture.sql": {Data: fixture}}
	for revision := 2; revision <= 8; revision++ {
		name := fmt.Sprintf("%03d_noop.sql", revision)
		previous[name] = &fstest.MapFile{Data: []byte("-- +goose Up\n-- +goose Down\n")}
	}
	migration, err := fs.ReadFile(MigrationFS(), "009_agent_conversation_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	upgrade := make(fstest.MapFS, len(previous)+1)
	for name, file := range previous {
		upgrade[name] = file
	}
	upgrade["009_agent_conversation_delete.sql"] = &fstest.MapFile{Data: migration}

	db, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	provider, err := newProvider(db, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply version eight fixture: %v", err)
	}
	current, _, err := provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 8 {
		t.Fatalf("fixture version = %d, want 8", current)
	}
	provider, err = newProvider(db, upgrade)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply agent delete migration: %v", err)
	}
	current, _, err = provider.GetVersions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if current != 9 {
		t.Fatalf("upgraded version = %d, want 9", current)
	}

	if _, err := admin.Exec(t.Context(), `
INSERT INTO agent.conversations(id,principal_id,title,status) VALUES ('purge','owner','Purge','active');
INSERT INTO agent.runs(id,conversation_id,status) VALUES ('purge-run','purge','completed');
INSERT INTO agent.messages(id,conversation_id,run_id,sequence,content_text) VALUES ('purge-message','purge','purge-run',1,'secret');
INSERT INTO agent.events(run_id,aggregate_version) VALUES ('purge-run',1);`); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var deletedID string
	if err := runtimeDB.QueryRow(t.Context(), `SELECT id FROM agent.delete_agent_conversation($1,$2)`, "purge", "owner").Scan(&deletedID); err != nil {
		t.Fatalf("runtime delete through migration function: %v", err)
	}
	if deletedID != "purge" {
		t.Fatalf("deleted id = %q", deletedID)
	}
	var remaining int
	if err := admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent.conversations) + (SELECT count(*) FROM agent.runs) + (SELECT count(*) FROM agent.messages) + (SELECT count(*) FROM agent.events)`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("cascaded agent rows remaining = %d, want 0", remaining)
	}
}
