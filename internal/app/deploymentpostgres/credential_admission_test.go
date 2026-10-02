package deploymentpostgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func credentialAdmissionFixture(t *testing.T) (*pgxpool.Pool, *deploymentpostgres.Repository, string) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := encryption.Binding{DeploymentID: admissionInstanceID, TargetID: admissionInstanceID, OwnerID: "customer", ScopeKind: "connection", ProjectID: "project_admission", Environment: "prod", ResourceID: "warehouse", Purpose: "connection-authentication", Provider: "postgres", Destination: admissionDigest('a'), VersionID: uuid.NewString()}
	actor := uuid.NewString()
	// Metadata is sufficient here: this fence never loads or decrypts an envelope.
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, binding.VersionID, binding.DeploymentID, binding.OwnerID, binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination, actor, now); err != nil {
		t.Fatal(err)
	}
	receipt := credential.ValidationReceipt{ReceiptID: uuid.NewString(), Binding: binding, ActorID: actor, BindingID: "warehouse-prod", BindingRevision: 1, ConfigurationDigest: admissionDigest('b'), ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	// These are persisted-state fixtures for the admission reader, not an
	// activation authority. Keep the real schema and transition guards enabled.
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.validation_receipt
(receipt_id,deployment_id,version_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,binding_id,binding_revision,configuration_digest,validated_at,expires_at)
SELECT $1,deployment_id,version_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,$2,$3,$4,$5,$6
FROM credential.draft_version WHERE version_id=$7`, receipt.ReceiptID, receipt.BindingID, receipt.BindingRevision, receipt.ConfigurationDigest, receipt.ValidatedAt, receipt.ExpiresAt, binding.VersionID); err != nil {
		t.Fatal(err)
	}
	return db, delivery, receipt.ReceiptID
}

func TestCredentialPublicationFenceObservesPreparationAfterTargetLockWait(t *testing.T) {
	for _, state := range []string{"prepared", "switching", "committed", "aborted"} {
		t.Run(state, func(t *testing.T) { testCredentialPublicationFenceAfterTargetLockWait(t, state) })
	}
}

func testCredentialPublicationFenceAfterTargetLockWait(t *testing.T, state string) {
	db, delivery, receiptID := credentialAdmissionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	holder, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := delivery.TargetForUpdateTx(ctx, holder, admissionInstanceID); err != nil {
		t.Fatal(err)
	}
	operationID := uuid.NewString()
	if _, err := holder.Exec(ctx, `INSERT INTO credential.activation_preparation
(operation_id,deployment_id,receipt_id,expected_target_revision,candidate_id,generation_id,publication_id,created_at)
VALUES($1,$2,$3,1,$4,$5,$6,clock_timestamp())`, operationID, admissionInstanceID, receiptID, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if state == "switching" || state == "committed" {
		if _, err := holder.Exec(ctx, `UPDATE credential.activation_preparation SET switching_at=clock_timestamp() WHERE operation_id=$1`, operationID); err != nil {
			t.Fatal(err)
		}
	}
	if state == "committed" {
		if _, err := holder.Exec(ctx, `UPDATE credential.activation_preparation SET committed_at=clock_timestamp() WHERE operation_id=$1`, operationID); err != nil {
			t.Fatal(err)
		}
	}
	if state == "aborted" {
		if _, err := holder.Exec(ctx, `UPDATE credential.activation_preparation SET aborted_at=clock_timestamp(), aborted_by=$2 WHERE operation_id=$1`, operationID, uuid.NewString()); err != nil {
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
		if state == "aborted" {
			if err != nil {
				t.Fatalf("aborted preparation denied publication after wait: %v", err)
			}
		} else if !errors.Is(err, deploymentpostgres.ErrConflict) {
			t.Fatalf("publication fence after wait = %v, want conflict", err)
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
	db, delivery, _ := credentialAdmissionFixture(t)
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
