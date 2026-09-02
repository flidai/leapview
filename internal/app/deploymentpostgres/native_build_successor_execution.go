package deploymentpostgres

// This file contains the executable continuation after marker-absent
// successor admission. It deliberately reuses the same physical, qualification,
// and generation-admission capabilities as the root build while resolving the
// public operation through the append-only successor leaf.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/materialize"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/pkg/jobs"
)

type nativeBuildSuccessorResolutionEvidence struct {
	SchemaVersion   int             `json:"schemaVersion"`
	OperationID     string          `json:"operationId"`
	PredecessorID   string          `json:"predecessorAttemptId"`
	AttemptID       string          `json:"attemptId"`
	AttemptIdentity string          `json:"attemptIdentity"`
	GenerationID    string          `json:"generationId"`
	SealID          string          `json:"sealId"`
	CandidateID     string          `json:"candidateId"`
	RequestDigest   string          `json:"requestDigest"`
	PlanDigest      string          `json:"planDigest"`
	PhysicalPoolID  string          `json:"physicalPoolId"`
	CatalogID       string          `json:"catalogId"`
	SnapshotID      int64           `json:"snapshotId"`
	FencingEpoch    int64           `json:"fencingEpoch"`
	CommitMarker    json.RawMessage `json:"commitMarker"`
}

// executeNativeBuildSuccessor runs a fresh physical build only after the
// marker resolver proved the predecessor marker absent and the operation,
// delivery, artifact-binding, and DuckLake successor rows committed together.
func (c *NativeBuildCoordinator) executeNativeBuildSuccessor(
	ctx context.Context,
	request deploymentmodule.NativeDeliveryBuildRequest,
	requestDigest string,
	reservation NativeBuildOperationReservationResult,
	plan nativeBuildPlan,
	contract NativeBuildContract,
	artifacts release.CandidateArtifactSet,
	successor NativeBuildSuccessorAdmissionResult,
) (deploymentmodule.NativeDeliveryBuild, error) {
	if c == nil || c.repository == nil || c.connections == nil || c.managedData == nil || c.physicalFactory == nil || c.qualificationFactory == nil {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	if successor.Operation.AttemptID == "" || successor.Delivery.Successor.AttemptID != successor.Operation.AttemptID || successor.Artifact.AttemptID != successor.Operation.AttemptID {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor admission identities differ", deploymentdomain.ErrDeliveryConflict)
	}
	if reservation.Operation.OperationID == "" || reservation.Operation.State != deploymentmodule.NativeOperationStateIndeterminate {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor requires an indeterminate public operation", deploymentdomain.ErrDeliveryConflict)
	}
	if contract.PoolContract == nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor physical-pool contract is unavailable", deploymentnative.ErrInvalid)
	}
	candidateID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "candidate")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	generationID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "generation")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if successor.Delivery.Successor.CandidateID != candidateID || successor.Artifact.ServingStateID != generationID || successor.Delivery.Successor.PlanID != plan.ID || successor.Delivery.Successor.PlanDigest != plan.Digest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor candidate/generation identity differs", deploymentdomain.ErrDeliveryConflict)
	}

	bindingRequest := nativeCandidateConnectionRequest(candidateID, request.PrincipalID, request.TargetID, artifacts)
	bindingDigest, err := resolveNativeCandidateBindingDigest(ctx, c.bindingEvidence, bindingRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if bindingDigest != plan.Execution.BindingDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor candidate connection evidence differs from planned binding identity", deploymentdomain.ErrDeliveryConflict)
	}
	materializationRequest, managedDataLifetime, err := prepareNativeMaterializationRequest(ctx, c.managedData, artifacts, request, generationID, candidateID, successor.Delivery.Successor.Namespace, plan.DeliveryPlan)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	managedDataReleased := false
	releaseManagedData := func() error {
		if managedDataReleased || managedDataLifetime == nil {
			return nil
		}
		managedDataReleased = true
		return managedDataLifetime.Release()
	}
	defer func() { _ = releaseManagedData() }()

	physicalRoot, err := contract.PoolContract.Pool.DataPath()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildSuccessorFailure(ctx, successor, requestDigest, err, NativePhysicalFailureDeterministic, NativePhysicalBuildPhaseValidation, nil)
	}
	marker := nativeBuildMarker(reservation.Operation.OperationID, generationID, successor.Delivery.Successor.AttemptID, requestDigest, request, plan.Digest, c.physicalPoolID, successor.Delivery.Successor.FencingEpoch)
	marker.LeaseEpoch = successor.Delivery.Successor.FencingEpoch
	marker.FencingToken = fmt.Sprintf("%d", marker.LeaseEpoch)
	attemptAdmission := CandidateBuildAttemptAdmissionResult{
		Lease: successor.Delivery.SuccessorLease, Attempt: successor.Delivery.Successor,
		Artifact: successor.Artifact, DuckLakeAttempt: successor.DuckLake,
	}
	operationLease := successor.Operation.Lease
	buildCtx, buildCancel := context.WithCancel(ctx)
	heartbeatGuard := newNativeBuildHeartbeatGuard(ctx, c.heartbeat, c.heartbeatInterval, NativeBuildHeartbeatInput{
		OperationLease: operationLease,
		TargetLease:    deploymentnative.LeaseFence{LeaseID: attemptAdmission.Lease.LeaseID, TargetID: attemptAdmission.Lease.TargetID, OwnerID: attemptAdmission.Lease.OwnerID, FencingEpoch: attemptAdmission.Lease.FencingEpoch},
		AttemptID:      attemptAdmission.Attempt.AttemptID, AttemptOwnerID: attemptAdmission.Attempt.OwnerID, AttemptFencingEpoch: attemptAdmission.Attempt.FencingEpoch, Duration: c.leaseDuration,
	}, buildCancel)
	heartbeatStopped := false
	stopHeartbeat := func() (deploymentmodule.NativeOperationLease, error) {
		if heartbeatStopped {
			return operationLease, nil
		}
		heartbeatStopped = true
		latestInput, stopErr := heartbeatGuard.Stop()
		if latestInput.OperationLease.OperationID != "" {
			operationLease = latestInput.OperationLease
			successor.Operation.Lease = operationLease
		}
		if latestInput.TargetLease.LeaseID != "" {
			attemptAdmission.Lease.ExpiresAt = operationLease.LeaseExpiresAt
			attemptAdmission.Attempt.LeaseExpiresAt = operationLease.LeaseExpiresAt
			successor.Delivery.SuccessorLease.ExpiresAt = operationLease.LeaseExpiresAt
			successor.Delivery.Successor.LeaseExpiresAt = operationLease.LeaseExpiresAt
		}
		return operationLease, stopErr
	}
	defer func() {
		if !heartbeatStopped {
			_, _ = stopHeartbeat()
		}
		buildCancel()
	}()
	settle := func(buildErr error, classification NativePhysicalFailureClassification, phase NativePhysicalBuildPhase, evidence *NativePhysicalBuildEvidence) (deploymentmodule.NativeDeliveryBuild, error) {
		latest, heartbeatErr := stopHeartbeat()
		if heartbeatErr != nil {
			buildErr = errors.Join(buildErr, heartbeatErr)
			classification = NativePhysicalFailureIndeterminate
		}
		_ = latest
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildSuccessorFailure(ctx, successor, requestDigest, buildErr, classification, phase, evidence)
	}

	materializationRequest.RelationNamespace = attemptAdmission.Attempt.Namespace
	physicalInput := NativePhysicalBuildInput{Attempt: attemptAdmission.Attempt, Marker: marker, CatalogID: contract.Catalog.CatalogID, ObjectRoot: physicalRoot, ObservationWriter: c.observationWriter, CaptureClock: c.clock, Request: materializationRequest}
	physicalContext := materialize.WithObservationBudget(buildCtx, materialize.ObservationBudget{MaxQueries: c.bounds.MaxQueries, MaxMillis: c.bounds.MaxMillis})
	physical, err := buildNativePhysicalWithCandidateBindings(physicalContext, c.connections, bindingRequest, plan.Execution.BindingDigest, physicalInput, c.physicalFactory)
	if releaseErr := releaseManagedData(); releaseErr != nil {
		err = nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, errors.Join(err, fmt.Errorf("release native successor managed-data roots: %w", releaseErr)))
	}
	if err != nil {
		classification, phase := NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseMaterialize
		if failure, ok := NativePhysicalBuildFailureOf(err); ok {
			classification, phase = failure.Classification, failure.Phase
		}
		var evidence *NativePhysicalBuildEvidence
		if physical.SnapshotID > 0 {
			evidence = &physical
		}
		return settle(err, classification, phase, evidence)
	}
	sources, models, err := nativeQualificationInputs(artifacts, physical.SourceObservations)
	if err != nil {
		return settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	qualification, err := QualifyNativeSnapshot(buildCtx, NativeQualificationRequest{Build: physical, CandidateID: candidateID, SourceDigest: plan.SourceDigest, BindingGeneration: plan.Execution.BindingDigest, RuntimeVersion: c.runtimeVersion, Compatibility: contract.Compatibility, Sources: sources, Models: models, Bounds: c.bounds, Now: c.clock().UTC()}, c.qualificationFactory)
	if err != nil {
		return settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	if _, heartbeatErr := stopHeartbeat(); heartbeatErr != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildSuccessorFailure(ctx, successor, requestDigest, heartbeatErr, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	sealID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "seal")
	if err != nil {
		return settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	assembled, err := AssembleNativeGenerationAdmissionInput(NativeSealEvidenceAssemblerInput{Build: physical, AttemptAdmission: attemptAdmission, PoolContract: contract.PoolContract, CatalogIdentity: contract.Catalog, Compatibility: contract.Compatibility, Plan: plan.DeliveryPlan, Artifacts: artifacts, RuntimeVersion: c.runtimeVersion, Qualification: qualification, SealID: sealID, GenerationID: generationID, TenantDomain: contract.TenantDomain, EncryptionDomain: contract.EncryptionDomain, ObjectNamespace: contract.ObjectNamespace})
	if err != nil {
		return settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	return c.completeNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan.DeliveryPlan, assembled, artifacts, attemptAdmission, successor, physical, sealID, generationID, operationLease)
}

func (c *NativeBuildCoordinator) completeNativeBuildSuccessor(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, reservation NativeBuildOperationReservationResult, plan deploymentdomain.DeliveryPlan, assembled GenerationAdmissionInput, artifacts release.CandidateArtifactSet, admission CandidateBuildAttemptAdmissionResult, successor NativeBuildSuccessorAdmissionResult, physical NativePhysicalBuildEvidence, sealID, generationID string, operationLease deploymentmodule.NativeOperationLease) (deploymentmodule.NativeDeliveryBuild, error) {
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
	lockedOperation, err := lockNativeBuildOperationTx(ctx, tx, c.operations, reservation.Operation, deploymentmodule.NativeOperationStateIndeterminate)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	_ = lockedOperation
	leafAuthority, ok := c.operations.(deploymentmodule.NativeBuildOperationSuccessorLockAuthority)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor operation leaf lock authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	lockedLeaf, err := leafAuthority.LockSuccessorAttemptTx(ctx, tx, operationLease)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if lockedLeaf.AttemptID != admission.Attempt.AttemptID || lockedLeaf.AttemptIdentity != successor.Operation.AttemptIdentity || lockedLeaf.State != deploymentmodule.NativeOperationStatePending {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor operation leaf is not pending", deploymentdomain.ErrDeliveryConflict)
	}
	lockedLease, err := lockNativeBuildLeaseTx(ctx, tx, c.repository, admission.Lease, "active")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if !lockedLease.ExpiresAt.Equal(operationLease.LeaseExpiresAt) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor operation and target lease deadlines differ", deploymentdomain.ErrDeliveryConflict)
	}
	generation, err := c.generationAdmission.CompleteBuildAndAdmitTx(ctx, tx, assembled)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	eventID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "event")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	auditID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "audit")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	payload, err := json.Marshal(nativeBuildEventPayload{OperationID: reservation.Operation.OperationID, ProjectID: request.ProjectID.String(), ResourceID: generationID, Status: "sealed"})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	event, err := c.events.AppendDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{EventID: eventID, ScopeID: request.TargetID, AggregateType: "delivery_build", AggregateID: reservation.Operation.OperationID, EventType: "delivery.build.sealed", SchemaVersion: 1, CorrelationID: reservation.Operation.OperationID, Payload: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if event.EventID != eventID || event.AggregateVersion <= 0 || !sameNativeJSON(event.Payload, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor build event identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{AuditID: auditID, DomainEventID: eventID, ScopeID: request.TargetID, ActorID: request.PrincipalID, Action: "delivery.build.sealed", ResourceKind: "build", ResourceID: reservation.Operation.OperationID, Outcome: "accepted", Operation: "build", RequestDigest: requestDigest, CorrelationID: reservation.Operation.OperationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if audit.AuditID != auditID || audit.EventID != eventID || !sameNativeJSON(audit.Metadata, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor build audit identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if c.workflow != nil {
		if err := c.workflow.RecordWorkflow(ctx, tx, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "delivery.build.sealed/" + reservation.Operation.OperationID, ResourceKind: "build", ResourceID: reservation.Operation.OperationID, EventType: "delivery.build.sealed", Data: payload}}); err != nil {
			return deploymentmodule.NativeDeliveryBuild{}, err
		}
	}
	predecessorDepth, err := nativeBuildSuccessorDepth(reservation.Operation.OperationID, successor.Operation.PredecessorID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	successorDepth := predecessorDepth + 1
	if successorDepth <= 0 || successorDepth > maxNativeBuildSuccessorDepth {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor depth is outside the bounded chain", deploymentmodule.ErrNativeOperationConflict)
	}
	outcome := nativeBuildOutcome{OperationID: reservation.Operation.OperationID, OperationOwnerID: reservation.Operation.OwnerID, PlanID: plan.ID, CandidateID: admission.Attempt.CandidateID, AttemptID: admission.Attempt.AttemptID, LeaseID: admission.Lease.LeaseID, AttemptIdentity: successor.Operation.AttemptIdentity, PredecessorAttemptID: successor.Operation.PredecessorID, SuccessorDepth: successorDepth, GenerationID: generationID, SealID: sealID, EventID: eventID, AuditID: auditID, ServingArtifactID: artifacts.Generation.ServingArtifactID, ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment, ActorID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, QualificationDigest: assembled.QualificationDigest, ServingArtifactDigest: artifacts.Generation.ArtifactDigest, Status: "sealed"}
	outcomeJSON, err := encodeNativeBuildOutcome(outcome, request, deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: reservation.Operation.OwnerID})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	canonicalMarker, err := physical.Marker.CanonicalJSON()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	resolutionJSON, err := json.Marshal(nativeBuildSuccessorResolutionEvidence{SchemaVersion: 1, OperationID: reservation.Operation.OperationID, PredecessorID: reservation.Operation.AttemptID, AttemptID: admission.Attempt.AttemptID, AttemptIdentity: successor.Operation.AttemptIdentity, GenerationID: generationID, SealID: sealID, CandidateID: admission.Attempt.CandidateID, RequestDigest: requestDigest, PlanDigest: plan.Digest, PhysicalPoolID: physical.Marker.PhysicalPoolID, CatalogID: physical.CatalogID, SnapshotID: physical.SnapshotID, FencingEpoch: admission.Attempt.FencingEpoch, CommitMarker: json.RawMessage([]byte(canonicalMarker))})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	reconciled, err := c.operations.ReconcileAttemptTx(ctx, tx, deploymentmodule.NativeOperationReconcileAttemptInput{Scope: request.TargetID, IdempotencyKey: request.IdempotencyKey, AttemptID: admission.Attempt.AttemptID, AttemptIdentity: successor.Operation.AttemptIdentity, State: deploymentmodule.NativeOperationStateCompleted, Outcome: outcomeJSON, Evidence: resolutionJSON})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if reconciled.Operation.State != deploymentmodule.NativeOperationStateCompleted || reconciled.Operation.AttemptID != admission.Attempt.AttemptID || reconciled.Operation.AttemptIdentity != successor.Operation.AttemptIdentity || !sameNativeJSON(reconciled.Operation.Outcome, outcomeJSON) || !sameNativeJSON(reconciled.Operation.ResolutionEvidence, resolutionJSON) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor operation reconciliation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	completedAttempt, err := c.repository.BuildAttemptTx(ctx, tx, admission.Attempt.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if completedAttempt.State != deploymentnative.AttemptCommitted || completedAttempt.AttemptID != admission.Attempt.AttemptID || completedAttempt.SnapshotID != assembled.Commit.SnapshotID || !sameCommitMarker(completedAttempt.CommitMarker, assembled.Commit.CommitMarker) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor completed attempt evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed = true
	return nativeBuildProjection(outcome, plan.BaseGenerationID, completedAttempt, lockedLease, generation.CandidateRevision)
}

func (c *NativeBuildCoordinator) settleNativeBuildSuccessorFailure(ctx context.Context, successor NativeBuildSuccessorAdmissionResult, requestDigest string, buildErr error, classification NativePhysicalFailureClassification, phase NativePhysicalBuildPhase, physical *NativePhysicalBuildEvidence) error {
	if c == nil || c.repository == nil || c.attemptTermination == nil {
		return errors.Join(buildErr, deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if classification != NativePhysicalFailureDeterministic && classification != NativePhysicalFailureIndeterminate {
		classification = NativePhysicalFailureIndeterminate
	}
	hash := sha256.Sum256([]byte(errorString(buildErr)))
	evidence := nativeBuildTerminationEvidence{SchemaVersion: 1, AttemptID: successor.Delivery.Successor.AttemptID, OwnerID: successor.Delivery.Successor.OwnerID, FencingEpoch: successor.Delivery.Successor.FencingEpoch, RequestDigest: requestDigest, PlanDigest: successor.Delivery.Successor.PlanDigest, PhysicalPoolID: successor.Delivery.Successor.PhysicalPoolID, Namespace: successor.Delivery.Successor.Namespace, SessionIdentity: successor.Delivery.Successor.SessionIdentity, Phase: phase, Classification: classification, ErrorDigest: "sha256:" + hex.EncodeToString(hash[:])}
	if physical != nil && physical.SnapshotID > 0 {
		evidence.SnapshotID = physical.SnapshotID
		marker, markerErr := physical.Marker.CanonicalJSON()
		if markerErr == nil {
			evidence.CommitMarker = json.RawMessage([]byte(marker))
		}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), nativeBuildSettlementTimeout)
	defer cancel()
	tx, err := c.repository.Begin(cleanupCtx)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	// Preserve the global lock order (public operation -> target lease ->
	// delivery attempt -> DuckLake) before terminalizing either attempt ledger.
	lockedOperation, found, lockErr := c.operations.LockOperationTx(cleanupCtx, tx, deploymentmodule.NativeOperationAcquireInput{
		Scope: successor.Operation.Lease.Scope, OperationType: nativeBuildOperationType,
		IdempotencyKey: successor.Operation.Lease.IdempotencyKey, RequestDigest: requestDigest,
		OwnerID: successor.Operation.Lease.OwnerID,
	})
	if lockErr != nil {
		return errors.Join(buildErr, lockErr)
	}
	if !found || lockedOperation.OperationID != successor.Operation.Lease.OperationID || lockedOperation.State != deploymentmodule.NativeOperationStateIndeterminate {
		return errors.Join(buildErr, fmt.Errorf("%w: successor settlement public operation is not indeterminate", deploymentdomain.ErrDeliveryConflict))
	}
	leafAuthority, ok := c.operations.(deploymentmodule.NativeBuildOperationSuccessorLockAuthority)
	if !ok {
		return errors.Join(buildErr, fmt.Errorf("%w: successor operation leaf lock authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable))
	}
	lockedLeaf, leafErr := leafAuthority.LockSuccessorAttemptTx(cleanupCtx, tx, successor.Operation.Lease)
	if leafErr != nil {
		return errors.Join(buildErr, leafErr)
	}
	if lockedLeaf.AttemptID != successor.Delivery.Successor.AttemptID || (lockedLeaf.State != deploymentmodule.NativeOperationStatePending && lockedLeaf.State != deploymentmodule.NativeOperationStateIndeterminate) {
		return errors.Join(buildErr, fmt.Errorf("%w: successor settlement operation leaf is not recoverable", deploymentdomain.ErrDeliveryConflict))
	}
	// Acquire the target lease before touching delivery/DuckLake attempts. This
	// keeps settlement in the same public operation -> successor leaf -> target
	// lease -> delivery attempt -> DuckLake order as completion and heartbeat.
	lockedTarget, targetErr := lockNativeBuildLeaseTx(cleanupCtx, tx, c.repository, successor.Delivery.SuccessorLease, "active", "released")
	if targetErr != nil {
		return errors.Join(buildErr, targetErr)
	}
	terminationInput := AttemptTerminationInput{AttemptID: successor.Delivery.Successor.AttemptID, OwnerID: successor.Delivery.Successor.OwnerID, FencingEpoch: successor.Delivery.Successor.FencingEpoch, Evidence: evidenceJSON}
	if classification == NativePhysicalFailureDeterministic {
		_, err = c.attemptTermination.AbortAttemptTx(cleanupCtx, tx, terminationInput)
	} else {
		_, err = c.attemptTermination.MarkAttemptIndeterminateTx(cleanupCtx, tx, terminationInput)
	}
	if err == nil {
		opLease := successor.Operation.Lease
		if classification == NativePhysicalFailureDeterministic {
			err = c.operations.FailTx(cleanupCtx, tx, opLease, evidenceJSON)
		} else {
			err = c.operations.MarkIndeterminateTx(cleanupCtx, tx, opLease, evidenceJSON)
		}
	}
	if err == nil {
		err = c.repository.ReleaseLeaseAfterAttemptTerminationTx(cleanupCtx, tx, deploymentnative.LeaseFence{LeaseID: lockedTarget.LeaseID, TargetID: lockedTarget.TargetID, OwnerID: lockedTarget.OwnerID, FencingEpoch: lockedTarget.FencingEpoch})
	}
	if err == nil {
		err = tx.Commit(cleanupCtx)
	}
	if err != nil {
		return errors.Join(buildErr, err)
	}
	committed = true
	return buildErr
}
