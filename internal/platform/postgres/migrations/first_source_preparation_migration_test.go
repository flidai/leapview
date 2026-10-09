package migrations

import (
	"database/sql"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestFirstSourcePreparationMigrationUsesExclusiveReceiptSchemaAndRestrictsRoles(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "first_source_preparation_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := make(fstest.MapFS)
	for _, name := range []string{"048_credential_draft_storage.sql", "050_credential_validation_receipts.sql", "051_credential_activation_preparation.sql", "061_credential_activation_requests.sql", "063_credential_first_source_admission.sql", "064_credential_first_source_preparation.sql"} {
		body, err := fs.ReadFile(MigrationFS(), name)
		if err != nil {
			t.Fatal(err)
		}
		source[name] = &fstest.MapFile{Data: body}
	}
	provider, err := newProviderWithoutLock(db, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var beforeHistory, afterHistory string
	const historyQuery = `SELECT json_agg(v ORDER BY id)::text FROM goose_db_version v`
	if err := db.QueryRow(historyQuery).Scan(&beforeHistory); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "first-source preparation is forward-only") {
		t.Fatalf("first-source preparation downgrade was not refused: %v", err)
	}
	if err := db.QueryRow(historyQuery).Scan(&afterHistory); err != nil {
		t.Fatal(err)
	}
	if beforeHistory != afterHistory {
		t.Fatal("denied first-source preparation downgrade changed immutable migration history")
	}
	for _, table := range []string{"credential.first_source_preparation", "credential.first_source_plan_link"} {
		var read, insert, update, remove, backupRead, backupInsert, guarded bool
		if err := db.QueryRow(`SELECT has_table_privilege('leapview_control_runtime',$1,'SELECT'),has_table_privilege('leapview_control_runtime',$1,'INSERT'),has_table_privilege('leapview_control_runtime',$1,'UPDATE'),has_table_privilege('leapview_control_runtime',$1,'DELETE'),has_table_privilege('leapview_control_backup',$1,'SELECT'),has_table_privilege('leapview_control_backup',$1,'INSERT'),EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND NOT tgisinternal AND tgenabled='O')`, table).Scan(&read, &insert, &update, &remove, &backupRead, &backupInsert, &guarded); err != nil {
			t.Fatal(err)
		}
		if !read || !insert || update || remove || !backupRead || backupInsert || !guarded {
			t.Fatalf("unsafe %s grants %v/%v/%v/%v backup=%v/%v guarded=%v", table, read, insert, update, remove, backupRead, backupInsert, guarded)
		}
	}
	body := string(source["064_credential_first_source_preparation.sql"].Data)
	start := strings.Index(body, "-- First-source source/publisher intent")
	end := strings.Index(body, "-- +goose StatementEnd")
	if start < 0 || end < start || !strings.Contains(credentialpostgres.SchemaSQL(), body[start:end]) {
		t.Fatal("migration differs from canonical preparation schema")
	}
}
