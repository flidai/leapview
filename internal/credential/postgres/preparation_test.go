package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func postgresActivationPreparation(t *testing.T, repository *Repository, stored credential.StoredVersion) credential.ActivationPreparation {
	t.Helper()
	receipt, audit := postgresValidationReceipt(t, stored)
	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	return credential.ActivationPreparation{
		OperationID: uuid.NewString(), Receipt: receipt, ExpectedTargetRevision: 8,
		PredecessorGenerationID: uuid.NewString(), CandidateID: uuid.NewString(),
		GenerationID: uuid.NewString(), PublicationID: uuid.NewString(),
	}
}

func activationPreparationForReceipt(receipt credential.ValidationReceipt) credential.ActivationPreparation {
	return credential.ActivationPreparation{
		OperationID: uuid.NewString(), Receipt: receipt, ExpectedTargetRevision: 8,
		PredecessorGenerationID: uuid.NewString(), CandidateID: uuid.NewString(),
		GenerationID: uuid.NewString(), PublicationID: uuid.NewString(),
	}
}

func seedActivationReceipt(t *testing.T, db *pgxpool.Pool, receipt credential.ValidationReceipt) {
	t.Helper()
	binding := receipt.Binding
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.validation_receipt (
		receipt_id, deployment_id, version_id, owner_id, scope_kind, target_id, project_id, environment,
		resource_id, purpose, provider, destination, actor_id, binding_id, binding_revision,
		configuration_digest, validated_at, expires_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		receipt.ReceiptID, binding.DeploymentID, binding.VersionID, binding.OwnerID,
		binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment,
		binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination,
		receipt.ActorID, receipt.BindingID, receipt.BindingRevision, receipt.ConfigurationDigest,
		receipt.ValidatedAt, receipt.ExpiresAt); err != nil {
		t.Fatal(err)
	}
}

func postgresActivationFixture(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *Repository, credential.StoredVersion) {
	t.Helper()
	db, runtimeDB, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	// Activation source operations fence on the instance delivery target, which
	// is the same identity as the credential deployment key.
	stored.Metadata.Binding.TargetID = stored.Metadata.Binding.DeploymentID
	saveValidationDraft(t, repository, stored, draftAudit)
	return db, runtimeDB, repository, stored
}

func allowActivationPreparation(context.Context, Tx, credential.ActivationPreparation) error {
	return nil
}

func TestPrepareActivationPersistsExactIntentAndRecoveryReadsIt(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	input.PredecessorGenerationID = ""
	outer, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outer.Rollback(context.Background()) }()

	prepared, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation)
	if err != nil {
		t.Fatalf("prepare activation: %v", err)
	}
	if !prepared.CreatedAt.After(input.Receipt.ValidatedAt) {
		t.Fatalf("preparation timestamp %s did not follow receipt validation %s", prepared.CreatedAt, input.Receipt.ValidatedAt)
	}
	var visible int
	if err := outer.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1`, input.OperationID).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 1 {
		t.Fatalf("preparation is not visible within its caller transaction: %d", visible)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID)
	if err != nil {
		t.Fatalf("recover preparation: %v", err)
	}
	assertActivationPreparationEqual(t, got.Preparation, input)
	if !got.CreatedAt.Equal(prepared.CreatedAt) {
		t.Fatalf("recovered timestamp %s differs from inserted timestamp %s", got.CreatedAt, prepared.CreatedAt)
	}
	var auditCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key = $1`, "credential-activation:"+input.OperationID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("preparation audit count = %d, want one", auditCount)
	}
}

func assertActivationPreparationEqual(t *testing.T, got, want credential.ActivationPreparation) {
	t.Helper()
	if !activationPreparationsEqual(got, want) {
		t.Fatalf("activation preparation differs from input:\n got: %#v\nwant: %#v", got, want)
	}
}

func activationPreparationsEqual(got, want credential.ActivationPreparation) bool {
	if got.OperationID != want.OperationID || got.ExpectedTargetRevision != want.ExpectedTargetRevision ||
		got.PredecessorGenerationID != want.PredecessorGenerationID || got.CandidateID != want.CandidateID ||
		got.GenerationID != want.GenerationID || got.PublicationID != want.PublicationID ||
		got.Receipt.ReceiptID != want.Receipt.ReceiptID || got.Receipt.Binding != want.Receipt.Binding ||
		got.Receipt.ActorID != want.Receipt.ActorID || got.Receipt.BindingID != want.Receipt.BindingID ||
		got.Receipt.BindingRevision != want.Receipt.BindingRevision ||
		got.Receipt.ConfigurationDigest != want.Receipt.ConfigurationDigest ||
		!got.Receipt.ValidatedAt.Equal(want.Receipt.ValidatedAt) || !got.Receipt.ExpiresAt.Equal(want.Receipt.ExpiresAt) {
		return false
	}
	return true
}

func TestPrepareActivationRequiresCurrentAuthorityBeforeAnyInsert(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	outer, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var outerTxID int64
	if err := outer.QueryRow(t.Context(), `SELECT txid_current()`).Scan(&outerTxID); err != nil {
		t.Fatal(err)
	}
	called := false
	denied := errors.New("current activation authority rejected")
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, func(ctx context.Context, tx Tx, received credential.ActivationPreparation) error {
		called = true
		assertActivationPreparationEqual(t, received, input)
		var callbackTxID int64
		if err := tx.QueryRow(ctx, `SELECT txid_current()`).Scan(&callbackTxID); err != nil {
			return err
		}
		if callbackTxID != outerTxID {
			return errors.New("authorization ran in a different transaction")
		}
		var rows int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1`, input.OperationID).Scan(&rows); err != nil {
			return err
		}
		if rows != 0 {
			return errors.New("preparation inserted before authorization")
		}
		return denied
	}); !errors.Is(err, denied) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("authorization error = %v, want original denial", err)
	}
	if !called {
		t.Fatal("authorization callback was not called")
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("caller transaction was left unusable after rejected authority: %v", err)
	}
	assertNoActivationPreparation(t, db, input)

	outer, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, nil); !errors.Is(err, credential.ErrUnavailable) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("missing authority callback = %v, want unavailable", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoActivationPreparation(t, db, input)
}

func TestPrepareActivationRejectsEveryReceiptFieldMismatch(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	base := postgresActivationPreparation(t, repository, stored)
	mutations := map[string]struct {
		change func(*credential.ValidationReceipt)
		want   error
	}{
		"receipt id": {change: func(receipt *credential.ValidationReceipt) { receipt.ReceiptID = uuid.NewString() }, want: credential.ErrConflict},
		"deployment": {change: func(receipt *credential.ValidationReceipt) {
			receipt.Binding.DeploymentID = "another-deployment"
			receipt.Binding.TargetID = "another-deployment"
		}, want: credential.ErrConflict},
		"owner":      {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.OwnerID = "another-owner" }, want: credential.ErrConflict},
		"actor":      {change: func(receipt *credential.ValidationReceipt) { receipt.ActorID = uuid.NewString() }, want: credential.ErrConflict},
		"version":    {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.VersionID = uuid.NewString() }, want: credential.ErrConflict},
		"scope":      {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.ProjectID = "another-project" }, want: credential.ErrConflict},
		"resource":   {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.ResourceID = "another-resource" }, want: credential.ErrConflict},
		"purpose":    {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.Purpose = "other-purpose" }, want: credential.ErrConflict},
		"provider":   {change: func(receipt *credential.ValidationReceipt) { receipt.Binding.Provider = "mysql" }, want: credential.ErrInvalid},
		"destination": {change: func(receipt *credential.ValidationReceipt) {
			receipt.Binding.Destination = "sha256:" + strings.Repeat("c", 64)
		}, want: credential.ErrConflict},
		"binding id":       {change: func(receipt *credential.ValidationReceipt) { receipt.BindingID = "binding_another" }, want: credential.ErrConflict},
		"binding revision": {change: func(receipt *credential.ValidationReceipt) { receipt.BindingRevision++ }, want: credential.ErrConflict},
		"configuration": {change: func(receipt *credential.ValidationReceipt) {
			receipt.ConfigurationDigest = "sha256:" + strings.Repeat("c", 64)
		}, want: credential.ErrConflict},
		"timestamps": {change: func(receipt *credential.ValidationReceipt) {
			receipt.ValidatedAt = receipt.ValidatedAt.Add(time.Microsecond)
			receipt.ExpiresAt = receipt.ExpiresAt.Add(time.Microsecond)
		}, want: credential.ErrConflict},
	}
	for name, test := range mutations {
		t.Run(name, func(t *testing.T) {
			input := base
			input.OperationID = uuid.NewString()
			input.Receipt = base.Receipt
			test.change(&input.Receipt)
			outer, err := db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); !errors.Is(err, test.want) {
				_ = outer.Rollback(context.Background())
				t.Fatalf("mismatched receipt = %v, want %v", err, test.want)
			}
			if err := outer.Commit(t.Context()); err != nil {
				t.Fatalf("receipt mismatch poisoned caller transaction: %v", err)
			}
			assertNoActivationPreparation(t, db, input)
		})
	}
}

func TestPrepareActivationRechecksReceiptExpiryAfterTransactionStart(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	firstReceipt, _ := postgresValidationReceipt(t, stored)
	secondReceipt, _ := postgresValidationReceipt(t, stored)
	firstReceipt.ValidatedAt = time.Now().UTC().Add(-5*time.Minute + 2500*time.Millisecond).Truncate(time.Microsecond)
	firstReceipt.ExpiresAt = firstReceipt.ValidatedAt.Add(5 * time.Minute)
	secondReceipt.ValidatedAt = firstReceipt.ValidatedAt
	secondReceipt.ExpiresAt = firstReceipt.ExpiresAt
	seedActivationReceipt(t, db, firstReceipt)
	seedActivationReceipt(t, db, secondReceipt)
	holderInput := activationPreparationForReceipt(firstReceipt)
	challengerInput := activationPreparationForReceipt(secondReceipt)
	holder, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(t.Context(), `INSERT INTO credential.activation_preparation (
		operation_id, deployment_id, receipt_id, expected_target_revision, predecessor_generation_id,
		candidate_id, generation_id, publication_id, created_at
	) VALUES ($1,$2,$3,$4,NULL,$5,$6,$7,clock_timestamp())`, holderInput.OperationID,
		holderInput.Receipt.Binding.DeploymentID, holderInput.Receipt.ReceiptID, holderInput.ExpectedTargetRevision,
		holderInput.CandidateID, holderInput.GenerationID, holderInput.PublicationID); err != nil {
		_ = holder.Rollback(context.Background())
		t.Fatal(err)
	}
	// The holder's unique deployment key forces the INSERT SELECT to wait. Its
	// source receipt is still fresh when the challenger reaches that insert.
	workerCtx, cancelWorker := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWorker()
	result := make(chan error, 1)
	callbackReady := make(chan struct{}, 1)
	go func() {
		outer, err := db.Begin(workerCtx)
		if err != nil {
			result <- err
			return
		}
		_, err = repository.PrepareActivationTx(workerCtx, outer, challengerInput, func(context.Context, Tx, credential.ActivationPreparation) error {
			callbackReady <- struct{}{}
			return nil
		})
		if err != nil {
			_ = outer.Commit(context.Background())
			result <- err
			return
		}
		if err := outer.Commit(context.Background()); err != nil {
			result <- err
			return
		}
		result <- nil
	}()
	select {
	case <-callbackReady:
	case <-time.After(10 * time.Second):
		_ = holder.Rollback(context.Background())
		t.Fatal("challenger did not reach the authorization callback")
	}
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRow(t.Context(), `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query ILIKE '%INSERT INTO credential.activation_preparation%'
		)`).Scan(&waiting); err != nil {
			_ = holder.Rollback(context.Background())
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(waitDeadline) || !time.Now().Before(challengerInput.Receipt.ExpiresAt) {
			_ = holder.Rollback(context.Background())
			t.Fatal("challenger did not wait on the held deployment uniqueness key")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(time.Until(challengerInput.Receipt.ExpiresAt) + 50*time.Millisecond)
	if err := holder.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, credential.ErrConflict) {
			t.Fatalf("preparation after unique-index wait and receipt expiry = %v, want conflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("challenger did not finish after the uniqueness holder rolled back")
	}
	assertNoActivationPreparation(t, db, challengerInput)
}

func TestPrepareActivationSavepointRollsBackAuditFailureAndOuterRollback(t *testing.T) {
	db, runtimeDB, repository, stored := postgresActivationFixture(t)
	input := postgresActivationPreparation(t, repository, stored)
	failingRepository, err := New(runtimeDB, credentialFailingAudit{err: errors.New("audit failed")})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failingRepository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); err == nil || err.Error() != "audit failed" {
		_ = outer.Rollback(context.Background())
		t.Fatalf("audit failure = %v, want original audit error", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatalf("audit failure poisoned caller transaction: %v", err)
	}
	assertNoActivationPreparation(t, db, input)

	outer, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, input, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatalf("prepare before caller rollback: %v", err)
	}
	if err := outer.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoActivationPreparation(t, db, input)
}

func TestPrepareActivationConcurrentReceiptsForOneDeploymentHaveOneWinner(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	inputs := []credential.ActivationPreparation{
		postgresActivationPreparation(t, repository, stored),
		postgresActivationPreparation(t, repository, stored),
	}
	ready := make(chan struct{}, len(inputs))
	release := make(chan struct{})
	type result struct {
		input credential.ActivationPreparation
		err   error
	}
	results := make(chan result, len(inputs))
	for _, input := range inputs {
		input := input
		go func() {
			outer, err := db.Begin(context.Background())
			if err != nil {
				results <- result{input: input, err: err}
				return
			}
			_, err = repository.PrepareActivationTx(context.Background(), outer, input, func(context.Context, Tx, credential.ActivationPreparation) error {
				ready <- struct{}{}
				<-release
				return nil
			})
			if err != nil {
				_ = outer.Commit(context.Background())
				results <- result{input: input, err: err}
				return
			}
			if commitErr := outer.Commit(context.Background()); commitErr != nil {
				results <- result{input: input, err: commitErr}
				return
			}
			results <- result{input: input}
		}()
	}
	<-ready
	<-ready
	close(release)

	winners := 0
	for range inputs {
		result := <-results
		if result.err == nil {
			winners++
		} else if !errors.Is(result.err, credential.ErrConflict) {
			t.Errorf("concurrent preparation returned unexpected error: %v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("preparation winners = %d, want exactly one", winners)
	}
	var preparations, audits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE deployment_id = $1`, stored.Metadata.Binding.DeploymentID).Scan(&preparations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key LIKE 'credential-activation:%'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if preparations != 1 || audits != 1 {
		t.Fatalf("durable concurrent winners preparation/audit = %d/%d, want 1/1", preparations, audits)
	}
}

func TestActivationPreparationReceiptReuseConflicts(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	first := postgresActivationPreparation(t, repository, stored)
	outer, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, first, allowActivationPreparation); err != nil {
		_ = outer.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := first
	second.OperationID = uuid.NewString()
	second.CandidateID = uuid.NewString()
	second.GenerationID = uuid.NewString()
	second.PublicationID = uuid.NewString()
	outer, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrepareActivationTx(t.Context(), outer, second, allowActivationPreparation); !errors.Is(err, credential.ErrConflict) {
		_ = outer.Rollback(context.Background())
		t.Fatalf("reused receipt preparation = %v, want conflict", err)
	}
	if err := outer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNoActivationPreparation(t, db, second)
}

func TestGetActivationPreparationReturnsExpiredReceiptForRecovery(t *testing.T) {
	db, _, repository, stored := postgresActivationFixture(t)
	receipt, _ := postgresValidationReceipt(t, stored)
	input := activationPreparationForReceipt(receipt)
	input.PredecessorGenerationID = ""
	input.Receipt.ValidatedAt = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	input.Receipt.ExpiresAt = input.Receipt.ValidatedAt.Add(5 * time.Minute)
	seedActivationReceipt(t, db, input.Receipt)
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.activation_preparation (
		operation_id, deployment_id, receipt_id, expected_target_revision, predecessor_generation_id,
		candidate_id, generation_id, publication_id, created_at
	) VALUES ($1,$2,$3,$4,NULL,$5,$6,$7,clock_timestamp())`, input.OperationID,
		input.Receipt.Binding.DeploymentID, input.Receipt.ReceiptID, input.ExpectedTargetRevision,
		input.CandidateID, input.GenerationID, input.PublicationID); err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetActivationPreparation(t.Context(), input.Receipt.Binding.DeploymentID, input.OperationID)
	if err != nil {
		t.Fatalf("read expired preparation for recovery: %v", err)
	}
	assertActivationPreparationEqual(t, got.Preparation, input)
	if !got.CreatedAt.After(input.Receipt.ExpiresAt) {
		t.Fatalf("recovery row created_at %s does not follow expired receipt %s", got.CreatedAt, input.Receipt.ExpiresAt)
	}
}

func TestActivationPreparationIsImmutableAndLeastPrivilege(t *testing.T) {
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
	var prepInsert, prepSelect, prepUpdate, prepDelete, abortTimeUpdate, abortActorUpdate, intentUpdate bool
	var receiptSelect, backupSelect, backupInsert bool
	if err := db.QueryRow(t.Context(), `SELECT
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'INSERT'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.activation_preparation', 'DELETE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_at', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'aborted_by', 'UPDATE'),
		has_column_privilege('leapview_control_runtime', 'credential.activation_preparation', 'candidate_id', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.activation_preparation', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.activation_preparation', 'INSERT')`).Scan(
		&prepInsert, &prepSelect, &prepUpdate, &prepDelete, &abortTimeUpdate, &abortActorUpdate, &intentUpdate,
		&receiptSelect, &backupSelect, &backupInsert); err != nil {
		t.Fatal(err)
	}
	if !prepInsert || !prepSelect || prepUpdate || prepDelete || !abortTimeUpdate || !abortActorUpdate || intentUpdate || !receiptSelect || !backupSelect || backupInsert {
		t.Fatalf("unexpected privileges prep insert/select/update/delete=%t/%t/%t/%t abort columns=%t/%t intent update=%t receipt select=%t backup select/insert=%t/%t",
			prepInsert, prepSelect, prepUpdate, prepDelete, abortTimeUpdate, abortActorUpdate, intentUpdate, receiptSelect, backupSelect, backupInsert)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE credential.activation_preparation SET candidate_id = $1 WHERE operation_id = $2`, uuid.NewString(), input.OperationID); err == nil {
		t.Fatal("runtime updated immutable preparation")
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM credential.activation_preparation WHERE operation_id = $1`, input.OperationID); err == nil {
		t.Fatal("preparation delete bypassed immutability trigger")
	}
}

func assertNoActivationPreparation(t *testing.T, db *pgxpool.Pool, input credential.ActivationPreparation) {
	t.Helper()
	var rows, audits int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.activation_preparation WHERE operation_id = $1`, input.OperationID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key = $1`, "credential-activation:"+input.OperationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if rows != 0 || audits != 0 {
		t.Fatalf("failed preparation left rows/audit=%d/%d", rows, audits)
	}
}
