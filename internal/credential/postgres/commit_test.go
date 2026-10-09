package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func activationPublication(input credential.ActivationPreparation) credential.ActivationPublication {
	return credential.ActivationPublication{
		TargetID: input.Receipt.Binding.TargetID, ExpectedTargetRevision: input.ExpectedTargetRevision,
		PredecessorGenerationID: input.PredecessorGenerationID, CandidateID: input.CandidateID,
		GenerationID: input.GenerationID, PublicationID: input.PublicationID, ActorID: input.Receipt.ActorID,
	}
}

func allowActivationCommit(context.Context, Tx, credential.PreparedActivation) error { return nil }

func TestCommitActivationPublicationRetainsExactPendingOperation(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	switched := switchActivation(t, repository, runtimeDB, input)
	tx := beginCredentialTx(t, runtimeDB)
	var transactionID int64
	if err := tx.QueryRow(t.Context(), `SELECT txid_current()`).Scan(&transactionID); err != nil {
		t.Fatal(err)
	}
	called := false
	committed, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input),
		func(ctx context.Context, authTx Tx, prepared credential.PreparedActivation) error {
			called = true
			var callbackID int64
			if err := authTx.QueryRow(ctx, `SELECT txid_current()`).Scan(&callbackID); err != nil {
				return err
			}
			if transactionID != callbackID || !activationPreparationsEqual(prepared.Preparation, input) || !prepared.SwitchingAt.Equal(switched.SwitchingAt) {
				return errors.New("commit authority did not receive the exact intent on the caller transaction")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !called || committed.CommittedAt.IsZero() || committed.CommittedAt.Before(switched.SwitchingAt) {
		t.Fatalf("invalid committed state: %#v", committed)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
	if err != nil || !recovered.CommittedAt.Equal(committed.CommittedAt) {
		t.Fatalf("committed recovery: %#v, %v", recovered, err)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, input.Receipt.Binding.DeploymentID, credential.ErrConflict)
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("repeated commit: %v", err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), tx, input.Receipt.Binding.DeploymentID, input.OperationID, input.Receipt.ActorID, allowActivationPreparationAbort); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("abort after commit: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID, []string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.committed"}, []int64{1, 2, 3})
}

func TestCommitActivationPublicationRequiresExactIntentAndAuthority(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("unswitched commit: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	switchActivation(t, repository, runtimeDB, input)
	for name, mutate := range map[string]func(*credential.ActivationPublication){
		"revision":    func(p *credential.ActivationPublication) { p.ExpectedTargetRevision++ },
		"predecessor": func(p *credential.ActivationPublication) { p.PredecessorGenerationID = uuid.NewString() },
		"candidate":   func(p *credential.ActivationPublication) { p.CandidateID = uuid.NewString() },
		"generation":  func(p *credential.ActivationPublication) { p.GenerationID = uuid.NewString() },
		"publication": func(p *credential.ActivationPublication) { p.PublicationID = uuid.NewString() },
		"actor":       func(p *credential.ActivationPublication) { p.ActorID = uuid.NewString() },
	} {
		t.Run(name, func(t *testing.T) {
			publication := activationPublication(input)
			mutate(&publication)
			tx := beginCredentialTx(t, runtimeDB)
			called := false
			_, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, publication, func(context.Context, Tx, credential.PreparedActivation) error { called = true; return nil })
			if !errors.Is(err, credential.ErrConflict) || called {
				t.Fatalf("mismatched publication: %v, authorized=%t", err, called)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), nil); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("missing authority: %v", err)
	}
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), func(context.Context, Tx, credential.PreparedActivation) error { return credential.ErrForbidden }); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("denied authority: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err := runtimeDB.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("repeatable read: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.committed")
}

func TestCommitActivationPublicationRollsBackWithCallerAndAuditFailure(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	switchActivation(t, repository, runtimeDB, input)
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("commit audit failed")
	failing, err := New(runtimeDB, credentialFailingAudit{err: sentinel})
	if err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	if _, err := failing.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, sentinel) {
		t.Fatalf("audit failure: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
	if err != nil || !recovered.CommittedAt.IsZero() {
		t.Fatalf("failed commit survived: %#v, %v", recovered, err)
	}
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.committed")
}

func TestCommitActivationPublicationRejectsSnapshotAbortedDuringAuthorization(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	switchActivation(t, repository, runtimeDB, input)
	tx := beginCredentialTx(t, runtimeDB)
	_, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input),
		func(ctx context.Context, _ Tx, snapshot credential.PreparedActivation) error {
			if !snapshot.AbortedAt.IsZero() {
				return errors.New("expected pre-abort snapshot")
			}
			other := beginCredentialTx(t, runtimeDB)
			if _, err := repository.AbortActivationPreparationTx(ctx, other, input.Receipt.Binding.DeploymentID,
				input.OperationID, input.Receipt.ActorID, allowActivationPreparationAbort); err != nil {
				return err
			}
			return other.Commit(ctx)
		})
	if !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("stale commit after abort: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID)
	if err != nil || recovered.AbortedAt.IsZero() || !recovered.CommittedAt.IsZero() {
		t.Fatalf("abort winner lost: %#v, %v", recovered, err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID, []string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.aborted"}, []int64{1, 2, 3})
}

func TestAbortActivationRejectsSnapshotCommittedDuringAuthorization(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	switchActivation(t, repository, runtimeDB, input)
	tx := beginCredentialTx(t, runtimeDB)
	_, err := repository.AbortActivationPreparationTx(t.Context(), tx, input.Receipt.Binding.DeploymentID, input.OperationID, input.Receipt.ActorID,
		func(ctx context.Context, _ Tx, snapshot credential.PreparedActivation, _ string) error {
			if !snapshot.CommittedAt.IsZero() {
				return errors.New("expected pre-commit snapshot")
			}
			other := beginCredentialTx(t, runtimeDB)
			if _, err := repository.CommitActivationPublicationTx(ctx, other, input.OperationID, activationPublication(input), allowActivationCommit); err != nil {
				return err
			}
			return other.Commit(ctx)
		})
	if !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("stale abort after commit: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
	if err != nil || !recovered.AbortedAt.IsZero() || recovered.CommittedAt.IsZero() {
		t.Fatalf("commit winner lost: %#v, %v", recovered, err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID, []string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.committed"}, []int64{1, 2, 3})
}

func TestCommitActivationPublicationRejectsExpiredReceiptAndRecoversIntent(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, receipt)
	input := activationPreparationForReceipt(receipt)
	input.PredecessorGenerationID = ""
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.activation_preparation
		(operation_id,deployment_id,receipt_id,expected_target_revision,candidate_id,generation_id,publication_id,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,clock_timestamp())`, input.OperationID, receipt.Binding.DeploymentID, receipt.ReceiptID, input.ExpectedTargetRevision, input.CandidateID, input.GenerationID, input.PublicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE credential.activation_preparation SET switching_at = clock_timestamp() WHERE operation_id = $1`, input.OperationID); err != nil {
		t.Fatal(err)
	}
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := repository.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("expired commit: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), receipt.Binding.DeploymentID)
	if err != nil || !recovered.CommittedAt.IsZero() || recovered.SwitchingAt.IsZero() {
		t.Fatalf("expired recovery: %#v, %v", recovered, err)
	}
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.committed")
}

type credentialCommitAuditFunc func(context.Context, Tx, access.AuditIntent) error

func (f credentialCommitAuditFunc) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	return f(ctx, tx, intent)
}

type credentialCommitReleaseFailureTx struct{ pgx.Tx }

func (tx credentialCommitReleaseFailureTx) Begin(ctx context.Context) (pgx.Tx, error) {
	nested, err := tx.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return credentialCommitNestedReleaseFailureTx{Tx: nested}, nil
}

type credentialCommitNestedReleaseFailureTx struct{ pgx.Tx }

func (tx credentialCommitNestedReleaseFailureTx) Commit(ctx context.Context) error {
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	return tx.Tx.Commit(cancelled)
}

func TestCommitActivationPublicationCleansUpCancellationAndReleaseFailure(t *testing.T) {
	for _, releaseFailure := range []bool{false, true} {
		name := "cancelled request"
		if releaseFailure {
			name = "failed savepoint release"
		}
		t.Run(name, func(t *testing.T) {
			db, runtimeDB, repository, stored := postgresActivationFixture(t)
			input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
			switchActivation(t, repository, runtimeDB, input)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			repo := repository
			tx := beginCredentialTx(t, runtimeDB)
			if releaseFailure {
				tx = credentialCommitReleaseFailureTx{Tx: tx}
			} else {
				var err error
				repo, err = New(runtimeDB, credentialCommitAuditFunc(func(ctx context.Context, tx Tx, intent access.AuditIntent) error {
					if err := repository.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
						return err
					}
					cancel()
					return nil
				}))
				if err != nil {
					t.Fatal(err)
				}
			}
			want := error(context.Canceled)
			if releaseFailure {
				want = credential.ErrUnavailable
			}
			if _, err := repo.CommitActivationPublicationTx(ctx, tx, input.OperationID, activationPublication(input), allowActivationCommit); !errors.Is(err, want) {
				t.Fatalf("commit failure: %v", err)
			}
			err := tx.Commit(t.Context())
			if releaseFailure && !errors.Is(err, pgx.ErrTxClosed) {
				t.Fatalf("unconfirmed cleanup left caller committable: %v", err)
			}
			if !releaseFailure && err != nil {
				t.Fatalf("cancellation poisoned caller: %v", err)
			}
			recovered, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
			if err != nil || !recovered.CommittedAt.IsZero() {
				t.Fatalf("failed commit survived: %#v, %v", recovered, err)
			}
			assertNoAuditAction(t, db, input.OperationID, "credential.activation.committed")
		})
	}
}

func TestCommitActivationPublicationExpiryIsSampledAtConsumptionGate(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 3*time.Second).Truncate(time.Microsecond)
	receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	audit, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	input := activationPreparationForReceipt(receipt)
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := repository.PrepareActivationTx(t.Context(), tx, input, allowActivationPreparation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	switchActivation(t, repository, runtimeDB, input)
	gated, err := New(runtimeDB, credentialCommitAuditFunc(func(ctx context.Context, tx Tx, intent access.AuditIntent) error {
		if err := repository.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
			return err
		}
		timer := time.NewTimer(time.Until(receipt.ExpiresAt.Add(50 * time.Millisecond)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	committed, err := gated.CommitActivationPublicationTx(t.Context(), tx, input.OperationID, activationPublication(input), allowActivationCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !committed.CommittedAt.Before(receipt.ExpiresAt) {
		t.Fatal("consumption gate accepted expired receipt")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), receipt.Binding.DeploymentID)
	if err != nil || !recovered.CommittedAt.Equal(committed.CommittedAt) {
		t.Fatalf("expired committed recovery: %#v, %v", recovered, err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID, []string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.committed"}, []int64{1, 2, 3})
}

func TestCommitActivationPublicationRejectsReceiptExpiryDuringRowLockWait(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 4*time.Second).Truncate(time.Microsecond)
	receipt.ExpiresAt = receipt.ValidatedAt.Add(5 * time.Minute)
	audit, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	input := activationPreparationForReceipt(receipt)
	tx := beginCredentialTx(t, runtimeDB)
	if _, err := repository.PrepareActivationTx(t.Context(), tx, input, allowActivationPreparation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	switchActivation(t, repository, runtimeDB, input)
	holder := beginCredentialTx(t, runtimeDB)
	if _, err := holder.Exec(t.Context(), `SELECT operation_id FROM credential.activation_preparation WHERE operation_id=$1 FOR UPDATE`, input.OperationID); err != nil {
		t.Fatal(err)
	}
	tx = beginCredentialTx(t, runtimeDB)
	var pid int
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := repository.CommitActivationPublicationTx(ctx, tx, input.OperationID, activationPublication(input), allowActivationCommit)
		done <- err
	}()
	for {
		var waiting bool
		if err := db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("commit finished before row wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if wait := time.Until(receipt.ExpiresAt.Add(50 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("receipt expired during row wait: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("expiry poisoned caller transaction: %v", err)
	}
	recovered, err := repository.GetPendingActivation(t.Context(), receipt.Binding.DeploymentID)
	if err != nil || !recovered.CommittedAt.IsZero() {
		t.Fatalf("expired commit survived: %#v, %v", recovered, err)
	}
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.committed")
}
