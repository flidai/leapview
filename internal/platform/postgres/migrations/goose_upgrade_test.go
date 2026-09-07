package migrations

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
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
