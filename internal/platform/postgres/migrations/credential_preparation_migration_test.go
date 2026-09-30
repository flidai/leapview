package migrations

import (
	"database/sql"
	"io"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestCredentialActivationPreparationAbortMigrationAndRolePolicy(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_preparation_049_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := make(fstest.MapFS)
	for _, name := range []string{"045_credential_draft_storage.sql", "047_credential_validation_receipts.sql", "048_credential_activation_preparation.sql", "049_credential_activation_abort.sql"} {
		file, err := MigrationFS().Open(name)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(file)
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
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
	var receiptRead, receiptWrite, receiptDelete bool
	var prepareRead, prepareInsert, prepareUpdate, prepareDelete, abortTimeUpdate, abortActorUpdate, intentUpdate, backupRead, backupInsert bool
	if err := db.QueryRow(`SELECT
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'DELETE'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'INSERT'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'DELETE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_by', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'candidate_id', 'UPDATE'),
		has_table_privilege('leapview_control_backup', 'credential.activation_preparation', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.activation_preparation', 'INSERT')`).Scan(
		&receiptRead, &receiptWrite, &receiptDelete, &prepareRead, &prepareInsert, &prepareUpdate, &prepareDelete,
		&abortTimeUpdate, &abortActorUpdate, &intentUpdate, &backupRead, &backupInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !receiptRead || receiptWrite || receiptDelete || !prepareRead || !prepareInsert || !abortTimeUpdate || !abortActorUpdate || intentUpdate || prepareDelete || !backupRead || backupInsert {
		t.Fatalf("invalid preparation role grants: receipt=%v/%v/%v preparation=%v/%v/%v/%v abort-columns=%v/%v intent-update=%v backup=%v/%v",
			receiptRead, receiptWrite, receiptDelete, prepareRead, prepareInsert, prepareUpdate, prepareDelete, abortTimeUpdate, abortActorUpdate, intentUpdate, backupRead, backupInsert)
	}
	var protected, partialUnique bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM pg_trigger
		WHERE tgrelid = 'credential.activation_preparation'::regclass
		AND NOT tgisinternal AND tgenabled = 'O'
		AND tgfoid = 'credential.guard_activation_preparation_abort()'::regprocedure
	), EXISTS (
		SELECT 1 FROM pg_index AS indexes
		JOIN pg_class AS index_class ON index_class.oid = indexes.indexrelid
		WHERE indexes.indrelid = 'credential.activation_preparation'::regclass
		  AND index_class.relname = 'activation_preparation_one_pending_deployment_idx'
		  AND pg_get_expr(indexes.indpred, indexes.indrelid) = '(aborted_at IS NULL)'
	)`).Scan(&protected, &partialUnique); err != nil {
		t.Fatal(err)
	}
	if !protected {
		t.Fatal("preparation abort guard missing after migration")
	}
	if !partialUnique {
		t.Fatal("one-pending-preparation index missing after migration")
	}
}
