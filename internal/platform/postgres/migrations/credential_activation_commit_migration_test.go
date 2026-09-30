package migrations

import (
	"database/sql"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestCredentialActivationCommitMigrationAndLifecycle(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_activation_commit_051_goose")
	h.GrantDatabase(t, database.Name, owner, "CONNECT", "CREATE")
	h.GrantDatabase(t, database.Name, runtime, "CONNECT")
	h.GrantDatabase(t, database.Name, backup, "CONNECT")
	db, err := sql.Open("pgx", database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	priorMigrations := credentialMigrationSource(t,
		"045_credential_draft_storage.sql", "047_credential_validation_receipts.sql",
		"048_credential_activation_preparation.sql", "049_credential_activation_abort.sql",
		"050_credential_activation_switching.sql",
	)
	priorProvider, err := newProviderWithoutLock(db, priorMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := priorProvider.Up(t.Context()); err != nil {
		t.Fatalf("apply credential migrations through 050: %v", err)
	}

	prepared := insertCredentialPreparationFixture(t, db, "deployment-commit-prepared", false)
	switching := insertCredentialPreparationFixture(t, db, "deployment-commit-switching", false)
	if _, err := db.Exec(`UPDATE credential.activation_preparation
		SET switching_at = GREATEST(clock_timestamp(), created_at) WHERE operation_id = $1`, switching.operationID); err != nil {
		t.Fatalf("put switching fixture in switching state: %v", err)
	}
	aborted := insertCredentialPreparationFixture(t, db, "deployment-commit-aborted", true)

	allMigrations := credentialMigrationSource(t,
		"045_credential_draft_storage.sql", "047_credential_validation_receipts.sql",
		"048_credential_activation_preparation.sql", "049_credential_activation_abort.sql",
		"050_credential_activation_switching.sql", "051_credential_activation_commit.sql",
	)
	provider, err := newProviderWithoutLock(db, allMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade credential lifecycle from 050 to 051: %v", err)
	}

	var preparedSwitching, preparedCommit, preparedAbort sql.NullTime
	if err := db.QueryRow(`SELECT switching_at, committed_at, aborted_at
		FROM credential.activation_preparation WHERE operation_id = $1`, prepared.operationID).Scan(
		&preparedSwitching, &preparedCommit, &preparedAbort,
	); err != nil {
		t.Fatal(err)
	}
	if preparedSwitching.Valid || preparedCommit.Valid || preparedAbort.Valid {
		t.Fatalf("prepared record changed during migration: switching=%v commit=%v abort=%v", preparedSwitching, preparedCommit, preparedAbort)
	}
	var switchingTime, switchingCommit sql.NullTime
	if err := db.QueryRow(`SELECT switching_at, committed_at FROM credential.activation_preparation WHERE operation_id = $1`, switching.operationID).Scan(
		&switchingTime, &switchingCommit,
	); err != nil {
		t.Fatal(err)
	}
	if !switchingTime.Valid || switchingCommit.Valid {
		t.Fatalf("switching record changed during migration: switching=%v commit=%v", switchingTime, switchingCommit)
	}
	var abortSwitching, abortCommit, abortTime sql.NullTime
	var abortBy sql.NullString
	if err := db.QueryRow(`SELECT switching_at, committed_at, aborted_at, aborted_by
		FROM credential.activation_preparation WHERE operation_id = $1`, aborted.operationID).Scan(
		&abortSwitching, &abortCommit, &abortTime, &abortBy,
	); err != nil {
		t.Fatal(err)
	}
	if abortSwitching.Valid || abortCommit.Valid || !abortTime.Valid || !abortBy.Valid || abortBy.String != "actor-a" {
		t.Fatalf("aborted record changed during migration: switching=%v commit=%v abort=%v actor=%v", abortSwitching, abortCommit, abortTime, abortBy)
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

	var commitUpdate, switchUpdate, abortTimeUpdate, abortActorUpdate, intentUpdate, broadUpdate bool
	if err := db.QueryRow(`SELECT
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'committed_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'switching_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_by', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'candidate_id', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'UPDATE')`).Scan(
		&commitUpdate, &switchUpdate, &abortTimeUpdate, &abortActorUpdate, &intentUpdate, &broadUpdate,
	); err != nil {
		t.Fatal(err)
	}
	if !commitUpdate || !switchUpdate || !abortTimeUpdate || !abortActorUpdate || intentUpdate || broadUpdate {
		t.Fatalf("runtime update grants commit/switch/abort/intent/table=%v/%v/%v/%v/%v/%v",
			commitUpdate, switchUpdate, abortTimeUpdate, abortActorUpdate, intentUpdate, broadUpdate)
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
		SET committed_at = clock_timestamp() WHERE operation_id = $1`, prepared.operationID); err == nil {
		t.Fatal("runtime committed a preparation that had not entered switching")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET candidate_id = $2 WHERE operation_id = $1`, switching.operationID, "00000000-0000-0000-0000-000000000001"); err == nil {
		t.Fatal("runtime changed immutable activation intent")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET committed_at = 'infinity'::timestamptz WHERE operation_id = $1`, switching.operationID); err == nil {
		t.Fatal("runtime supplied a non-finite commit timestamp")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET committed_at = clock_timestamp() + interval '1 day' WHERE operation_id = $1`, switching.operationID); err == nil {
		t.Fatal("runtime supplied a future commit timestamp")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET committed_at = clock_timestamp() WHERE operation_id = $1`, switching.operationID); err != nil {
		t.Fatalf("runtime could not commit a fresh switching preparation: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET aborted_at = clock_timestamp(), aborted_by = 'actor-a' WHERE operation_id = $1`, switching.operationID); err == nil {
		t.Fatal("runtime aborted a committed preparation")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET committed_at = committed_at WHERE operation_id = $1`, switching.operationID); err == nil {
		t.Fatal("runtime repeated a commit transition")
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE credential.activation_preparation
		SET committed_at = clock_timestamp() WHERE operation_id = $1`, aborted.operationID); err == nil {
		t.Fatal("runtime committed an aborted preparation")
	}
	if _, err := conn.ExecContext(t.Context(), `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	var committedAt sql.NullTime
	if err := db.QueryRow(`SELECT committed_at FROM credential.activation_preparation WHERE operation_id = $1`, switching.operationID).Scan(&committedAt); err != nil {
		t.Fatal(err)
	}
	if !committedAt.Valid || committedAt.Time.Before(switchingTime.Time) {
		t.Fatalf("commit marker = %v, want DB time at or after switching marker %v", committedAt, switchingTime)
	}
}
