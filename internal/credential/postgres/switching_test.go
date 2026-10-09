package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func allowActivationSwitching(context.Context, Tx, credential.PreparedActivation, string) error {
	return nil
}

func prepareActivationForSwitching(t *testing.T, repository *Repository, runtimeDB *pgxpool.Pool, stored credential.StoredVersion) (credential.ActivationPreparation, credential.PreparedActivation) {
	t.Helper()
	input := postgresActivationPreparation(t, repository, stored)
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := repository.PrepareActivationTx(t.Context(), tx, input, allowActivationPreparation)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("prepare activation: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return input, prepared
}

func switchActivation(t *testing.T, repository *Repository, runtimeDB *pgxpool.Pool, input credential.ActivationPreparation) credential.PreparedActivation {
	t.Helper()
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := repository.BeginActivationSwitchingTx(t.Context(), tx,
		input.Receipt.Binding.DeploymentID, input.OperationID, input.Receipt.ActorID, allowActivationSwitching)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("begin activation switching: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestBeginActivationSwitchingCommitsExactTransitionAndRecoversIt(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, prepared := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var outerTxID int64
	if err := tx.QueryRow(t.Context(), `SELECT txid_current()`).Scan(&outerTxID); err != nil {
		t.Fatal(err)
	}
	called := false
	switched, err := repository.BeginActivationSwitchingTx(t.Context(), tx,
		input.Receipt.Binding.DeploymentID, input.OperationID, input.Receipt.ActorID,
		func(ctx context.Context, authTx Tx, current credential.PreparedActivation, actorID string) error {
			called = true
			if !activationPreparationsEqual(current.Preparation, input) || !current.CreatedAt.Equal(prepared.CreatedAt) ||
				!current.SwitchingAt.IsZero() || !current.AbortedAt.IsZero() || current.AbortedBy != "" {
				return errors.New("switch authorization did not receive the exact prepared record")
			}
			if actorID != input.Receipt.ActorID {
				return errors.New("switch authorization received a different actor")
			}
			var callbackTxID int64
			if err := authTx.QueryRow(ctx, `SELECT txid_current()`).Scan(&callbackTxID); err != nil {
				return err
			}
			if callbackTxID != outerTxID {
				return errors.New("switch authorization did not use the caller transaction")
			}
			return nil
		})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("begin activation switching: %v", err)
	}
	if !called || switched.SwitchingAt.IsZero() || switched.SwitchingAt.Before(prepared.CreatedAt) ||
		!switched.AbortedAt.IsZero() || switched.AbortedBy != "" {
		t.Fatalf("switch result has invalid lifecycle state: callback=%t record=%#v", called, switched)
	}
	var visible int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation
		WHERE operation_id = $1 AND switching_at = $2`, input.OperationID, switched.SwitchingAt).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Fatalf("switch transition is not visible in caller transaction: %d", visible)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	recovered, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
	if err != nil {
		t.Fatalf("recover pending activation: %v", err)
	}
	if !activationPreparationsEqual(recovered.Preparation, input) || !recovered.CreatedAt.Equal(prepared.CreatedAt) ||
		!recovered.SwitchingAt.Equal(switched.SwitchingAt) {
		t.Fatalf("recovered switching record differs: %#v", recovered)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared", "credential.activation.switching"}, []int64{1, 2})

	// Recovery retains the exact switching evidence without renewing it.
	recovered, err = repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID)
	if err != nil || !recovered.SwitchingAt.Equal(switched.SwitchingAt) {
		t.Fatalf("switching recovery = %#v, %v", recovered, err)
	}
	tx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx,
		input.Receipt.Binding.DeploymentID, input.OperationID, input.Receipt.ActorID, allowActivationSwitching); !errors.Is(err, credential.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("repeat switch = %v, want conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("repeat switch conflict poisoned caller transaction: %v", err)
	}
	assertNoPendingActivationTransaction(t, runtimeDB, input.Receipt.Binding.DeploymentID, credential.ErrConflict)
}

func TestBeginActivationSwitchingRequiresReadCommittedActorAndCurrentAuthority(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	actorID := input.Receipt.ActorID

	t.Run("repeatable read is rejected before authorization", func(t *testing.T) {
		tx, err := runtimeDB.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		called := false
		if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
			input.OperationID, actorID, func(context.Context, Tx, credential.PreparedActivation, string) error {
				called = true
				return nil
			}); !errors.Is(err, credential.ErrUnavailable) {
			_ = tx.Rollback(context.Background())
			t.Fatalf("repeatable-read switch = %v, want unavailable", err)
		}
		if called {
			_ = tx.Rollback(context.Background())
			t.Fatal("repeatable-read switch reached authorization")
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatalf("repeatable-read rejection poisoned caller transaction: %v", err)
		}
	})

	t.Run("missing authority is rejected", func(t *testing.T) {
		tx, err := runtimeDB.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
			input.OperationID, actorID, nil); !errors.Is(err, credential.ErrUnavailable) {
			_ = tx.Rollback(context.Background())
			t.Fatalf("missing switch authorizer = %v, want unavailable", err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatalf("missing-authority rejection poisoned caller transaction: %v", err)
		}
	})

	t.Run("different actor is forbidden before authority callback", func(t *testing.T) {
		tx, err := runtimeDB.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		called := false
		if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
			input.OperationID, uuid.NewString(), func(context.Context, Tx, credential.PreparedActivation, string) error {
				called = true
				return nil
			}); !errors.Is(err, credential.ErrForbidden) {
			_ = tx.Rollback(context.Background())
			t.Fatalf("different actor switch = %v, want forbidden", err)
		}
		if called {
			_ = tx.Rollback(context.Background())
			t.Fatal("different actor reached authority callback")
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatalf("actor rejection poisoned caller transaction: %v", err)
		}
	})

	seedSwitchingTestTarget(t, db, input, input.ExpectedTargetRevision, input.PredecessorGenerationID, true)
	for _, test := range []struct {
		name        string
		revision    int64
		predecessor string
		permitted   bool
		want        error
	}{
		{name: "target revision denial", revision: input.ExpectedTargetRevision + 1, predecessor: input.PredecessorGenerationID, permitted: true, want: credential.ErrConflict},
		{name: "predecessor denial", revision: input.ExpectedTargetRevision, predecessor: uuid.NewString(), permitted: true, want: credential.ErrConflict},
		{name: "current authority denial", revision: input.ExpectedTargetRevision, predecessor: input.PredecessorGenerationID, permitted: false, want: credential.ErrForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			setSwitchingTestTarget(t, db, input, test.revision, test.predecessor, test.permitted)
			tx, err := runtimeDB.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
				input.OperationID, actorID, func(ctx context.Context, authTx Tx, current credential.PreparedActivation, receivedActor string) error {
					called = true
					if !activationPreparationsEqual(current.Preparation, input) || receivedActor != actorID {
						return errors.New("authority callback did not receive stored intent and actor")
					}
					return authorizeSwitchingTestTarget(ctx, authTx, current)
				})
			if !errors.Is(err, test.want) {
				_ = tx.Rollback(context.Background())
				t.Fatalf("authority denial = %v, want %v", err, test.want)
			}
			if !called {
				_ = tx.Rollback(context.Background())
				t.Fatal("current authority callback was not called")
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatalf("authority denial poisoned caller transaction: %v", err)
			}
			assertPreparationNotSwitching(t, db, input.OperationID)
			assertActivationLifecycleAudit(t, db, input.OperationID,
				[]string{"credential.activation.prepared"}, []int64{1})
		})
	}
}

func TestBeginActivationSwitchingRollsBackWithCallerAndAuditFailure(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)

	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, input.Receipt.ActorID, allowActivationSwitching); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("switch in rollback test: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertPreparationNotSwitching(t, db, input.OperationID)
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared"}, []int64{1})

	failing, err := New(runtimeDB, credentialFailingAudit{err: errors.New("switch audit failed")})
	if err != nil {
		t.Fatal(err)
	}
	tx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, input.Receipt.ActorID, allowActivationSwitching); err == nil || err.Error() != "switch audit failed" {
		_ = tx.Rollback(context.Background())
		t.Fatalf("switch audit failure = %v, want original audit error", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("audit failure poisoned caller transaction: %v", err)
	}
	assertPreparationNotSwitching(t, db, input.OperationID)
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared"}, []int64{1})
}

func TestBeginActivationSwitchingRejectsExpiredReceiptBeforeMutation(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute - time.Second).Truncate(time.Microsecond)
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

	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := repository.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, input.Receipt.ActorID, func(context.Context, Tx, credential.PreparedActivation, string) error {
			called = true
			return nil
		}); !errors.Is(err, credential.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("switch with expired receipt = %v, want conflict", err)
	}
	if !called {
		_ = tx.Rollback(context.Background())
		t.Fatal("switching did not perform current authorization before its freshness-guarded update")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("expired receipt conflict poisoned caller transaction: %v", err)
	}
	assertPreparationNotSwitching(t, db, input.OperationID)
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.switching")
}

type credentialGatedAudit struct {
	delegate AuditRepository
	entered  chan struct{}
	release  chan struct{}
}

func (a credentialGatedAudit) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	if err := a.delegate.RecordAuditEvent(ctx, tx, intent); err != nil {
		return err
	}
	close(a.entered)
	select {
	case <-a.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBeginActivationSwitchingRechecksExpiryAfterAudit(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	receipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 3*time.Second).Truncate(time.Microsecond)
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

	entered, release := make(chan struct{}), make(chan struct{})
	gated, err := New(runtimeDB, credentialGatedAudit{delegate: repository.audit, entered: entered, release: release})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		prepared credential.PreparedActivation
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		prepared, err := gated.BeginActivationSwitchingTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
			input.OperationID, input.Receipt.ActorID, allowActivationSwitching)
		resultCh <- result{prepared: prepared, err: err}
	}()

	select {
	case <-entered:
	case got := <-resultCh:
		_ = tx.Rollback(context.Background())
		t.Fatalf("switching did not reach audit before completion: %#v, %v", got.prepared, got.err)
	case <-time.After(5 * time.Second):
		_ = tx.Rollback(context.Background())
		t.Fatal("switching did not reach audit")
	}
	if wait := time.Until(receipt.ExpiresAt.Add(50 * time.Millisecond)); wait > 0 {
		select {
		case <-time.After(wait):
		case <-time.After(5 * time.Second):
			close(release)
			_ = tx.Rollback(context.Background())
			t.Fatal("receipt did not expire while audit was held")
		}
	}
	close(release)
	got := <-resultCh
	if !errors.Is(got.err, credential.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("switch after audit-time expiry = %v, want conflict", got.err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("post-audit expiry conflict poisoned caller transaction: %v", err)
	}
	assertPreparationNotSwitching(t, db, input.OperationID)
	assertNoAuditAction(t, db, input.OperationID, "credential.activation.switching")
}

func TestGetPendingActivationSelectsOneLivePreparationOrSwitchAndHidesAborted(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, prepared := prepareActivationForSwitching(t, repository, runtimeDB, stored)
	if got, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID); err != nil ||
		got.Preparation.OperationID != input.OperationID || !got.SwitchingAt.IsZero() {
		t.Fatalf("prepared pending read = %#v, %v", got, err)
	}
	if err := CheckNoPendingActivationTx(t.Context(), beginCredentialTx(t, runtimeDB), input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("prepared activation did not block target transition: %v", err)
	}

	switched := switchActivation(t, repository, runtimeDB, input)
	if err := CheckNoPendingActivationTx(t.Context(), beginCredentialTx(t, runtimeDB), input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("switching activation did not block target transition: %v", err)
	}
	if !switched.SwitchingAt.After(prepared.CreatedAt) {
		t.Fatalf("switch timestamp %s does not follow creation %s", switched.SwitchingAt, prepared.CreatedAt)
	}

	actorID := uuid.NewString()
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, actorID, allowActivationPreparationAbort); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("abort switched preparation: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetPendingActivation(t.Context(), input.Receipt.Binding.DeploymentID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("aborted pending read = %v, want not found", err)
	}
	if got, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID); err != nil ||
		!got.SwitchingAt.Equal(switched.SwitchingAt) || got.AbortedBy != actorID || got.AbortedAt.IsZero() {
		t.Fatalf("aborted preparation lost lifecycle evidence: %#v, %v", got, err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.aborted"}, []int64{1, 2, 3})
	if err := CheckNoPendingActivationTx(t.Context(), beginCredentialTx(t, runtimeDB), input.Receipt.Binding.DeploymentID); err != nil {
		t.Fatalf("aborted activation still blocks target transition: %v", err)
	}

	// An expired but unfinished row remains available to recovery. It has no
	// transition authority, but reading it preserves the exact original intent.
	expiredReceipt, _ := postgresValidationReceipt(t, stored)
	expiredReceipt.ValidatedAt = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	expiredReceipt.ExpiresAt = expiredReceipt.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, expiredReceipt)
	expiredInput := activationPreparationForReceipt(expiredReceipt)
	expiredInput.PredecessorGenerationID = ""
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.activation_preparation (
		operation_id, deployment_id, receipt_id, expected_target_revision, predecessor_generation_id,
		candidate_id, generation_id, publication_id, created_at
	) VALUES ($1,$2,$3,$4,NULL,$5,$6,$7,clock_timestamp())`, expiredInput.OperationID,
		expiredInput.Receipt.Binding.DeploymentID, expiredInput.Receipt.ReceiptID, expiredInput.ExpectedTargetRevision,
		expiredInput.CandidateID, expiredInput.GenerationID, expiredInput.PublicationID); err != nil {
		t.Fatal(err)
	}
	expiredPending, err := repository.GetPendingActivation(t.Context(), expiredInput.Receipt.Binding.DeploymentID)
	if err != nil || expiredPending.Preparation.OperationID != expiredInput.OperationID ||
		!expiredPending.CreatedAt.After(expiredPending.Preparation.Receipt.ExpiresAt) {
		t.Fatalf("expired pending recovery = %#v, %v", expiredPending, err)
	}
}

func TestAbortRejectsStalePreparedSnapshotAfterConcurrentSwitch(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input, _ := prepareActivationForSwitching(t, repository, runtimeDB, stored)

	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = repository.AbortActivationPreparationTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, uuid.NewString(), func(ctx context.Context, _ Tx, snapshot credential.PreparedActivation, _ string) error {
			called = true
			if !snapshot.SwitchingAt.IsZero() {
				return errors.New("abort did not read the prepared-phase snapshot")
			}
			other, err := runtimeDB.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err := repository.BeginActivationSwitchingTx(ctx, other, input.Receipt.Binding.DeploymentID,
				input.OperationID, input.Receipt.ActorID, allowActivationSwitching); err != nil {
				_ = other.Rollback(context.Background())
				return fmt.Errorf("concurrent switch: %w", err)
			}
			return other.Commit(ctx)
		})
	if !errors.Is(err, credential.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("stale abort after concurrent switch = %v, want conflict", err)
	}
	if !called {
		_ = tx.Rollback(context.Background())
		t.Fatal("abort authority callback was not called")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("stale abort conflict poisoned caller transaction: %v", err)
	}
	assertPreparationNotAborted(t, db, input.OperationID)
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared", "credential.activation.switching"}, []int64{1, 2})

	actorID := uuid.NewString()
	tx, err = runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AbortActivationPreparationTx(t.Context(), tx, input.Receipt.Binding.DeploymentID,
		input.OperationID, actorID, allowActivationPreparationAbort); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("fresh abort after switch: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID); err != nil ||
		got.AbortedBy != actorID || got.AbortedAt.IsZero() || got.SwitchingAt.IsZero() {
		t.Fatalf("fresh abort did not retain switch and abort evidence: %#v, %v", got, err)
	}
	assertActivationLifecycleAudit(t, db, input.OperationID,
		[]string{"credential.activation.prepared", "credential.activation.switching", "credential.activation.aborted"}, []int64{1, 2, 3})
}

func beginCredentialTx(t *testing.T, runtimeDB *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func assertNoPendingActivationTransaction(t *testing.T, runtimeDB *pgxpool.Pool, deploymentID string, want error) {
	t.Helper()
	tx := beginCredentialTx(t, runtimeDB)
	if err := CheckNoPendingActivationTx(t.Context(), tx, deploymentID); !errors.Is(err, want) {
		t.Fatalf("pending activation check = %v, want %v", err, want)
	}
}

func seedSwitchingTestTarget(t *testing.T, db *pgxpool.Pool, input credential.ActivationPreparation, revision int64, predecessor string, permitted bool) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `CREATE TABLE credential.switching_test_target (
		deployment_id text PRIMARY KEY,
		target_revision bigint NOT NULL,
		predecessor_generation_id text NOT NULL,
		permitted boolean NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `GRANT SELECT, UPDATE ON credential.switching_test_target TO leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	setSwitchingTestTarget(t, db, input, revision, predecessor, permitted)
}

func setSwitchingTestTarget(t *testing.T, db *pgxpool.Pool, input credential.ActivationPreparation, revision int64, predecessor string, permitted bool) {
	t.Helper()
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.switching_test_target
		(deployment_id, target_revision, predecessor_generation_id, permitted)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (deployment_id) DO UPDATE SET target_revision = EXCLUDED.target_revision,
		predecessor_generation_id = EXCLUDED.predecessor_generation_id, permitted = EXCLUDED.permitted`,
		input.Receipt.Binding.DeploymentID, revision, predecessor, permitted); err != nil {
		t.Fatal(err)
	}
}

func authorizeSwitchingTestTarget(ctx context.Context, tx Tx, prepared credential.PreparedActivation) error {
	var revision int64
	var predecessor string
	var permitted bool
	err := tx.QueryRow(ctx, `SELECT target_revision, predecessor_generation_id, permitted
		FROM credential.switching_test_target WHERE deployment_id = $1 FOR UPDATE`,
		prepared.Preparation.Receipt.Binding.DeploymentID).Scan(&revision, &predecessor, &permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return credential.ErrForbidden
	}
	if revision != prepared.Preparation.ExpectedTargetRevision || predecessor != prepared.Preparation.PredecessorGenerationID {
		return credential.ErrConflict
	}
	return nil
}

func assertPreparationNotSwitching(t *testing.T, db *pgxpool.Pool, operationID string) {
	t.Helper()
	var rows int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation
		WHERE operation_id = $1 AND switching_at IS NULL AND aborted_at IS NULL`, operationID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("prepared-phase activation rows = %d, want one", rows)
	}
}

func assertActivationLifecycleAudit(t *testing.T, db *pgxpool.Pool, operationID string, wantActions []string, wantSequences []int64) {
	t.Helper()
	rows, err := db.Query(t.Context(), `SELECT action, aggregate_sequence FROM audit.audit_event
		WHERE aggregate_key = $1 ORDER BY aggregate_sequence`, "credential-activation:"+operationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	var sequences []int64
	for rows.Next() {
		var action string
		var sequence int64
		if err := rows.Scan(&action, &sequence); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		sequences = append(sequences, sequence)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actions) != len(wantActions) || len(sequences) != len(wantSequences) {
		t.Fatalf("activation audit events = %v at %v, want %v at %v", actions, sequences, wantActions, wantSequences)
	}
	for index := range wantActions {
		if actions[index] != wantActions[index] || sequences[index] != wantSequences[index] {
			t.Fatalf("activation audit event %d = %q at %d, want %q at %d", index, actions[index], sequences[index], wantActions[index], wantSequences[index])
		}
	}
}

func assertNoAuditAction(t *testing.T, db *pgxpool.Pool, operationID, action string) {
	t.Helper()
	var rows int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event
		WHERE aggregate_key = $1 AND action = $2`, "credential-activation:"+operationID, action).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("unexpected %q audit count = %d", action, rows)
	}
}
