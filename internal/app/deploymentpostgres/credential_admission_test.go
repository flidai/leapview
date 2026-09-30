package deploymentpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credentialFenceAudit struct{}

func (credentialFenceAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

func credentialAdmissionFixture(t *testing.T) (*pgxpool.Pool, *deploymentpostgres.Repository, *credentialpostgres.Repository, credential.ActivationPreparation) {
	t.Helper()
	db := generationAdmissionDB(t)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := credentialpostgres.ApplySchema(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivery := deploymentpostgres.New(db)
	if _, err := delivery.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: admissionInstanceID, ProjectID: "project_admission", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	repository, err := credentialpostgres.New(db, credentialFenceAudit{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := encryption.Binding{DeploymentID: admissionInstanceID, TargetID: admissionInstanceID, OwnerID: "customer", ScopeKind: "connection", ProjectID: "project_admission", Environment: "prod", ResourceID: "warehouse", Purpose: "connection-authentication", Provider: "postgres", Destination: admissionDigest('a'), VersionID: uuid.NewString()}
	actor := uuid.NewString()
	// Metadata is sufficient here: this fence never loads or decrypts an envelope.
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, binding.VersionID, binding.DeploymentID, binding.OwnerID, binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination, actor, now); err != nil {
		t.Fatal(err)
	}
	receipt := credential.ValidationReceipt{ReceiptID: uuid.NewString(), Binding: binding, ActorID: actor, BindingID: "warehouse-prod", BindingRevision: 1, ConfigurationDigest: admissionDigest('b'), ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	audit, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	preparation := credential.ActivationPreparation{OperationID: uuid.NewString(), Receipt: receipt, ExpectedTargetRevision: 1, CandidateID: uuid.NewString(), GenerationID: uuid.NewString(), PublicationID: uuid.NewString()}
	return db, delivery, repository, preparation
}

func credentialPreparationTargetFence(delivery *deploymentpostgres.Repository) func(context.Context, pgx.Tx, credential.ActivationPreparation) error {
	return func(ctx context.Context, tx pgx.Tx, input credential.ActivationPreparation) error {
		target, err := delivery.TargetForUpdateTx(ctx, tx, input.Receipt.Binding.TargetID)
		if err != nil {
			return err
		}
		if target.TargetID != input.Receipt.Binding.DeploymentID || target.ProjectID != input.Receipt.Binding.ProjectID || target.Environment != input.Receipt.Binding.Environment || target.TargetRevision != input.ExpectedTargetRevision || target.ActiveGenerationID != input.PredecessorGenerationID {
			return credential.ErrConflict
		}
		return nil
	}
}

func TestCredentialPublicationFenceObservesPreparationAfterTargetLockWait(t *testing.T) {
	t.Run("prepared", func(t *testing.T) { testCredentialPublicationFenceAfterTargetLockWait(t, false) })
	t.Run("switching", func(t *testing.T) { testCredentialPublicationFenceAfterTargetLockWait(t, true) })
}

func testCredentialPublicationFenceAfterTargetLockWait(t *testing.T, switching bool) {
	db, delivery, repository, preparation := credentialAdmissionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	holder, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := repository.PrepareActivationTx(ctx, holder, preparation, credentialPreparationTargetFence(delivery)); err != nil {
		t.Fatal(err)
	}
	if switching {
		_, err := repository.BeginActivationSwitchingTx(ctx, holder, admissionInstanceID,
			preparation.OperationID, preparation.Receipt.ActorID,
			func(ctx context.Context, tx pgx.Tx, current credential.PreparedActivation, _ string) error {
				return credentialPreparationTargetFence(delivery)(ctx, tx, current.Preparation)
			})
		if err != nil {
			t.Fatal(err)
		}
	}
	waiting, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiting.Rollback(context.Background()) }()
	publication := deploymentpostgres.DeliveryPublication{TargetID: admissionInstanceID}
	// The preparation is uncommitted. An earlier snapshot cannot be reused after
	// waiting for the publication target: the subsequent statement must see it.
	if err := admitPublicationWithoutCredentialOperation(ctx, waiting, publication); err != nil {
		t.Fatalf("before preparation commit: %v", err)
	}
	pid := waiting.Conn().PgConn().PID()
	result := make(chan error, 1)
	go func() {
		if _, err := delivery.TargetForUpdateTx(ctx, waiting, admissionInstanceID); err != nil {
			result <- err
			return
		}
		result <- admitPublicationWithoutCredentialOperation(ctx, waiting, publication)
	}()
	for {
		var blocked bool
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("publication did not wait for target: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, deploymentpostgres.ErrConflict) {
			t.Fatalf("publication fence after wait = %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := waiting.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	target, err := delivery.Target(ctx, admissionInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 1 || target.ActiveGenerationID != "" {
		t.Fatalf("fence changed target: %#v", target)
	}
}

func TestCredentialPublicationFenceFailsClosedWithoutSchema(t *testing.T) {
	db := generationAdmissionDB(t)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := admitPublicationWithoutCredentialOperation(t.Context(), tx, deploymentpostgres.DeliveryPublication{TargetID: admissionInstanceID}); err == nil {
		t.Fatal("missing credential schema permitted publication")
	}
}

func TestCredentialPublicationFenceRejectsStaleSnapshotIsolation(t *testing.T) {
	db, delivery, _, _ := credentialAdmissionFixture(t)
	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			tx, err := db.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := delivery.TargetForUpdateTx(t.Context(), tx, admissionInstanceID); err != nil {
				t.Fatal(err)
			}
			if err := admitPublicationWithoutCredentialOperation(t.Context(), tx, deploymentpostgres.DeliveryPublication{TargetID: admissionInstanceID}); err == nil {
				t.Fatal("snapshot isolation permitted publication")
			}
		})
	}
}
