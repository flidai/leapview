package deploymentpostgres

// This file owns the final, recovery-only native build hand-off.  Recovery
// has already resolved the external marker and normalized both attempt
// ledgers; this boundary only composes those exact values into one delivery
// transaction.  It intentionally does not call BuildPlan or open any
// physical/catalog authority.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/pkg/jobs"
)

// nativeBuildRecoveryFinalizationInput is the complete value-only hand-off
// from marker recovery and seal assembly to the durable completion boundary.
// Admission is expected to contain the exact released target lease and the
// indeterminate delivery/DuckLake attempt ledgers returned by recovery
// preparation.  Its artifact value is the expected immutable binding; the
// binding itself may be absent until this transaction's first step.
type nativeBuildRecoveryFinalizationInput struct {
	Request       deploymentmodule.NativeDeliveryBuildRequest
	RequestDigest string
	Reservation   NativeBuildOperationReservationResult
	Plan          deploymentdomain.DeliveryPlan
	Assembled     GenerationAdmissionInput
	Artifacts     release.CandidateArtifactSet
	Admission     CandidateBuildAttemptAdmissionResult
	Physical      NativePhysicalBuildEvidence
	SealID        string
	GenerationID  string
}

const maxNativeBuildRecoveryEvidenceBytes = 64 << 10

// nativeBuildRecoveryEvidence is deliberately bounded and contains only
// canonical identities/digests and the resolved marker/snapshot.  In
// particular, it never persists raw physical errors or arbitrary catalog
// responses as operation resolution evidence.
type nativeBuildRecoveryEvidence struct {
	SchemaVersion          int             `json:"schemaVersion"`
	OperationID            string          `json:"operationId"`
	AttemptID              string          `json:"attemptId"`
	AttemptIdentity        string          `json:"attemptIdentity"`
	GenerationID           string          `json:"generationId"`
	SealID                 string          `json:"sealId"`
	CandidateID            string          `json:"candidateId"`
	RequestDigest          string          `json:"requestDigest"`
	PlanDigest             string          `json:"planDigest"`
	PhysicalPoolID         string          `json:"physicalPoolId"`
	CatalogID              string          `json:"catalogId"`
	SnapshotID             int64           `json:"snapshotId"`
	FencingEpoch           int64           `json:"fencingEpoch"`
	ServingArtifactDigest  string          `json:"servingArtifactDigest"`
	QualificationDigest    string          `json:"qualificationDigest"`
	ObjectRoot             string          `json:"objectRoot"`
	RelationManifestDigest string          `json:"relationManifestDigest"`
	ClosureDigest          string          `json:"closureDigest"`
	CommitMarker           json.RawMessage `json:"commitMarker"`
}

// completeRecoveredNativeBuild owns one delivery transaction and commits only
// after every durable consequence has been reloaded and verified.  Recovery
// callers are intentionally kept inside this package: BuildPlan remains the
// only public fresh-build entry point.
func (c *NativeBuildCoordinator) completeRecoveredNativeBuild(ctx context.Context, input nativeBuildRecoveryFinalizationInput) (deploymentmodule.NativeDeliveryBuild, error) {
	if c == nil || c.repository == nil || !c.repository.Configured() || !c.repository.TransactionCapable() {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeNativeBuildRecoveryFinalizationInput(input)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	result, err := c.finalizeRecoveredNativeBuildTx(ctx, tx, normalized)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed = true
	return result, nil
}

func normalizeNativeBuildRecoveryFinalizationInput(input nativeBuildRecoveryFinalizationInput) (nativeBuildRecoveryFinalizationInput, error) {
	request, err := normalizeNativeBuildRequest(input.Request)
	if err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if input.RequestDigest != digest {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery request digest differs from canonical request", deploymentdomain.ErrDeliveryConflict)
	}
	reservation := input.Reservation
	if reservation.Disposition != deploymentmodule.NativeOperationIndeterminate || reservation.Operation.State != deploymentmodule.NativeOperationStateIndeterminate {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery finalization requires an indeterminate operation", deploymentdomain.ErrDeliveryConflict)
	}
	if !nativeBuildLeaseIsZero(reservation.Lease) {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery operation returned an executable lease", deploymentdomain.ErrDeliveryConflict)
	}
	op := reservation.Operation
	if op.Scope != request.TargetID || op.OperationType != nativeBuildOperationType || op.IdempotencyKey != request.IdempotencyKey || op.RequestDigest != digest {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery operation identity differs from request", deploymentdomain.ErrDeliveryConflict)
	}
	for label, value := range map[string]string{"operation id": op.OperationID, "operation owner": op.OwnerID, "attempt id": op.AttemptID} {
		if _, err := canonicalUUIDv7(value); err != nil {
			return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery %s identity: %v", deploymentdomain.ErrDeliveryConflict, label, err)
		}
	}
	if op.AttemptIdentity == "" || op.AttemptIdentity != "native-build/"+op.OperationID || op.FencingGeneration <= 0 || op.LeaseExpiresAt.IsZero() || !op.LeaseExpiresAt.Equal(op.LeaseExpiresAt.UTC()) {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery operation attempt fence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	if expected, idErr := nativeBuildConsequenceID(op.OperationID, "attempt"); idErr != nil || op.AttemptID != expected {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery operation attempt identity is not deterministic", deploymentdomain.ErrDeliveryConflict)
	}
	for role, value := range map[string]string{"lease": input.Admission.Lease.LeaseID, "generation": input.GenerationID, "seal": input.SealID} {
		expected, idErr := nativeBuildConsequenceID(op.OperationID, role)
		if idErr != nil || value != expected {
			return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery %s identity is not deterministic", deploymentdomain.ErrDeliveryConflict, role)
		}
	}
	canonicalAttemptEvidence, evidenceErr := canonicalTerminationEvidence(op.AttemptEvidence)
	if evidenceErr != nil || !bytes.Equal(canonicalAttemptEvidence, op.AttemptEvidence) {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery operation attempt evidence is not canonical", deploymentdomain.ErrDeliveryConflict)
	}
	if err := input.Plan.Validate(); err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if input.Plan.ID != request.PlanID.String() || input.Plan.ProjectID != request.ProjectID || input.Plan.TargetID != request.TargetID || input.Plan.Environment != request.Environment {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery plan identity differs from request", deploymentdomain.ErrDeliveryConflict)
	}
	assembled, err := normalizeInput(input.Assembled)
	if err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if assembled.Generation.GenerationID != input.GenerationID || assembled.Seal.SealID != input.SealID || assembled.Generation.PlanID != input.Plan.ID || assembled.Seal.PlanDigest != input.Plan.Digest || assembled.Seal.RequestDigest != digest || assembled.Commit.OwnerID != request.PrincipalID {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: assembled recovery evidence differs from request or plan", deploymentdomain.ErrDeliveryConflict)
	}
	if err := validateNativeBuildArtifacts(input.Artifacts, request, input.Plan); err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if err := validateNativeBuildRecoveryAdmission(input.Admission, assembled, input.Artifacts, op); err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	marker, canonicalMarker, err := canonicalBuildMarker(input.Physical)
	if err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if err := validateNativeBuildRecoveryPhysical(input.Physical, marker, canonicalMarker, assembled, input.Admission, input.Artifacts, op, request, input.GenerationID, input.SealID); err != nil {
		return nativeBuildRecoveryFinalizationInput{}, err
	}
	if expected, err := nativeBuildConsequenceID(op.OperationID, "candidate"); err != nil || assembled.Generation.CandidateID != expected || input.Admission.Attempt.CandidateID != expected {
		return nativeBuildRecoveryFinalizationInput{}, fmt.Errorf("%w: recovery candidate identity is not deterministic", deploymentdomain.ErrDeliveryConflict)
	}
	return nativeBuildRecoveryFinalizationInput{Request: request, RequestDigest: digest, Reservation: reservation, Plan: input.Plan, Assembled: assembled, Artifacts: input.Artifacts, Admission: input.Admission, Physical: input.Physical, SealID: input.SealID, GenerationID: input.GenerationID}, nil
}

func validateNativeBuildRecoveryAdmission(admission CandidateBuildAttemptAdmissionResult, assembled GenerationAdmissionInput, artifacts release.CandidateArtifactSet, operation deploymentmodule.NativeOperationRecord) error {
	attempt, lease, duckAttempt := admission.Attempt, admission.Lease, admission.DuckLakeAttempt
	if attempt.AttemptID != assembled.Commit.AttemptID || attempt.PlanID != assembled.Generation.PlanID || attempt.OwnerID != assembled.Commit.OwnerID || attempt.FencingEpoch != assembled.Commit.FencingEpoch || attempt.State != deploymentnative.AttemptIndeterminate || lease.LeaseID != assembled.Fence.LeaseID || lease.TargetID != assembled.Fence.TargetID || lease.OwnerID != assembled.Fence.OwnerID || lease.FencingEpoch != assembled.Fence.FencingEpoch || lease.State != "released" || lease.ExpiresAt.IsZero() || lease.ReleasedAt.IsZero() || !lease.ExpiresAt.Equal(operation.LeaseExpiresAt) {
		return fmt.Errorf("%w: recovered admission lease or attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if duckAttempt.AttemptID != attempt.AttemptID || duckAttempt.OwnerID != attempt.OwnerID || duckAttempt.FencingEpoch != attempt.FencingEpoch || duckAttempt.State != ducklakepostgres.AttemptIndeterminate || !duckAttempt.LeaseExpiresAt.Equal(lease.ExpiresAt) {
		return fmt.Errorf("%w: recovered DuckLake attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if !sameTerminationEvidence(operation.AttemptEvidence, attempt.TerminationEvidence) {
		return fmt.Errorf("%w: recovery operation and attempt ledgers disagree", deploymentdomain.ErrDeliveryConflict)
	}
	if err := validateRecoveredAttemptTermination(attempt, duckAttempt); err != nil {
		return err
	}
	want := artifacts.Generation
	got := admission.Artifact
	expectedArtifactID := "artifact-" + strings.TrimPrefix(want.ArtifactDigest, "sha256:")
	if got.AttemptID != attempt.AttemptID || got.ServingArtifactID != want.ServingArtifactID || got.ServingArtifactID != expectedArtifactID || got.ServingArtifactDigest != want.ArtifactDigest || got.ServingStateID != assembled.Generation.GenerationID {
		return fmt.Errorf("%w: recovered artifact value differs", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}

func validateNativeBuildRecoveryPhysical(build NativePhysicalBuildEvidence, parsed catalogartifact.CommitMarker, canonicalMarker []byte, assembled GenerationAdmissionInput, admission CandidateBuildAttemptAdmissionResult, artifacts release.CandidateArtifactSet, operation deploymentmodule.NativeOperationRecord, request deploymentmodule.NativeDeliveryBuildRequest, generationID, sealID string) error {
	if parsed.DeliveryID != operation.OperationID || parsed.GenerationID != generationID || parsed.AttemptID != admission.Attempt.AttemptID || parsed.PlanDigest != assembled.Generation.PlanDigest || parsed.RequestDigest != assembled.Seal.RequestDigest || parsed.Project != request.ProjectID.String() || parsed.Environment != request.Environment || parsed.PhysicalPoolID != admission.Attempt.PhysicalPoolID || parsed.LeaseEpoch != admission.Attempt.FencingEpoch || !bytes.Equal(canonicalMarker, assembled.Commit.CommitMarker) {
		return fmt.Errorf("%w: recovered commit marker identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	catalogVersion, err := canonicalNumericCatalogVersion(build.Seal.CatalogVersion)
	if err != nil || catalogVersion != assembled.Seal.CatalogVersion {
		return fmt.Errorf("%w: recovered catalog version differs from assembled seal", deploymentdomain.ErrDeliveryConflict)
	}
	if build.AttemptID != admission.Attempt.AttemptID || build.CatalogID != assembled.Seal.CatalogID || build.SnapshotID != assembled.Commit.SnapshotID || build.ObjectRoot != assembled.Seal.ObjectRoot || build.Seal.SnapshotID != build.SnapshotID || build.Seal.CatalogType != "postgres" || build.Seal.MetadataSchema != ducklake.MetadataSchemaForPool(admission.Attempt.PhysicalPoolID) || build.Seal.DataPath != build.ObjectRoot || build.Seal.ExtensionVersion != assembled.Seal.DuckLakeExtensionVersion || build.Seal.CommitMarker != string(canonicalMarker) || build.Closure.SnapshotID != build.SnapshotID || build.Closure.CatalogID != build.CatalogID || build.Closure.ObjectRoot != build.ObjectRoot || build.Closure.RelationNamespace != assembled.Seal.RelationNamespace || build.Closure.RelationManifestDigest != assembled.Seal.RelationManifestDigest || build.Closure.ClosureDigest != assembled.Seal.ClosureDigest || build.Closure.ObjectRootDigest != assembled.Seal.ObjectRootDigest {
		return fmt.Errorf("%w: recovered physical evidence differs from assembled seal", deploymentdomain.ErrDeliveryConflict)
	}
	if artifacts.Generation.Identity.GenerationID != generationID || artifacts.Generation.ServingArtifactID != assembled.Seal.ServingArtifactID || artifacts.Generation.ArtifactDigest != assembled.Seal.ServingArtifactDigest || assembled.Seal.SealID != sealID {
		return fmt.Errorf("%w: recovered artifact or seal identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}

func (c *NativeBuildCoordinator) finalizeRecoveredNativeBuildTx(ctx context.Context, tx deploymentnative.Tx, input nativeBuildRecoveryFinalizationInput) (deploymentmodule.NativeDeliveryBuild, error) {
	if c.attemptTermination == nil || c.generationAdmission == nil || c.operations == nil || c.events == nil || c.audit == nil {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	marker, canonicalMarker, err := canonicalBuildMarker(input.Physical)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	// Heartbeats lock operation -> target lease -> delivery attempt -> DuckLake,
	// while fresh completion starts at the target lease. Recovery takes both
	// read locks in that canonical order before its first mutation, preventing
	// stale writers from forming operation/lease/attempt lock cycles.
	lockedOperation, err := lockNativeBuildOperationTx(ctx, tx, c.operations, input.Reservation.Operation, deploymentmodule.NativeOperationStateIndeterminate, deploymentmodule.NativeOperationStateCompleted)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if !sameTerminationEvidence(lockedOperation.AttemptEvidence, input.Reservation.Operation.AttemptEvidence) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: locked recovery operation evidence differs", deploymentdomain.ErrDeliveryConflict)
	}
	lockedLease, err := c.repository.LockLeaseTx(ctx, tx, input.Admission.Lease.LeaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := validateRecoveredFinalizationLease(lockedLease, input.Admission.Lease); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	binding, err := c.repository.BindRecoveredBuildArtifactTx(ctx, tx, deploymentnative.RecoveredBuildArtifactBindingInput{
		AttemptID: input.Admission.Attempt.AttemptID, ServingArtifactID: input.Admission.Artifact.ServingArtifactID,
		ServingArtifactDigest: input.Admission.Artifact.ServingArtifactDigest, ServingStateID: input.Admission.Artifact.ServingStateID,
		OwnerID: input.Admission.Attempt.OwnerID, FencingEpoch: input.Admission.Attempt.FencingEpoch, CommitMarker: canonicalMarker,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if binding.AttemptID != input.Admission.Attempt.AttemptID || binding.ServingArtifactID != input.Admission.Artifact.ServingArtifactID || binding.ServingArtifactDigest != input.Admission.Artifact.ServingArtifactDigest || binding.ServingStateID != input.Admission.Artifact.ServingStateID || binding.BoundAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered artifact binding identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	termination, err := c.attemptTermination.ReconcileAttemptTx(ctx, tx, AttemptReconciliationInput{
		AttemptID: input.Admission.Attempt.AttemptID, OwnerID: input.Admission.Attempt.OwnerID, FencingEpoch: input.Admission.Attempt.FencingEpoch,
		PhysicalPoolID: marker.PhysicalPoolID, CatalogID: input.Physical.CatalogID, SnapshotID: input.Physical.SnapshotID,
		CommitMarker: canonicalMarker, State: deploymentnative.AttemptCommitted, SessionIdentity: input.Admission.Attempt.SessionIdentity,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if termination.DeliveryAttempt.State != deploymentnative.AttemptCommitted || termination.DuckLakeAttempt.State != ducklakepostgres.AttemptCommitted || termination.DeliveryAttempt.SnapshotID != input.Physical.SnapshotID || !sameCommitMarker(termination.DeliveryAttempt.CommitMarker, canonicalMarker) || !sameCommitMarker([]byte(termination.DuckLakeAttempt.CommitMarker), canonicalMarker) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered attempt reconciliation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if err := c.repository.ReleaseLeaseAfterAttemptTerminationTx(ctx, tx, deploymentnative.LeaseFence{LeaseID: input.Admission.Lease.LeaseID, TargetID: input.Admission.Lease.TargetID, OwnerID: input.Admission.Lease.OwnerID, FencingEpoch: input.Admission.Lease.FencingEpoch}); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	lease, err := c.repository.LeaseTx(ctx, tx, input.Admission.Lease.LeaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := validateRecoveredFinalizationLease(lease, input.Admission.Lease); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	generation, err := c.generationAdmission.CompleteBuildAndAdmitTx(ctx, tx, input.Assembled)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	eventID, err := nativeBuildConsequenceID(input.Reservation.Operation.OperationID, "event")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	auditID, err := nativeBuildConsequenceID(input.Reservation.Operation.OperationID, "audit")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	payload, err := json.Marshal(nativeBuildEventPayload{OperationID: input.Reservation.Operation.OperationID, ProjectID: input.Request.ProjectID.String(), ResourceID: input.GenerationID, Status: "sealed"})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	event, err := c.events.AppendDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{EventID: eventID, ScopeID: input.Request.TargetID, AggregateType: "delivery_build", AggregateID: input.Reservation.Operation.OperationID, EventType: "delivery.build.sealed", SchemaVersion: 1, CorrelationID: input.Reservation.Operation.OperationID, Payload: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if event.EventID != eventID || event.ScopeID != input.Request.TargetID || event.AggregateType != "delivery_build" || event.AggregateID != input.Reservation.Operation.OperationID || event.EventType != "delivery.build.sealed" || event.SchemaVersion != 1 || event.CorrelationID != input.Reservation.Operation.OperationID || event.AggregateVersion <= 0 || event.OccurredAt.IsZero() || !sameNativeJSON(event.Payload, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered build event identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{AuditID: auditID, DomainEventID: eventID, ScopeID: input.Request.TargetID, ActorID: input.Request.PrincipalID, Action: "delivery.build.sealed", ResourceKind: "build", ResourceID: input.Reservation.Operation.OperationID, Outcome: "accepted", Operation: "build", RequestDigest: input.RequestDigest, CorrelationID: input.Reservation.Operation.OperationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if audit.AuditID != auditID || audit.EventID != eventID || audit.ScopeID != input.Request.TargetID || audit.ActorID != input.Request.PrincipalID || audit.Action != "delivery.build.sealed" || audit.ResourceKind != "build" || audit.ResourceID != input.Reservation.Operation.OperationID || audit.Outcome != "accepted" || audit.RequestDigest != input.RequestDigest || audit.OccurredAt.IsZero() || !sameNativeJSON(audit.Metadata, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered build audit identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if c.workflow != nil {
		if err := c.workflow.RecordWorkflow(ctx, tx, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "delivery.build.sealed/" + input.Reservation.Operation.OperationID, ResourceKind: "build", ResourceID: input.Reservation.Operation.OperationID, EventType: "delivery.build.sealed", Data: payload}}); err != nil {
			return deploymentmodule.NativeDeliveryBuild{}, err
		}
	}
	outcome := nativeBuildOutcome{OperationID: input.Reservation.Operation.OperationID, OperationOwnerID: input.Reservation.Operation.OwnerID, PlanID: input.Plan.ID, CandidateID: input.Admission.Attempt.CandidateID, AttemptID: input.Admission.Attempt.AttemptID, LeaseID: input.Admission.Lease.LeaseID, GenerationID: input.GenerationID, SealID: input.SealID, EventID: eventID, AuditID: auditID, ServingArtifactID: input.Artifacts.Generation.ServingArtifactID, ProjectID: input.Request.ProjectID.String(), TargetID: input.Request.TargetID, Environment: input.Request.Environment, ActorID: input.Request.PrincipalID, IdempotencyKey: input.Request.IdempotencyKey, RequestDigest: input.RequestDigest, PlanDigest: input.Plan.Digest, SourceDigest: input.Plan.SourceDigest, ExecutionDigest: input.Plan.ExecutionDigest, QualificationDigest: input.Assembled.QualificationDigest, ServingArtifactDigest: input.Artifacts.Generation.ArtifactDigest, Status: "sealed"}
	outcomeJSON, err := encodeNativeBuildOutcome(outcome, input.Request, deploymentmodule.NativeOperationAcquireInput{Scope: input.Request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: input.Request.IdempotencyKey, RequestDigest: input.RequestDigest, OwnerID: input.Reservation.Operation.OwnerID})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	recoveryJSON, err := encodeNativeBuildRecoveryEvidence(input, marker, canonicalMarker)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	reconciled, err := c.operations.ReconcileAttemptTx(ctx, tx, deploymentmodule.NativeOperationReconcileAttemptInput{Scope: input.Request.TargetID, IdempotencyKey: input.Request.IdempotencyKey, AttemptID: input.Admission.Attempt.AttemptID, AttemptIdentity: input.Reservation.Operation.AttemptIdentity, State: deploymentmodule.NativeOperationStateCompleted, Outcome: outcomeJSON, Evidence: recoveryJSON})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := verifyRecoveredCompletedOperation(reconciled.Operation, input, outcomeJSON, recoveryJSON); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	completedAttempt, err := c.repository.BuildAttemptTx(ctx, tx, input.Admission.Attempt.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if completedAttempt.State != deploymentnative.AttemptCommitted || completedAttempt.AttemptID != input.Assembled.Commit.AttemptID || completedAttempt.PlanID != input.Assembled.Generation.PlanID || completedAttempt.CandidateID != input.Assembled.Generation.CandidateID || completedAttempt.OwnerID != input.Assembled.Commit.OwnerID || completedAttempt.PhysicalPoolID != input.Assembled.Seal.PhysicalPoolID || completedAttempt.FencingEpoch != input.Assembled.Commit.FencingEpoch || completedAttempt.RequestDigest != input.Assembled.Seal.RequestDigest || completedAttempt.PlanDigest != input.Assembled.Generation.PlanDigest || completedAttempt.Namespace != input.Assembled.Seal.RelationNamespace || completedAttempt.SnapshotID != input.Assembled.Commit.SnapshotID || completedAttempt.SessionIdentity == "" || completedAttempt.LeaseExpiresAt.IsZero() || completedAttempt.FinishedAt.IsZero() || completedAttempt.UpdatedAt.IsZero() || completedAttempt.FinishedAt.Before(completedAttempt.CreatedAt) || len(completedAttempt.TerminationEvidence) != 0 || !sameCommitMarker(completedAttempt.CommitMarker, input.Assembled.Commit.CommitMarker) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: completed recovered build attempt evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	return nativeBuildProjection(outcome, input.Plan.BaseGenerationID, completedAttempt, lease, generation.CandidateRevision)
}

func validateRecoveredFinalizationLease(got, expected deploymentnative.DeliveryLease) error {
	if got.LeaseID != expected.LeaseID || got.State != "released" || got.TargetID != expected.TargetID || got.OwnerID != expected.OwnerID || got.FencingEpoch != expected.FencingEpoch || !got.AcquiredAt.Equal(expected.AcquiredAt) || !got.ExpiresAt.Equal(expected.ExpiresAt) || got.ReleasedAt.IsZero() || !got.ReleasedAt.Equal(expected.ReleasedAt) {
		return fmt.Errorf("%w: recovered target lease was not released exactly", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}

func encodeNativeBuildRecoveryEvidence(input nativeBuildRecoveryFinalizationInput, marker catalogartifact.CommitMarker, canonicalMarker []byte) (json.RawMessage, error) {
	evidence := nativeBuildRecoveryEvidence{SchemaVersion: 1, OperationID: input.Reservation.Operation.OperationID, AttemptID: input.Admission.Attempt.AttemptID, AttemptIdentity: input.Reservation.Operation.AttemptIdentity, GenerationID: input.GenerationID, SealID: input.SealID, CandidateID: input.Admission.Attempt.CandidateID, RequestDigest: input.RequestDigest, PlanDigest: input.Plan.Digest, PhysicalPoolID: marker.PhysicalPoolID, CatalogID: input.Physical.CatalogID, SnapshotID: input.Physical.SnapshotID, FencingEpoch: input.Admission.Attempt.FencingEpoch, ServingArtifactDigest: input.Artifacts.Generation.ArtifactDigest, QualificationDigest: input.Assembled.QualificationDigest, ObjectRoot: input.Physical.ObjectRoot, RelationManifestDigest: input.Physical.Closure.RelationManifestDigest, ClosureDigest: input.Physical.Closure.ClosureDigest, CommitMarker: append(json.RawMessage(nil), canonicalMarker...)}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxNativeBuildRecoveryEvidenceBytes {
		return nil, fmt.Errorf("%w: recovery evidence exceeds %d bytes", deploymentdomain.ErrDeliveryInvalid, maxNativeBuildRecoveryEvidenceBytes)
	}
	return encoded, nil
}

func verifyRecoveredCompletedOperation(got deploymentmodule.NativeOperationRecord, input nativeBuildRecoveryFinalizationInput, outcome, evidence []byte) error {
	op := input.Reservation.Operation
	if got.Scope != input.Request.TargetID || got.OperationType != nativeBuildOperationType || got.IdempotencyKey != input.Request.IdempotencyKey || got.RequestDigest != input.RequestDigest || got.OwnerID != op.OwnerID || got.OperationID != op.OperationID || got.State != deploymentmodule.NativeOperationStateCompleted || got.FencingGeneration != op.FencingGeneration || !got.LeaseExpiresAt.Equal(op.LeaseExpiresAt) || got.AttemptID != input.Admission.Attempt.AttemptID || got.AttemptIdentity != op.AttemptIdentity || !sameTerminationEvidence(got.AttemptEvidence, op.AttemptEvidence) || !sameNativeJSON(got.Outcome, outcome) || !sameNativeJSON(got.ResolutionEvidence, evidence) {
		return fmt.Errorf("%w: completed recovery operation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}
