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

func TestFirstSourceAdmissionMigrationRestrictsRolesAndPreservesCanonicalSchema(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "first_source_admission_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := make(fstest.MapFS)
	for _, name := range []string{"048_credential_draft_storage.sql", "064_credential_first_source_admission.sql"} {
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
	if _, err := provider.Down(t.Context()); err == nil || !strings.Contains(err.Error(), "first-source admission is forward-only") {
		t.Fatalf("first-source admission downgrade was not refused: %v", err)
	}
	if err := db.QueryRow(historyQuery).Scan(&afterHistory); err != nil {
		t.Fatal(err)
	}
	if beforeHistory != afterHistory {
		t.Fatal("denied first-source admission downgrade changed immutable migration history")
	}
	var read, insert, update, remove, backupRead, backupInsert, publicRead, guarded bool
	if err := db.QueryRow(`SELECT
		has_table_privilege('leapview_control_runtime', 'credential.first_source_admission', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.first_source_admission', 'INSERT'),
		has_table_privilege('leapview_control_runtime', 'credential.first_source_admission', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.first_source_admission', 'DELETE'),
		has_table_privilege('leapview_control_backup', 'credential.first_source_admission', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.first_source_admission', 'INSERT'),
		EXISTS (SELECT 1 FROM pg_class, LATERAL aclexplode(relacl) AS acl WHERE oid='credential.first_source_admission'::regclass AND acl.grantee=0),
		EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='credential.first_source_admission'::regclass AND NOT tgisinternal AND tgenabled='O' AND tgfoid='credential.guard_first_source_admission()'::regprocedure)
	`).Scan(&read, &insert, &update, &remove, &backupRead, &backupInsert, &publicRead, &guarded); err != nil {
		t.Fatal(err)
	}
	if !read || !insert || update || remove || !backupRead || backupInsert || publicRead || !guarded {
		t.Fatalf("admission grants runtime=%v/%v/%v/%v backup=%v/%v public=%v guarded=%v", read, insert, update, remove, backupRead, backupInsert, publicRead, guarded)
	}
	body := string(source["064_credential_first_source_admission.sql"].Data)
	start := strings.Index(body, "-- Immutable installation-operator admission")
	end := strings.Index(body, "-- +goose StatementEnd")
	if start < 0 || end < start || !strings.Contains(credentialpostgres.SchemaSQL(), body[start:end]) {
		t.Fatal("forward migration differs from canonical admission schema")
	}
}
