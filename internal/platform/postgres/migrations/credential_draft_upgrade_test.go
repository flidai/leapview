package migrations

import (
	"database/sql"
	"io"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCredentialDraftMigrationRunsThroughGooseParser(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_045_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := MigrationFS().Open("045_credential_draft_storage.sql")
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
		"045_credential_draft_storage.sql": &fstest.MapFile{Data: body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply credential migration through Goose: %v", err)
	}
	for _, table := range []string{
		"credential.encryption_budget", "credential.draft_version", "credential.envelope",
	} {
		var exists bool
		if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("migration did not create %s", table)
		}
	}
	var commitmentColumn bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema='credential' AND table_name='encryption_budget' AND column_name='key_commitment'
	)`).Scan(&commitmentColumn); err != nil {
		t.Fatal(err)
	}
	if !commitmentColumn {
		t.Fatal("migration did not persist key material commitments")
	}
	if _, err := db.Exec(`INSERT INTO credential.encryption_budget(deployment_id,key_id,key_commitment,uses) VALUES ('deployment-a','key-a',decode(repeat('01',32),'hex'),1)`); err != nil {
		t.Fatalf("insert committed key budget: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO credential.encryption_budget(deployment_id,key_id,key_commitment,uses) VALUES ('deployment-a','key-b',decode(repeat('01',32),'hex'),1)`); err == nil {
		t.Fatal("migration allowed one key material commitment under multiple IDs")
	}
	if _, err := db.Exec(`UPDATE credential.encryption_budget SET key_commitment=decode(repeat('02',32),'hex') WHERE deployment_id='deployment-a' AND key_id='key-a'`); err == nil {
		t.Fatal("migration allowed a key ID commitment to be rebound")
	}
}
