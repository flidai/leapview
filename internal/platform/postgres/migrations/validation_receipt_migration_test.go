package migrations

import (
	"database/sql"
	"io"
	"testing"
	"testing/fstest"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCredentialValidationReceiptMigrationAndRolePolicy(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_validation_receipt_047_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migrations := make(fstest.MapFS)
	for _, name := range []string{"045_credential_draft_storage.sql", "047_credential_validation_receipts.sql"} {
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
		migrations[name] = &fstest.MapFile{Data: body}
	}
	provider, err := newProviderWithoutLock(db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply credential draft and validation receipt migrations through Goose: %v", err)
	}
	var tableExists bool
	if err := db.QueryRow(`SELECT to_regclass('credential.validation_receipt') IS NOT NULL`).Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists {
		t.Fatal("migration did not create credential.validation_receipt")
	}
	var runtimeInsert, runtimeSelect, runtimeUpdate, runtimeDelete, backupSelect, backupInsert bool
	if err := db.QueryRow(`SELECT
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'INSERT'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'DELETE'),
		has_table_privilege('leapview_control_backup', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.validation_receipt', 'INSERT')`).Scan(
		&runtimeInsert, &runtimeSelect, &runtimeUpdate, &runtimeDelete, &backupSelect, &backupInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !runtimeInsert || runtimeSelect || runtimeUpdate || runtimeDelete || !backupSelect || backupInsert {
		t.Fatalf("receipt grants runtime insert/select/update/delete=%t/%t/%t/%t backup select/insert=%t/%t", runtimeInsert, runtimeSelect, runtimeUpdate, runtimeDelete, backupSelect, backupInsert)
	}

	versionID, receiptID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO credential.draft_version(
		version_id, deployment_id, owner_id, scope_kind, target_id, project_id,
		environment, resource_id, purpose, provider, destination, actor_id, created_at
	) VALUES ($1, 'deployment-a', 'customer-a', 'connection', 'target-a', 'sales', 'prod',
		'warehouse', 'connection-auth', 'postgres', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', $2, now())`, versionID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`WITH observation AS (SELECT clock_timestamp() AS validated_at)
		INSERT INTO credential.validation_receipt(
			receipt_id, deployment_id, version_id, owner_id, scope_kind, target_id,
			project_id, environment, resource_id, purpose, provider, destination,
			actor_id, binding_id, binding_revision, configuration_digest,
			validated_at, expires_at
		)
		SELECT $1, 'deployment-a', $2, 'customer-a', 'connection', 'target-a', 'sales', 'prod',
			'warehouse', 'connection-auth', 'postgres',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			$3, 'binding_a', 1,
			'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			validated_at, validated_at + interval '5 minutes' FROM observation`, receiptID, versionID, actorID); err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	if _, err := db.Exec(`UPDATE credential.validation_receipt SET actor_id = 'changed' WHERE receipt_id = $1`, receiptID); err == nil {
		t.Fatal("migration allowed a validation receipt update")
	}
	if _, err := db.Exec(`DELETE FROM credential.validation_receipt WHERE receipt_id = $1`, receiptID); err == nil {
		t.Fatal("migration allowed a validation receipt delete")
	}
}
