package deploymentpostgres

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type nativeRecoveryPreparationFixture struct {
	Pool      *pgxpool.Pool
	Delivery  *deploymentnative.Repository
	DuckLake  *ducklakepostgres.Repository
	Operation deploymentmodule.NativeBuildOperationAuthority
	Request   deploymentmodule.NativeDeliveryBuildRequest
	Digest    string
	Record    deploymentmodule.NativeOperationRecord
	Input     NativeBuildRecoveryPreparationInput
}

func newNativeRecoveryPreparationFixture(t *testing.T) nativeRecoveryPreparationFixture {
	return newNativeRecoveryPreparationFixtureMode(t, true)
}

func newNativeRecoveryPreparationFixtureMode(t *testing.T, expireOperation bool) nativeRecoveryPreparationFixture {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "native_build_recovery_preparation")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), operationpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	delivery := deploymentnative.New(p)
	ducklake := ducklakepostgres.New(p)
	operationLeaseDuration := time.Minute
	if expireOperation {
		operationLeaseDuration = 100 * time.Millisecond
	}
	operations := deploymentoperation.New(operationpostgres.NewWithConfig(p, operationLeaseDuration, time.Hour))
	request := deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: projectgraph.ResourceID("project-recovery-preparation"), TargetID: "target-recovery-preparation",
		Environment: "prod", PlanID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000001001"),
		PrincipalID: "principal-recovery-preparation", IdempotencyKey: "recovery-preparation-1",
	}
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	plan := nativePlanFixture(t, deploymentnative.PlanInput{
		PlanID: request.PlanID.String(), TargetID: request.TargetID, PlanRevision: 1,
		CompiledGraphDigest: preparationDigest('d'), CompiledConfigDigest: preparationDigest('e'),
		SecurityDomainFingerprint: preparationDigest('f'), ArtifactDigest: preparationDigest('c'),
		QualificationDigest: preparationDigest('1'),
	}, request.ProjectID.String())
	if _, err := delivery.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	const catalogID = "catalog-recovery-preparation"
	if _, err := ducklake.RegisterCatalog(t.Context(), ducklakepostgres.CatalogIdentity{
		PhysicalPoolID: "pool-recovery-preparation", CatalogDatabase: "ducklake", CatalogID: catalogID,
		CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000001099", MetadataSchema: "main",
	}); err != nil {
		t.Fatal(err)
	}
	owner := "0198f2c0-7c7a-7f00-8a11-000000001002"
	reserved, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{Request: request, RequestDigest: digest, OwnerID: owner, LeaseDuration: operationLeaseDuration})
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := nativeBuildConsequenceID(reserved.Operation.OperationID, "candidate")
	if err != nil {
		t.Fatal(err)
	}
	generationID, err := nativeBuildConsequenceID(reserved.Operation.OperationID, "generation")
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := nativeBuildConsequenceID(reserved.Operation.OperationID, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	leaseID, err := nativeBuildConsequenceID(reserved.Operation.OperationID, "lease")
	if err != nil {
		t.Fatal(err)
	}
	attemptIdentity := "native-build/" + reserved.Operation.OperationID
	tx, err = p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	boundOperationAttempt, err := operations.BeginAttemptTx(t.Context(), tx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: reserved.Lease, AttemptID: attemptID, AttemptIdentity: attemptIdentity})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := delivery.CreateCandidateAllocatedTx(t.Context(), tx, deploymentnative.CandidateInput{CandidateID: candidateID, TargetID: request.TargetID, PlanID: request.PlanID.String(), ArtifactDigest: plan.ArtifactDigest}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	admission, err := NewCandidateBuildAttemptAdmission(delivery, ducklake)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	_, err = admission.AdmitCandidateBuildAttemptTx(t.Context(), tx, CandidateBuildAttemptAdmissionInput{
		Lease:    deploymentnative.LeaseInput{LeaseID: leaseID, TargetID: request.TargetID, OwnerID: request.PrincipalID, ExpiresAt: reserved.Lease.LeaseExpiresAt},
		Attempt:  deploymentnative.BuildAttemptInput{AttemptID: attemptID, PlanID: request.PlanID.String(), CandidateID: candidateID, OwnerID: request.PrincipalID, PhysicalPoolID: "pool-recovery-preparation", RequestDigest: digest, PlanDigest: plan.PlanDigest, SessionIdentity: "session-recovery-preparation", LeaseExpiresAt: reserved.Lease.LeaseExpiresAt},
		Artifact: CandidateBuildArtifactInput{ServingArtifactID: "artifact-" + strings.TrimPrefix(plan.ArtifactDigest, "sha256:"), ServingArtifactDigest: plan.ArtifactDigest, ServingStateID: generationID}, CatalogID: catalogID,
	})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	attempt, err := delivery.LoadBuildAttempt(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	var operationRecord deploymentmodule.NativeOperationRecord
	if expireOperation {
		for wait := time.Until(reserved.Lease.LeaseExpiresAt) + 20*time.Millisecond; wait > 0; wait = time.Until(reserved.Lease.LeaseExpiresAt) + 20*time.Millisecond {
			time.Sleep(wait)
		}
		indeterminate, err := ReserveNativeBuildOperation(t.Context(), delivery, operations, NativeBuildOperationReservationInput{Request: request, RequestDigest: digest, OwnerID: "0198f2c0-7c7a-7f00-8a11-000000001003", LeaseDuration: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if indeterminate.Disposition != deploymentmodule.NativeOperationIndeterminate {
			t.Fatalf("expired operation disposition = %s", indeterminate.Disposition)
		}
		operationRecord = indeterminate.Operation
	} else {
		evidence, err := json.Marshal(nativeBuildTerminationEvidence{
			SchemaVersion: 1, AttemptID: attempt.AttemptID, OwnerID: attempt.OwnerID,
			FencingEpoch: attempt.FencingEpoch, RequestDigest: attempt.RequestDigest,
			PlanDigest: attempt.PlanDigest, PhysicalPoolID: attempt.PhysicalPoolID,
			Namespace: attempt.Namespace, SessionIdentity: attempt.SessionIdentity,
			Phase: NativePhysicalBuildPhaseEvidence, Classification: NativePhysicalFailureIndeterminate,
			ErrorDigest: preparationDigest('a'),
		})
		if err != nil {
			t.Fatal(err)
		}
		termination, err := NewAttemptTermination(delivery, ducklake)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := termination.MarkAttemptIndeterminateTx(t.Context(), tx, AttemptTerminationInput{AttemptID: attempt.AttemptID, OwnerID: attempt.OwnerID, FencingEpoch: attempt.FencingEpoch, Evidence: evidence}); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := operations.MarkIndeterminateTx(t.Context(), tx, boundOperationAttempt.Lease, evidence); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		lease, err := delivery.Lease(t.Context(), leaseID)
		if err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := delivery.ReleaseLeaseAfterAttemptTerminationTx(t.Context(), tx, deploymentnative.LeaseFence{LeaseID: lease.LeaseID, TargetID: lease.TargetID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		var found bool
		operationRecord, found, err = operations.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, OwnerID: reserved.Operation.OwnerID})
		if err != nil || !found {
			t.Fatalf("lookup direct indeterminate operation: found=%v err=%v", found, err)
		}
	}
	return nativeRecoveryPreparationFixture{
		Pool: p, Delivery: delivery, DuckLake: ducklake, Operation: operations, Request: request, Digest: digest,
		Record: operationRecord,
		Input:  NativeBuildRecoveryPreparationInput{Request: request, RequestDigest: digest, Operation: operationRecord, PhysicalPoolID: attempt.PhysicalPoolID, CatalogID: catalogID},
	}
}

func preparationDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func TestPrepareNativeBuildRecoveryNormalizesAndReplays(t *testing.T) {
	f := newNativeRecoveryPreparationFixture(t)
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	first, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, f.Input)
	if err != nil {
		t.Fatalf("prepare recovery: %v", err)
	}
	if first.Operation.State != deploymentmodule.NativeOperationStateIndeterminate || first.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate || first.DuckLakeAttempt.State != ducklakepostgres.AttemptIndeterminate || first.Lease.State != "released" {
		t.Fatalf("prepared recovery = %#v", first)
	}
	if first.CandidateID == "" || first.GenerationID == "" || first.AttemptID == "" || first.LeaseID == "" || first.Artifact.ServingStateID != first.GenerationID {
		t.Fatalf("prepared deterministic identities = %#v", first)
	}

	secondInput := f.Input
	secondInput.Operation = first.Operation
	second, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, secondInput)
	if err != nil {
		t.Fatalf("exact recovery replay: %v", err)
	}
	if second.Operation.OperationID != first.Operation.OperationID || string(second.Operation.AttemptEvidence) != string(first.Operation.AttemptEvidence) || second.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate || second.DuckLakeAttempt.State != ducklakepostgres.AttemptIndeterminate || second.Lease.State != "released" {
		t.Fatalf("replayed recovery = %#v, first=%#v", second, first)
	}
}

func TestPrepareNativeBuildRecoveryRollsBackWhenOperationEvidenceDiffers(t *testing.T) {
	f := newNativeRecoveryPreparationFixture(t)
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	bad := f.Input
	bad.Operation.AttemptEvidence = []byte(`{"code":"different"}`)
	if _, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, bad); !errors.Is(err, deploymentmodule.ErrNativeOperationConflict) && !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("mismatched operation evidence error = %v", err)
	}
	attemptID, _ := nativeBuildConsequenceID(f.Record.OperationID, "attempt")
	attempt, err := f.Delivery.LoadBuildAttempt(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != deploymentnative.AttemptRunning {
		t.Fatalf("delivery attempt changed despite rollback: %#v", attempt)
	}
	leaseID, _ := nativeBuildConsequenceID(f.Record.OperationID, "lease")
	lease, err := f.Delivery.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != "active" {
		t.Fatalf("target lease changed despite rollback: %#v", lease)
	}
	record, found, err := f.Operation.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Request.IdempotencyKey, RequestDigest: f.Digest, OwnerID: f.Record.OwnerID})
	if err != nil || !found || string(record.AttemptEvidence) != string(f.Record.AttemptEvidence) {
		t.Fatalf("operation changed despite rollback: found=%v err=%v record=%#v", found, err, record)
	}
}

func TestPrepareNativeBuildRecoveryAllowsBindingToBeAddedDuringFinalAdmission(t *testing.T) {
	f := newNativeRecoveryPreparationFixture(t)
	attemptID, err := nativeBuildConsequenceID(f.Record.OperationID, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	// The immutable binding is normally created during attempt admission. A
	// recovery crash can precede that insert, so exercise the preparation seam
	// with the binding absent; final generation admission owns the recovered bind.
	if _, err := f.Pool.Exec(t.Context(), `ALTER TABLE delivery.delivery_build_artifact_binding DISABLE TRIGGER delivery_build_artifact_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	_, deleteErr := f.Pool.Exec(t.Context(), `DELETE FROM delivery.delivery_build_artifact_binding WHERE attempt_id = $1`, attemptID)
	_, enableErr := f.Pool.Exec(t.Context(), `ALTER TABLE delivery.delivery_build_artifact_binding ENABLE TRIGGER delivery_build_artifact_binding_immutable`)
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if enableErr != nil {
		t.Fatal(enableErr)
	}
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, f.Input)
	if err != nil {
		t.Fatalf("prepare recovery without prior binding: %v", err)
	}
	if prepared.Artifact.AttemptID != "" || prepared.GenerationID == "" || prepared.Lease.State != "released" {
		t.Fatalf("binding-absent preparation = %#v", prepared)
	}
}

func TestPrepareNativeBuildRecoveryReplaysDirectIndeterminateOperation(t *testing.T) {
	f := newNativeRecoveryPreparationFixtureMode(t, false)
	termination, err := NewAttemptTermination(f.Delivery, f.DuckLake)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, f.Input)
	if err != nil {
		t.Fatalf("prepare direct indeterminate recovery: %v", err)
	}
	if prepared.Operation.FencingGeneration != 2 || prepared.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate || prepared.DuckLakeAttempt.State != ducklakepostgres.AttemptIndeterminate || prepared.Lease.State != "released" {
		t.Fatalf("direct indeterminate preparation = %#v", prepared)
	}
	if _, err := PrepareNativeBuildRecovery(t.Context(), f.Delivery, f.Operation, termination, f.Input); err != nil {
		t.Fatalf("direct indeterminate replay: %v", err)
	}
}
