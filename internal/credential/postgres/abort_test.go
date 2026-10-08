package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func allowActivationPreparationAbort(context.Context, Tx, credential.PreparedActivation, string) error {
	return nil
}

func TestAbortActivationPreparationReleasesDeploymentAndRetainsReceipt(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation)
	if err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("prepare activation: %v", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	actorID := uuid.NewString()
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var callerTxID int64
	if err := outer.QueryRow(t.Context(), `SELECT txid_current()`).Scan(&callerTxID); err != nil {
		t.Fatal(err)
	}
	called := false
	aborted, err := repository.AbortActivationPreparationTx(t.Context(), outer,
		input.Receipt.Binding.DeploymentID, input.OperationID, actorID,
		func(ctx context.Context, tx Tx, current credential.PreparedActivation, receivedActor string) error {
			called = true
			if !activationPreparationsEqual(current.Preparation, input) {
				return errors.New("authorization did not receive the exact stored preparation")
			}
			if !current.CreatedAt.Equal(prepared.CreatedAt) || !current.AbortedAt.IsZero() || current.AbortedBy != "" || receivedActor != actorID {
				return errors.New("authorization did not receive the exact active preparation and actor")
			}
			var callbackTxID int64
			if err := tx.QueryRow(ctx, `SELECT txid_current()`).Scan(&callbackTxID); err != nil {
				return err
			}
			if callbackTxID != callerTxID {
				return errors.New("authorization did not receive the caller transaction")
			}
			return nil
		})
	if err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("abort activation: %v", err)
	}
	if !called || !aborted.AbortedAt.After(time.Time{}) || aborted.AbortedAt.Before(prepared.CreatedAt) || aborted.AbortedBy != actorID {
		t.Fatalf("abort result does not contain its durable state: called=%t record=%#v", called, aborted)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID)
	if err != nil {
		t.Fatalf("recover aborted preparation: %v", err)
	}
	if !got.AbortedAt.Equal(aborted.AbortedAt) || got.AbortedBy != actorID {
		t.Fatalf("recovered abort state = %s by %q, want %s by %q", got.AbortedAt, got.AbortedBy, aborted.AbortedAt, actorID)
	}
	var abortAudits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event
		WHERE aggregate_key = $1 AND action = 'credential.activation.aborted'`, "credential-activation:"+input.OperationID).Scan(&abortAudits); err != nil {
		t.Fatal(err)
	}
	if abortAudits != 1 {
		t.Fatalf("abort audit count = %d, want one", abortAudits)
	}

	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID, allowActivationPreparationAbort); !errors.Is(err, credential.ErrConflict) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("duplicate abort = %v, want conflict", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("duplicate abort poisoned outer transaction: %v", err)
	}

	// Abortion releases only the deployment reservation. The original receipt
	// remains permanently consumed, while a distinct fresh receipt can prepare.
	reused := input
	reused.OperationID, reused.CandidateID, reused.GenerationID, reused.PublicationID = uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, reused, allowActivationPreparation); !errors.Is(err, credential.ErrConflict) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("original receipt reuse after abort = %v, want conflict", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoPreparationOperation(t, db, reused.OperationID)

	newInput := postgresActivationPreparation(t, repository, stored)
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, newInput, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("prepare with new receipt after abort: %v", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAbortActivationPreparationAllowsExpiredReceiptWithCurrentAuthorization(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, receipt)
	input := activationPreparationForReceipt(receipt)
	input.PredecessorGenerationID = ""
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.activation_preparation (
		operation_id, deployment_id, receipt_id, expected_target_revision, predecessor_generation_id,
		candidate_id, generation_id, publication_id, created_at
	) VALUES ($1,$2,$3,$4,NULL,$5,$6,$7,clock_timestamp())`, input.OperationID,
		input.Receipt.Binding.DeploymentID, input.Receipt.ReceiptID, input.ExpectedTargetRevision,
		input.CandidateID, input.GenerationID, input.PublicationID); err != nil {
		t.Fatal(err)
	}

	actorID := "current-operator"
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	aborted, err := repository.AbortActivationPreparationTx(t.Context(), outer,
		input.Receipt.Binding.DeploymentID, input.OperationID, actorID,
		func(_ context.Context, _ Tx, current credential.PreparedActivation, receivedActor string) error {
			called = true
			if !activationPreparationsEqual(current.Preparation, input) {
				return errors.New("authorization did not receive the exact stored expired preparation")
			}
			if receivedActor != actorID || !current.CreatedAt.After(current.Preparation.Receipt.ExpiresAt) {
				return errors.New("authorization did not receive expired stored intent")
			}
			return nil
		})
	if err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("abort expired preparation with current authorization: %v", err)
	}
	if !called || aborted.AbortedBy != actorID {
		t.Fatalf("expired preparation was not durably aborted: %#v", aborted)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAbortActivationPreparationRequiresAuthorizationAndAuditAtomically(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.NewString()
	outer, err = runtimeDB.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	rrCallbackCalled := false
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID,
		func(context.Context, Tx, credential.PreparedActivation, string) error {
			rrCallbackCalled = true
			return nil
		}); !errors.Is(err, credential.ErrUnavailable) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("repeatable-read abort = %v, want unavailable", err)
	}
	if rrCallbackCalled {
		_ = outer.Rollback(context.Background())
		t.Fatal("repeatable-read abort reached the authorization callback")
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("repeatable-read abort poisoned outer transaction: %v", err)
	}
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID, nil); !errors.Is(err, credential.ErrUnavailable) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("missing authorization callback = %v, want unavailable", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("abort authority rejected")
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID,
		func(_ context.Context, _ Tx, current credential.PreparedActivation, receivedActor string) error {
			if !activationPreparationsEqual(current.Preparation, input) {
				return errors.New("authorization did not receive the exact stored preparation")
			}
			if receivedActor != actorID {
				return errors.New("wrong actor received")
			}
			return denied
		}); !errors.Is(err, denied) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("rejected authorization = %v, want denial", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("authorization failure poisoned outer transaction: %v", err)
	}
	assertPreparationNotAborted(t, db, input.OperationID)

	failing, err := New(runtimeDB, credentialFailingAudit{err: errors.New("audit failed")})
	if err != nil {
		t.Fatal(err)
	}
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID, allowActivationPreparationAbort); err == nil || err.Error() != "audit failed" {
		_ = outer.Rollback(context.Background())
		t.Fatalf("audit failure = %v, want original audit error", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("audit failure poisoned outer transaction: %v", err)
	}
	assertPreparationNotAborted(t, db, input.OperationID)

	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, actorID, allowActivationPreparationAbort); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("retry abort after audit rollback: %v", err)
	}
	if err := outer.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPreparationNotAborted(t, db, input.OperationID)
}

func TestCheckNoPendingActivationAfterTargetFenceAndRequiresReadCommitted(t *testing.T) {
	_, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckNoPendingActivationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrConflict) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("pending activation check = %v, want conflict", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID, input.OperationID, uuid.NewString(), allowActivationPreparationAbort); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	outer, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckNoPendingActivationTx(t.Context(), outer, input.Receipt.Binding.DeploymentID); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("no pending activation = %v", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	second := postgresActivationPreparation(t, repository, stored)
	outer, err = runtimeDB.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, second, allowActivationPreparation); !errors.Is(err, credential.ErrUnavailable) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("repeatable-read preparation = %v, want unavailable", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckNoPendingActivationUsesStatementSnapshotAfterFenceWait(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	const fenceKey int64 = 774300991
	blocker, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, fenceKey); err != nil {
		t.Fatal(err)
	}
	waiter, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Rollback(context.Background()) }()
	var waiterPID int32
	if err := waiter.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&waiterPID); err != nil {
		t.Fatal(err)
	}
	lockResult := make(chan error, 1)
	go func() {
		_, err := waiter.Exec(context.Background(), `SELECT pg_advisory_xact_lock($1)`, fenceKey)
		lockResult <- err
	}()
	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted
		)`, waiterPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-lockResult:
			t.Fatalf("fence waiter acquired unexpectedly: %v", err)
		case <-waitCtx.Done():
			t.Fatal("fence waiter did not block before pending insert")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.activation_preparation (
		operation_id, deployment_id, receipt_id, expected_target_revision, predecessor_generation_id,
		candidate_id, generation_id, publication_id, created_at
	) VALUES ($1,$2,$3,$4,NULL,$5,$6,$7,clock_timestamp())`, input.OperationID,
		input.Receipt.Binding.DeploymentID, input.Receipt.ReceiptID, input.ExpectedTargetRevision,
		input.CandidateID, input.GenerationID, input.PublicationID); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-lockResult; err != nil {
		t.Fatal(err)
	}
	if err := CheckNoPendingActivationTx(t.Context(), waiter, input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("post-fence pending check = %v, want conflict from the new statement snapshot", err)
	}
	if err := waiter.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareActivationRejectsMismatchedTargetBeforeWrite(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	input.Receipt.Binding.TargetID = "another-target"
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, func(context.Context, Tx, credential.ActivationPreparation) error {
		called = true
		return nil
	}); !errors.Is(err, credential.ErrInvalid) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("mismatched preparation target = %v, want invalid", err)
	}
	if called {
		t.Fatal("mismatched target reached authorization callback")
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoPreparationOperation(t, db, input.OperationID)
}

func TestActivationPreparationAllowsOnlyOneDatabaseAbortTransition(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	abortAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE credential.activation_preparation SET aborted_at = $1, aborted_by = $2 WHERE operation_id = $3`, abortAt, "operator", input.OperationID); err != nil {
		t.Fatalf("runtime abort-column transition rejected: %v", err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE credential.activation_preparation SET aborted_at = clock_timestamp(), aborted_by = 'operator-2' WHERE operation_id = $1`, input.OperationID); err == nil {
		t.Fatal("second database abort transition was allowed")
	}
	if _, err := db.Exec(t.Context(), `UPDATE credential.activation_preparation SET candidate_id = $1 WHERE operation_id = $2`, uuid.NewString(), input.OperationID); err == nil {
		t.Fatal("database changed original preparation intent")
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM credential.activation_preparation WHERE operation_id = $1`, input.OperationID); err == nil {
		t.Fatal("database deleted the aborted preparation receipt")
	}
	assertPreparationAborted(t, db, input.OperationID)
}

func assertPreparationNotAborted(t *testing.T, db *pgxpool.Pool, operationID string) {
	t.Helper()
	var rows int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1 AND aborted_at IS NULL AND aborted_by IS NULL`, operationID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("active preparation row count = %d, want one", rows)
	}
}

func assertPreparationAborted(t *testing.T, db *pgxpool.Pool, operationID string) {
	t.Helper()
	var rows int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1 AND aborted_at IS NOT NULL AND aborted_by IS NOT NULL`, operationID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("aborted preparation row count = %d, want one", rows)
	}
}

func assertNoPreparationOperation(t *testing.T, db *pgxpool.Pool, operationID string) {
	t.Helper()
	var rows, audits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1`, operationID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE event_id = $1::uuid AND action = 'credential.activation.prepared'`, operationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || audits != 0 {
		t.Fatalf("unexpected rows/audits for operation %s: %d/%d", operationID, rows, audits)
	}
}
