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

func TestCredentialActivationSwitchingMigrationAndLifecycle(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_activation_switching_050_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	priorMigrations := credentialMigrationSource(t, "045_credential_draft_storage.sql", "047_credential_validation_receipts.sql", "048_credential_activation_preparation.sql", "049_credential_activation_abort.sql")
	priorProvider, err := newProviderWithoutLock(db, priorMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := priorProvider.Up(t.Context()); err != nil {
		t.Fatalf("apply credential migrations through 049: %v", err)
	}

	prepared := insertCredentialPreparationFixture(t, db, "deployment-switching", false)
	aborted := insertCredentialPreparationFixture(t, db, "deployment-aborted", true)

	allMigrations := credentialMigrationSource(t,
		"045_credential_draft_storage.sql", "047_credential_validation_receipts.sql",
		"048_credential_activation_preparation.sql", "049_credential_activation_abort.sql",
		"050_credential_activation_switching.sql",
	)
	provider, err := newProviderWithoutLock(db, allMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade credential lifecycle from 049 to 050: %v", err)
	}

	var preservedPreparedSwitching, preservedPreparedAbort, preservedAbortSwitching, preservedAbortTime sql.NullTime
	var preservedAbortBy sql.NullString
	if err := db.QueryRow(`SELECT switching_at, aborted_at, aborted_by FROM credential.activation_preparation WHERE operation_id = $1`, prepared.operationID).Scan(&preservedPreparedSwitching, &preservedPreparedAbort, new(sql.NullString)); err != nil {
		t.Fatal(err)
	}
	if preservedPreparedSwitching.Valid || preservedPreparedAbort.Valid {
		t.Fatalf("pre-existing prepared state changed during migration: switching=%v abort=%v", preservedPreparedSwitching, preservedPreparedAbort)
	}
	if err := db.QueryRow(`SELECT switching_at, aborted_at, aborted_by FROM credential.activation_preparation WHERE operation_id = $1`, aborted.operationID).Scan(&preservedAbortSwitching, &preservedAbortTime, &preservedAbortBy); err != nil {
		t.Fatal(err)
	}
	if preservedAbortSwitching.Valid || !preservedAbortTime.Valid || !preservedAbortBy.Valid || preservedAbortBy.String != "actor-a" {
		t.Fatalf("pre-existing aborted state changed during migration: switching=%v abort=%v actor=%v", preservedAbortSwitching, preservedAbortTime, preservedAbortBy)
	}

	var triggerExists, partialIndexExists bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM pg_trigger
		WHERE tgrelid = 'credential.activation_preparation'::regclass
		  AND NOT tgisinternal AND tgenabled = 'O'
		  AND tgfoid = 'credential.guard_activation_preparation_transition()'::regprocedure
	), EXISTS (
		SELECT 1 FROM pg_index AS indexes
		JOIN pg_class AS index_class ON index_class.oid = indexes.indexrelid
		WHERE indexes.indrelid = 'credential.activation_preparation'::regclass
		  AND index_class.relname = 'activation_preparation_one_pending_deployment_idx'
		  AND pg_get_expr(indexes.indpred, indexes.indrelid) = '(aborted_at IS NULL)'
	)`).Scan(&triggerExists, &partialIndexExists); err != nil {
		t.Fatal(err)
	}
	if !triggerExists || !partialIndexExists {
		t.Fatalf("migration lifecycle protection trigger/index missing: trigger=%v pending-index=%v", triggerExists, partialIndexExists)
	}

	var switchUpdate, abortTimeUpdate, abortActorUpdate, intentUpdate, broadUpdate bool
	if err := db.QueryRow(`SELECT
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'switching_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_by', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'candidate_id', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'UPDATE')`).Scan(
		&switchUpdate, &abortTimeUpdate, &abortActorUpdate, &intentUpdate, &broadUpdate,
	); err != nil {
		t.Fatal(err)
	}
	if !switchUpdate || !abortTimeUpdate || !abortActorUpdate || intentUpdate || broadUpdate {
		t.Fatalf("runtime update grants switching/abort/intent/table=%v/%v/%v/%v/%v", switchUpdate, abortTimeUpdate, abortActorUpdate, intentUpdate, broadUpdate)
	}

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `SET ROLE leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET switching_at = clock_timestamp(), aborted_at = clock_timestamp(), aborted_by = 'actor-a'
		WHERE operation_id = $1`, prepared.operationID); err == nil {
		t.Fatal("runtime combined switching and abort in one update")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation SET candidate_id = $2 WHERE operation_id = $1`, prepared.operationID, uuid.NewString()); err == nil {
		t.Fatal("runtime changed immutable activation intent")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation SET switching_at = GREATEST(clock_timestamp(), created_at) WHERE operation_id = $1`, prepared.operationID); err != nil {
		t.Fatalf("runtime could not enter switching: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation SET switching_at = switching_at WHERE operation_id = $1`, prepared.operationID); err == nil {
		t.Fatal("runtime performed a no-op switching update")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation SET switching_at = NULL WHERE operation_id = $1`, prepared.operationID); err == nil {
		t.Fatal("runtime cleared switching state")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET aborted_at = GREATEST(clock_timestamp(), switching_at), aborted_by = 'actor-a'
		WHERE operation_id = $1`, prepared.operationID); err != nil {
		t.Fatalf("runtime could not abort switching preparation: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET aborted_at = GREATEST(clock_timestamp(), created_at), aborted_by = 'actor-a'
		WHERE operation_id = $1`, aborted.operationID); err == nil {
		t.Fatal("runtime repeated an abort transition")
	}
	if _, err := conn.ExecContext(t.Context(), `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
}

type credentialPreparationFixture struct {
	operationID string
}

func insertCredentialPreparationFixture(t *testing.T, db *sql.DB, deploymentID string, abort bool) credentialPreparationFixture {
	t.Helper()
	versionID, receiptID, operationID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	actorID := "actor-a"
	if _, err := db.Exec(`INSERT INTO credential.draft_version(
		version_id, deployment_id, owner_id, scope_kind, target_id, project_id,
		environment, resource_id, purpose, provider, destination, actor_id, created_at
	) VALUES ($1, $2, 'owner-a', 'connection', $2, 'project-a', 'production', 'warehouse',
		'connection-auth', 'postgres', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', $3, clock_timestamp())`,
		versionID, deploymentID, actorID); err != nil {
		t.Fatalf("insert %s credential draft: %v", deploymentID, err)
	}
	if _, err := db.Exec(`WITH observation AS (SELECT clock_timestamp() AS validated_at)
		INSERT INTO credential.validation_receipt(
			receipt_id, deployment_id, version_id, owner_id, scope_kind, target_id,
			project_id, environment, resource_id, purpose, provider, destination,
			actor_id, binding_id, binding_revision, configuration_digest,
			validated_at, expires_at
		)
		SELECT $1, $2, $3, 'owner-a', 'connection', $2, 'project-a', 'production',
			'warehouse', 'connection-auth', 'postgres',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			$4, 'binding-a', 1,
			'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			validated_at, validated_at + interval '5 minutes' FROM observation`,
		receiptID, deploymentID, versionID, actorID); err != nil {
		t.Fatalf("insert %s validation receipt: %v", deploymentID, err)
	}
	if _, err := db.Exec(`INSERT INTO credential.activation_preparation(
		operation_id, deployment_id, receipt_id, expected_target_revision,
		predecessor_generation_id, candidate_id, generation_id, publication_id, created_at
	) VALUES ($1, $2, $3, 1, NULL, $4, $5, $6, clock_timestamp())`,
		operationID, deploymentID, receiptID, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatalf("insert %s preparation: %v", deploymentID, err)
	}
	if abort {
		if _, err := db.Exec(`UPDATE credential.activation_preparation
			SET aborted_at = GREATEST(clock_timestamp(), created_at), aborted_by = $2
			WHERE operation_id = $1`, operationID, actorID); err != nil {
			t.Fatalf("abort %s preparation before switching migration: %v", deploymentID, err)
		}
	}
	return credentialPreparationFixture{operationID: operationID}
}

func credentialMigrationSource(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	source := make(fstest.MapFS, len(names))
	for _, name := range names {
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
	return source
}
