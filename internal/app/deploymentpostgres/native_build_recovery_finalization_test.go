package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	deploymentevents "github.com/flidai/leapview/internal/app/deploymentevents"
	deploymentoperation "github.com/flidai/leapview/internal/app/deploymentoperation"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	eventpostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryFinalizeOperationConflict struct {
	deploymentmodule.NativeBuildOperationAuthority
}

func (recoveryFinalizeOperationConflict) ReconcileAttemptTx(context.Context, deploymentmodule.NativeOperationTx, deploymentmodule.NativeOperationReconcileAttemptInput) (deploymentmodule.NativeOperationReconcileAttemptResult, error) {
	return deploymentmodule.NativeOperationReconcileAttemptResult{}, deploymentmodule.ErrNativeOperationConflict
}

type recoveryFinalizeFixture struct {
	DB          *pgxpool.Pool
	Delivery    *deploymentnative.Repository
	DuckLake    *ducklakepostgres.Repository
	Coordinator *NativeBuildCoordinator
	Input       nativeBuildRecoveryFinalizationInput
}

func recoveryFinalizeDB(t *testing.T) (*pgxpool.Pool, *deploymentnative.Repository, *ducklakepostgres.Repository) {
	t.Helper()
	h := postgrestest.Start(t)
	db, err := pgxpool.New(t.Context(), h.NewDatabase(t, "native_build_recovery_finalization").AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := operationpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := servingnative.ApplySchema(t.Context(), tx); err != nil {
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
	return db, deploymentnative.New(db), ducklakepostgres.New(db)
}

func recoveryFinalizeFixtureForTest(t *testing.T) recoveryFinalizeFixture {
	db, delivery, ducklake := recoveryFinalizeDB(t)
	base := validNativeSealAssemblerInput(t)
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: base.Plan.ProjectID, Kind: projectgraph.KindProject, Name: "recovery_finalization"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	portableArtifact, err := projectartifact.NewProject(graph, projectmanifest.Project{ID: base.Plan.ProjectID.String(), Name: "recovery_finalization"})
	if err != nil {
		t.Fatal(err)
	}
	base.Artifacts.Compiler.Graph = graph
	base.Artifacts.Compiler.Artifact = portableArtifact
	base.Artifacts.Compiler.Manifest = portableArtifact.Manifest()
	base.Artifacts.Compiler.Plan = projectcompiler.ProjectPlan{Project: base.Plan.ProjectID.String(), Deterministic: true}
	base.Artifacts.Artifact.ProjectDigest = portableArtifact.Digest()
	base.Plan.ServingArtifactDigest = base.Artifacts.Generation.ArtifactDigest
	base.Plan.Digest = ""
	normalizedPlan, err := deploymentdomain.NewDeliveryPlan(base.Plan)
	if err != nil {
		t.Fatal(err)
	}
	base.Plan = normalizedPlan
	projectID := base.Plan.ProjectID
	planID, _ := uuid.Parse(base.Plan.ID)
	request := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: projectID, TargetID: base.Plan.TargetID, Environment: base.Plan.Environment, PlanID: planID, PrincipalID: "builder-assembler", IdempotencyKey: "native-recovery-finalization"}
	requestDigest, err := nativeBuildRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := "0198f2c0-7c7a-7f00-8a11-000000009101"
	operationAuth := deploymentoperation.New(operationpostgres.NewWithConfig(db, time.Hour, 24*time.Hour))
	operationTx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	opAcquire, err := operationAuth.AcquireTx(t.Context(), operationTx, deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: ownerID})
	if err != nil || opAcquire.Status != deploymentmodule.NativeOperationAcquired {
		_ = operationTx.Rollback(t.Context())
		t.Fatalf("operation acquire: status=%q err=%v", opAcquire.Status, err)
	}
	if err := operationTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	operationID := opAcquire.Operation.OperationID
	role := func(name string) string {
		id, e := nativeBuildConsequenceID(operationID, name)
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	candidateID, attemptID, leaseID, generationID, sealID := role("candidate"), role("attempt"), role("lease"), role("generation"), role("seal")
	base.GenerationID, base.SealID = generationID, sealID
	base.Artifacts.Generation.Identity.GenerationID = generationID
	base.Artifacts.Generation.ServingArtifactID = "artifact-" + strings.TrimPrefix(base.Artifacts.Generation.ArtifactDigest, "sha256:")
	base.AttemptAdmission.Artifact.ServingArtifactID = base.Artifacts.Generation.ServingArtifactID
	base.AttemptAdmission.Artifact.AttemptID = attemptID
	base.AttemptAdmission.Artifact.ServingArtifactDigest = base.Artifacts.Generation.ArtifactDigest
	base.AttemptAdmission.Artifact.ServingStateID = generationID
	base.Build.AttemptID = attemptID
	base.Build.Marker = catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: operationID, GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: base.Plan.Digest, Project: projectID.String(), Environment: base.Plan.Environment, PhysicalPoolID: base.AttemptAdmission.Attempt.PhysicalPoolID}
	markerJSON, err := base.Build.Marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	base.Build.CanonicalMarkerJSON = []byte(markerJSON)
	base.Build.Seal.CommitMarker = string(markerJSON)
	base.Artifacts.Generation.Identity.ProjectID, base.Artifacts.Generation.Identity.Environment = projectID, base.Plan.Environment
	base.AttemptAdmission.Attempt.AttemptID, base.AttemptAdmission.Attempt.CandidateID = attemptID, candidateID
	base.AttemptAdmission.Attempt.FencingEpoch = 1
	base.AttemptAdmission.Attempt.RequestDigest = requestDigest
	base.AttemptAdmission.Attempt.PlanDigest = base.Plan.Digest
	base.AttemptAdmission.Attempt.State = deploymentnative.AttemptIndeterminate
	base.AttemptAdmission.Lease.LeaseID, base.AttemptAdmission.Lease.State = leaseID, "released"
	base.AttemptAdmission.Lease.FencingEpoch = 1
	base.AttemptAdmission.Lease.ReleasedAt = base.AttemptAdmission.Lease.ExpiresAt.Add(time.Minute)
	base.AttemptAdmission.DuckLakeAttempt.AttemptID, base.AttemptAdmission.DuckLakeAttempt.State = attemptID, ducklakepostgres.AttemptIndeterminate
	base.AttemptAdmission.DuckLakeAttempt.FencingEpoch = 1
	base.AttemptAdmission.DuckLakeAttempt.RequestDigest = requestDigest
	base.AttemptAdmission.DuckLakeAttempt.PlanDigest = base.Plan.Digest
	ledgerTime := base.AttemptAdmission.Lease.AcquiredAt
	base.AttemptAdmission.Attempt.CreatedAt = ledgerTime
	base.AttemptAdmission.Attempt.UpdatedAt = ledgerTime
	base.AttemptAdmission.Attempt.FinishedAt = ledgerTime.Add(time.Second)
	base.AttemptAdmission.DuckLakeAttempt.CreatedAt = ledgerTime
	base.AttemptAdmission.DuckLakeAttempt.UpdatedAt = ledgerTime
	base.AttemptAdmission.DuckLakeAttempt.TerminalAt = ledgerTime.Add(time.Second)
	namespace, err := deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	base.AttemptAdmission.Attempt.Namespace, base.Build.Closure.RelationNamespace = namespace, namespace
	base.AttemptAdmission.Attempt.SessionIdentity = "session-recovery-finalization"
	base.AttemptAdmission.DuckLakeAttempt.SessionIdentity = "session-recovery-finalization"
	for i := range base.Build.Closure.Relations {
		base.Build.Closure.Relations[i].Schema = namespace
	}
	base.Build.Closure = nativeAssemblerClosure(t, base.Build.CatalogID, base.Build.ObjectRoot, namespace, base.Build.Closure.Relations)
	base.Qualification.CandidateID, base.Qualification.AttemptID, base.Qualification.RelationNamespace = candidateID, attemptID, namespace
	base.Qualification.RelationManifestDigest, base.Qualification.ClosureDigest = base.Build.Closure.RelationManifestDigest, base.Build.Closure.ClosureDigest
	base.Qualification.Gates.CandidateID = candidateID
	base.Qualification.Gates.Digest = ""
	canonicalGates, err := base.Qualification.Gates.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	base.Qualification.Gates = canonicalGates
	evidence, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "owner_id": request.PrincipalID, "fencing_epoch": 1, "phase": "recovery"})
	base.AttemptAdmission.Attempt.TerminationEvidence = evidence
	base.AttemptAdmission.DuckLakeAttempt.TerminationEvidence = evidence
	base.Qualification.Digest = ""
	_, qDigest, err := base.Qualification.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	base.Qualification.Digest = qDigest
	assembled, err := AssembleRecoveredNativeGenerationAdmissionInput(NativeRecoveredSealEvidenceAssemblerInput(base))
	if err != nil {
		t.Fatal(err)
	}
	planDoc, err := json.Marshal(base.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateTarget(t.Context(), deploymentnative.TargetInput{TargetID: request.TargetID, ProjectID: projectID.String(), Environment: request.Environment}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(t.Context(), deploymentnative.PlanInput{PlanID: base.Plan.ID, TargetID: request.TargetID, PlanDigest: base.Plan.Digest, PlanRevision: 1, CompiledGraphDigest: base.Artifacts.Compiler.Graph.Digest(), CompiledConfigDigest: base.Plan.Execution.ConfigDigest, SecurityDomainFingerprint: base.Artifacts.AuthorizationFingerprint, ArtifactDigest: base.Artifacts.Generation.ArtifactDigest, QualificationDigest: base.Plan.Governance.QualificationDigest, QualificationRequired: true, PlanDocument: planDoc, Evidence: json.RawMessage(`{}`), CreatedAt: base.Plan.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateCandidate(t.Context(), deploymentnative.CandidateInput{CandidateID: candidateID, TargetID: request.TargetID, PlanID: base.Plan.ID, CandidateRevision: 1, ArtifactDigest: base.Artifacts.Generation.ArtifactDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := ducklake.RegisterCatalog(t.Context(), base.CatalogIdentity); err != nil {
		t.Fatal(err)
	}
	firstTx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationAuth.BeginAttemptTx(t.Context(), firstTx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: opAcquire.Lease, AttemptID: attemptID, AttemptIdentity: "native-build/" + operationID}); err != nil {
		_ = firstTx.Rollback(t.Context())
		t.Fatal(err)
	}
	admissionAuth, err := NewCandidateBuildAttemptAdmission(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	// Re-open the operation lease projection after BeginAttempt; the adapter's
	// convenience call has committed the bind and retains no transaction.
	if err := firstTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	opRecord, found, err := operationAuth.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: ownerID})
	if err != nil || !found {
		t.Fatalf("operation lookup: %v", err)
	}
	operationLease := deploymentmodule.NativeOperationLease{Scope: opRecord.Scope, IdempotencyKey: opRecord.IdempotencyKey, OperationID: opRecord.OperationID, OwnerID: opRecord.OwnerID, FencingGeneration: opRecord.FencingGeneration, LeaseExpiresAt: opRecord.LeaseExpiresAt, AttemptID: opRecord.AttemptID, AttemptIdentity: opRecord.AttemptIdentity}
	firstTx, err = delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	attemptAdmission, err := admissionAuth.AdmitCandidateBuildAttemptTx(t.Context(), firstTx, CandidateBuildAttemptAdmissionInput{Lease: deploymentnative.LeaseInput{LeaseID: leaseID, TargetID: request.TargetID, OwnerID: request.PrincipalID, ExpiresAt: operationLease.LeaseExpiresAt}, Attempt: deploymentnative.BuildAttemptInput{AttemptID: attemptID, PlanID: base.Plan.ID, CandidateID: candidateID, OwnerID: request.PrincipalID, PhysicalPoolID: base.AttemptAdmission.Attempt.PhysicalPoolID, RequestDigest: requestDigest, PlanDigest: base.Plan.Digest, SessionIdentity: "session-recovery-finalization"}, Artifact: CandidateBuildArtifactInput{ServingArtifactID: base.Artifacts.Generation.ServingArtifactID, ServingArtifactDigest: base.Artifacts.Generation.ArtifactDigest, ServingStateID: generationID}, CatalogID: base.Build.CatalogID})
	if err != nil {
		_ = firstTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := firstTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	term, err := NewAttemptTermination(delivery, ducklake)
	if err != nil {
		t.Fatal(err)
	}
	secondTx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := term.MarkAttemptIndeterminateTx(t.Context(), secondTx, AttemptTerminationInput{AttemptID: attemptID, OwnerID: request.PrincipalID, FencingEpoch: 1, Evidence: evidence}); err != nil {
		_ = secondTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := operationAuth.MarkIndeterminateTx(t.Context(), secondTx, operationLease, evidence); err != nil {
		_ = secondTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := delivery.ReleaseLeaseAfterAttemptTerminationTx(t.Context(), secondTx, deploymentnative.LeaseFence{LeaseID: leaseID, TargetID: request.TargetID, OwnerID: request.PrincipalID, FencingEpoch: 1}); err != nil {
		_ = secondTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := secondTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `ALTER TABLE delivery.delivery_build_artifact_binding DISABLE TRIGGER delivery_build_artifact_binding_immutable`); err != nil {
		t.Fatal(err)
	}
	_, deleteErr := db.Exec(t.Context(), `DELETE FROM delivery.delivery_build_artifact_binding WHERE attempt_id = $1::uuid`, attemptID)
	_, enableErr := db.Exec(t.Context(), `ALTER TABLE delivery.delivery_build_artifact_binding ENABLE TRIGGER delivery_build_artifact_binding_immutable`)
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if enableErr != nil {
		t.Fatal(enableErr)
	}
	operation, found, err := operationAuth.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: ownerID})
	if err != nil || !found {
		t.Fatalf("indeterminate operation lookup: %v", err)
	}
	attempt, err := delivery.BuildAttempt(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := delivery.Lease(t.Context(), leaseID)
	if err != nil {
		t.Fatal(err)
	}
	duckAttempt, err := ducklake.LoadAttempt(t.Context(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	input := nativeBuildRecoveryFinalizationInput{Request: request, RequestDigest: requestDigest, Reservation: NativeBuildOperationReservationResult{Disposition: deploymentmodule.NativeOperationIndeterminate, Operation: operation}, Plan: base.Plan, Assembled: assembled, Artifacts: base.Artifacts, Admission: CandidateBuildAttemptAdmissionResult{Lease: lease, Attempt: attempt, Artifact: attemptAdmission.Artifact, DuckLakeAttempt: duckAttempt}, Physical: base.Build, SealID: sealID, GenerationID: generationID}
	coord := &NativeBuildCoordinator{repository: delivery, operations: operationAuth, attemptTermination: term, generationAdmission: mustGenerationAdmission(t, delivery, db, ducklake), events: deploymentevents.NewWithRepository(eventpostgres.New()), audit: deploymentaudit.NewWithRepository(accesspostgres.New())}
	return recoveryFinalizeFixture{DB: db, Delivery: delivery, DuckLake: ducklake, Coordinator: coord, Input: input}
}

func mustGenerationAdmission(t *testing.T, delivery *deploymentnative.Repository, db *pgxpool.Pool, ducklake *ducklakepostgres.Repository) GenerationAdmission {
	t.Helper()
	capability, err := NewGenerationAdmission(delivery, servingnative.New(db), ducklake)
	if err != nil {
		t.Fatal(err)
	}
	return capability
}

func TestCompleteRecoveredNativeBuildPostgresSuccessAndExactReplay(t *testing.T) {
	f := recoveryFinalizeFixtureForTest(t)
	// The physical catalog preserves the accepted lake.options spelling while
	// durable seal admission stores the canonical numeric version.
	f.Input.Physical.Seal.CatalogVersion = "ducklake:v1"
	first, err := f.Coordinator.completeRecoveredNativeBuild(t.Context(), f.Input)
	if err != nil {
		t.Fatalf("recovery finalization: %v", err)
	}
	second, err := f.Coordinator.completeRecoveredNativeBuild(t.Context(), f.Input)
	if err != nil {
		t.Fatalf("exact recovery replay: %v", err)
	}
	if first != second {
		t.Fatalf("replay projection changed: first=%+v second=%+v", first, second)
	}
	operation, found, err := f.Coordinator.operations.Lookup(t.Context(), deploymentmodule.NativeOperationAcquireInput{Scope: f.Input.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: f.Input.Request.IdempotencyKey, RequestDigest: f.Input.RequestDigest, OwnerID: f.Input.Reservation.Operation.OwnerID})
	if err != nil || !found || operation.State != deploymentmodule.NativeOperationStateCompleted {
		t.Fatalf("operation = %+v found=%v err=%v", operation, found, err)
	}
	var bindings, seals, generations, bundles, events, audits int
	if err := f.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery.delivery_build_artifact_binding WHERE attempt_id=$1::uuid), (SELECT count(*) FROM delivery.delivery_snapshot_seal WHERE seal_id=$2::uuid), (SELECT count(*) FROM delivery.delivery_generation WHERE generation_id=$3::uuid), (SELECT count(*) FROM serving_state.bundle WHERE generation_id=$3::uuid), (SELECT count(*) FROM event.event_log WHERE event_id=$4::uuid), (SELECT count(*) FROM audit.audit_event WHERE audit_id=$5::uuid)`, f.Input.Admission.Attempt.AttemptID, f.Input.SealID, f.Input.GenerationID, first.EventID, first.AuditID).Scan(&bindings, &seals, &generations, &bundles, &events, &audits); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 || seals != 1 || generations != 1 || bundles != 1 || events != 1 || audits != 1 {
		t.Fatalf("durable consequences = %d/%d/%d/%d/%d/%d", bindings, seals, generations, bundles, events, audits)
	}
}

func TestCompleteRecoveredNativeBuildPostgresLateOperationConflictRollsBack(t *testing.T) {
	f := recoveryFinalizeFixtureForTest(t)
	f.Coordinator.operations = recoveryFinalizeOperationConflict{NativeBuildOperationAuthority: f.Coordinator.operations}
	if _, err := f.Coordinator.completeRecoveredNativeBuild(t.Context(), f.Input); !errors.Is(err, deploymentmodule.ErrNativeOperationConflict) {
		t.Fatalf("late operation error = %v", err)
	}
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.Input.Admission.Attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != deploymentnative.AttemptIndeterminate {
		t.Fatalf("attempt state after rollback = %q", attempt.State)
	}
	duckAttempt, err := f.DuckLake.LoadAttempt(t.Context(), f.Input.Admission.Attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if duckAttempt.State != ducklakepostgres.AttemptIndeterminate {
		t.Fatalf("DuckLake attempt state after rollback = %q", duckAttempt.State)
	}
	if _, err := f.Delivery.BuildArtifactBinding(t.Context(), f.Input.Admission.Attempt.AttemptID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("binding after rollback = %v", err)
	}
	var seals, generations, bundles, events, audits int
	if err := f.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM delivery.delivery_snapshot_seal WHERE seal_id=$1::uuid), (SELECT count(*) FROM delivery.delivery_generation WHERE generation_id=$2::uuid), (SELECT count(*) FROM serving_state.bundle WHERE generation_id=$2::uuid), (SELECT count(*) FROM event.event_log), (SELECT count(*) FROM audit.audit_event)`, f.Input.SealID, f.Input.GenerationID).Scan(&seals, &generations, &bundles, &events, &audits); err != nil {
		t.Fatal(err)
	}
	if seals != 0 || generations != 0 || bundles != 0 || events != 0 || audits != 0 {
		t.Fatalf("late conflict retained durable consequences = %d/%d/%d/%d/%d", seals, generations, bundles, events, audits)
	}
}

func TestCompleteRecoveredNativeBuildRejectsOperationAttemptEvidenceMismatch(t *testing.T) {
	f := recoveryFinalizeFixtureForTest(t)
	f.Input.Reservation.Operation.AttemptEvidence = json.RawMessage(`{"reason":"different-operation-evidence"}`)
	if _, err := f.Coordinator.completeRecoveredNativeBuild(t.Context(), f.Input); !errors.Is(err, deploymentdomain.ErrDeliveryConflict) {
		t.Fatalf("operation-attempt evidence mismatch error = %v", err)
	}
	attempt, err := f.Delivery.BuildAttempt(t.Context(), f.Input.Admission.Attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != deploymentnative.AttemptIndeterminate {
		t.Fatalf("attempt state after rejected evidence = %q", attempt.State)
	}
}
