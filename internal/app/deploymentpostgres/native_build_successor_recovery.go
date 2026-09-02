package deploymentpostgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/release"
)

// recoverNativeBuildSuccessor resolves the current operation-chain tip. It
// never treats a successor as a fresh root: the exact leaf delivery/DuckLake
// rows are loaded, normalized, and marker-resolved before either finalizing
// that leaf or appending its deterministic child.
func (c *NativeBuildCoordinator) recoverNativeBuildSuccessor(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, reservation NativeBuildOperationReservationResult, plan nativeBuildPlan, contract NativeBuildContract, successorAuthority deploymentmodule.NativeBuildOperationSuccessorAuthority) (deploymentmodule.NativeDeliveryBuild, error) {
	leafAuthority, ok := successorAuthority.(deploymentmodule.NativeBuildOperationSuccessorLockAuthority)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor leaf lock authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	leaf, found, err := successorAuthority.CurrentSuccessorAttempt(ctx, reservation.Operation.OperationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if !found {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor leaf disappeared during recovery", deploymentmodule.ErrNativeOperationConflict)
	}
	if leaf.State != deploymentmodule.NativeOperationStatePending && leaf.State != deploymentmodule.NativeOperationStateIndeterminate {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor leaf state %q is not recoverable", deploymentmodule.ErrNativeOperationConflict, leaf.State)
	}
	if leaf.PredecessorID == "" || leaf.PredecessorIdentity == "" {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor predecessor identity is missing", deploymentmodule.ErrNativeOperationConflict)
	}
	candidateID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "candidate")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	generationID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "generation")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	rootDeliveryAttemptID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "attempt")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	rootDeliveryLeaseID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "lease")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	// Root the current leaf depth in the operation-derived attempt chain. This
	// prevents a forged predecessor UUID from selecting an arbitrary delivery
	// fencing epoch while still allowing any bounded number of successors.
	predecessorDepth, err := nativeBuildSuccessorDepth(reservation.Operation.OperationID, leaf.PredecessorID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	leafDepth := predecessorDepth + 1
	if leafDepth <= 0 || leafDepth > maxNativeBuildSuccessorDepth {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor depth is outside the bounded chain", deploymentmodule.ErrNativeOperationConflict)
	}
	leaseID, err := nativeBuildSuccessorID(leaf.PredecessorID, "lease")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	rootDeliveryAttempt, err := c.repository.LoadBuildAttempt(ctx, rootDeliveryAttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	rootDeliveryLease, err := c.repository.Lease(ctx, rootDeliveryLeaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	attempt, err := c.repository.LoadBuildAttempt(ctx, leaf.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	lease, err := c.repository.Lease(ctx, leaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	binding, err := c.repository.LoadBuildArtifactBinding(ctx, leaf.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	candidate, err := c.repository.LoadCandidate(ctx, candidateID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	duckReader, ok := c.attemptAdmission.(CandidateBuildAttemptSuccessorDuckLakeReader)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor DuckLake reader is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	duckAttempt, err := duckReader.LoadSuccessorDuckLakeAttempt(ctx, leaf.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if reservation.Operation.AttemptID != rootDeliveryAttemptID || reservation.Operation.AttemptIdentity != "native-build/"+reservation.Operation.OperationID || rootDeliveryAttempt.AttemptID != rootDeliveryAttemptID || rootDeliveryAttempt.PlanID != plan.ID || rootDeliveryAttempt.CandidateID != candidateID || rootDeliveryAttempt.OwnerID != request.PrincipalID || rootDeliveryAttempt.PhysicalPoolID != c.physicalPoolID || rootDeliveryAttempt.RequestDigest != requestDigest || rootDeliveryAttempt.PlanDigest != plan.Digest || rootDeliveryAttempt.State != deploymentnative.AttemptIndeterminate || rootDeliveryAttempt.FencingEpoch <= 0 || rootDeliveryLease.LeaseID != rootDeliveryLeaseID || rootDeliveryLease.TargetID != request.TargetID || rootDeliveryLease.OwnerID != rootDeliveryAttempt.OwnerID || rootDeliveryLease.FencingEpoch != rootDeliveryAttempt.FencingEpoch || rootDeliveryLease.State != "released" || !rootDeliveryAttempt.LeaseExpiresAt.Equal(rootDeliveryLease.ExpiresAt) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor root delivery identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if err := validateNativeBuildSuccessorFenceChain(reservation.Operation.FencingGeneration, rootDeliveryAttempt.FencingEpoch, predecessorDepth, leaf.Lease.FencingGeneration, attempt.FencingEpoch); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if attempt.AttemptID != leaf.AttemptID || attempt.PlanID != plan.ID || attempt.CandidateID != candidateID || attempt.OwnerID != request.PrincipalID || attempt.PhysicalPoolID != c.physicalPoolID || attempt.RequestDigest != requestDigest || attempt.PlanDigest != plan.Digest || attempt.Namespace == "" || attempt.SessionIdentity == "" || !attempt.LeaseExpiresAt.Equal(lease.ExpiresAt) || lease.OwnerID != attempt.OwnerID || lease.FencingEpoch != attempt.FencingEpoch || lease.TargetID != request.TargetID || candidate.CandidateID != candidateID || binding.AttemptID != leaf.AttemptID || duckAttempt.AttemptID != leaf.AttemptID || duckAttempt.OwnerID != attempt.OwnerID || duckAttempt.FencingEpoch != attempt.FencingEpoch {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor leaf delivery/DuckLake identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	persistedPlan, err := c.repository.Plan(ctx, plan.ID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	prepared := NativeBuildRecoveryPreparationResult{Operation: reservation.Operation, Plan: persistedPlan, Candidate: candidate, Artifact: binding, DeliveryAttempt: attempt, DuckLakeAttempt: duckAttempt, Lease: lease, CandidateID: candidateID, GenerationID: generationID, AttemptID: attempt.AttemptID, LeaseID: lease.LeaseID}
	prepared.Operation.AttemptID = leaf.AttemptID
	prepared.Operation.AttemptIdentity = leaf.AttemptIdentity
	prepared.Operation.FencingGeneration = leaf.Lease.FencingGeneration
	prepared.Operation.LeaseExpiresAt = leaf.Lease.LeaseExpiresAt
	prepared.Operation.AttemptEvidence = append(json.RawMessage(nil), leaf.AttemptEvidence...)
	if err := normalizeSuccessorLeafForRecovery(ctx, c, leafAuthority, prepared, reservation.Operation, requestDigest); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	// Reload normalized ledgers after the transaction so marker resolution sees
	// exactly the released indeterminate attempt identity.
	attempt, err = c.repository.LoadBuildAttempt(ctx, leaf.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	lease, err = c.repository.Lease(ctx, leaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	prepared.DeliveryAttempt, prepared.Lease = attempt, lease
	latestLeaf, latestFound, latestErr := successorAuthority.CurrentSuccessorAttempt(ctx, reservation.Operation.OperationID)
	if latestErr != nil || !latestFound {
		if latestErr == nil {
			latestErr = deploymentmodule.ErrNativeOperationConflict
		}
		return deploymentmodule.NativeDeliveryBuild{}, latestErr
	}
	if latestLeaf.AttemptID != leaf.AttemptID || latestLeaf.AttemptIdentity != leaf.AttemptIdentity || latestLeaf.PredecessorID != leaf.PredecessorID || latestLeaf.PredecessorIdentity != leaf.PredecessorIdentity || latestLeaf.State != deploymentmodule.NativeOperationStateIndeterminate {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor leaf changed during recovery normalization", deploymentmodule.ErrNativeOperationConflict)
	}
	prepared.Operation.AttemptEvidence = append(json.RawMessage(nil), latestLeaf.AttemptEvidence...)
	duckAttempt, err = duckReader.LoadSuccessorDuckLakeAttempt(ctx, leaf.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	prepared.DuckLakeAttempt = duckAttempt
	if prepared.Operation.FencingGeneration < 1 {
		prepared.Operation.FencingGeneration = leaf.Lease.FencingGeneration
	}
	artifactRequest, artifactBinding, marker, err := deriveNativeBuildRecoveryArtifactValues(request, requestDigest, plan, prepared, c.physicalPoolID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	artifacts, err := c.artifactRecovery.RecoverCandidateArtifacts(ctx, artifactRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := validateNativeBuildArtifacts(artifacts, request, plan.DeliveryPlan); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if artifacts.Generation.Identity.GenerationID != generationID || artifacts.Generation.ArtifactDigest != plan.ArtifactDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered artifact identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	physicalInput, err := deriveNativeBuildRecoveryPhysicalInput(request, plan, prepared, contract, artifacts, marker, c.markerResolverFactory, c.markerQuarantine, c.observationReader, c.snapshotFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	physical, err := RecoverNativePhysicalBuild(ctx, physicalInput)
	if err != nil {
		if errors.Is(err, ErrNativePhysicalMarkerAbsent) {
			resolution, marshalErr := json.Marshal(deploymentnative.BuildAttemptMarkerResolutionEvidence{SchemaVersion: 1, PhysicalPoolID: prepared.DeliveryAttempt.PhysicalPoolID, CatalogID: contract.Catalog.CatalogID, AttemptID: prepared.DeliveryAttempt.AttemptID, RequestDigest: prepared.DeliveryAttempt.RequestDigest, PlanDigest: prepared.DeliveryAttempt.PlanDigest, MarkerAbsent: true, ResolvedAt: c.clock().UTC()})
			if marshalErr != nil {
				return deploymentmodule.NativeDeliveryBuild{}, marshalErr
			}
			return c.appendNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan, contract, artifacts, prepared, artifactBinding, resolution)
		}
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	return c.completeRecoveredNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan, contract, artifacts, prepared, artifactBinding, physical, leaf)
}

func normalizeSuccessorLeafForRecovery(ctx context.Context, c *NativeBuildCoordinator, leafAuthority deploymentmodule.NativeBuildOperationSuccessorLockAuthority, prepared NativeBuildRecoveryPreparationResult, root deploymentmodule.NativeOperationRecord, requestDigest string) error {
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if _, err := lockNativeBuildOperationTx(ctx, tx, c.operations, root, deploymentmodule.NativeOperationStateIndeterminate); err != nil {
		return err
	}
	leafLease := deploymentmodule.NativeOperationLease{Scope: prepared.Operation.Scope, IdempotencyKey: prepared.Operation.IdempotencyKey, OperationID: prepared.Operation.OperationID, OwnerID: prepared.Operation.OwnerID, FencingGeneration: prepared.Operation.FencingGeneration, LeaseExpiresAt: prepared.Operation.LeaseExpiresAt, AttemptID: prepared.Operation.AttemptID, AttemptIdentity: prepared.Operation.AttemptIdentity}
	leaf, err := leafAuthority.LockSuccessorAttemptTx(ctx, tx, leafLease)
	if err != nil {
		return fmt.Errorf("lock successor operation leaf: %w", err)
	}
	evidence := append(json.RawMessage(nil), leaf.AttemptEvidence...)
	if len(evidence) == 0 {
		hash := sha256.Sum256([]byte("leapview/native-build-successor-recovery/" + prepared.AttemptID))
		evidence, err = json.Marshal(nativeBuildTerminationEvidence{SchemaVersion: 1, AttemptID: prepared.AttemptID, OwnerID: prepared.DeliveryAttempt.OwnerID, FencingEpoch: prepared.DeliveryAttempt.FencingEpoch, RequestDigest: requestDigest, PlanDigest: prepared.DeliveryAttempt.PlanDigest, PhysicalPoolID: prepared.DeliveryAttempt.PhysicalPoolID, Namespace: prepared.DeliveryAttempt.Namespace, SessionIdentity: prepared.DeliveryAttempt.SessionIdentity, Phase: NativePhysicalBuildPhaseEvidence, Classification: NativePhysicalFailureIndeterminate, ErrorDigest: "sha256:" + hex.EncodeToString(hash[:])})
		if err != nil {
			return err
		}
	}
	if leaf.State == deploymentmodule.NativeOperationStatePending {
		opLease := leaf.Lease
		// Do not fence a still-active successor.  ExpireAttemptTx delegates
		// expiry comparison to the operation authority's transaction clock; an
		// unexpired lease therefore returns a conflict and leaves every ledger
		// untouched.  Only an actually expired pending leaf is normalized here.
		if err := c.operations.ExpireAttemptTx(ctx, tx, opLease, evidence); err != nil {
			return fmt.Errorf("%w: successor leaf remains active or could not be expired: %v", deploymentmodule.ErrNativeOperationBusy, err)
		}
	}
	target, err := lockNativeBuildLeaseTx(ctx, tx, c.repository, prepared.Lease, "active", "released")
	if err != nil {
		return err
	}
	if prepared.DeliveryAttempt.State == deploymentnative.AttemptRunning {
		termination, termErr := c.attemptTermination.MarkAttemptIndeterminateTx(ctx, tx, AttemptTerminationInput{AttemptID: prepared.DeliveryAttempt.AttemptID, OwnerID: prepared.DeliveryAttempt.OwnerID, FencingEpoch: prepared.DeliveryAttempt.FencingEpoch, Evidence: evidence})
		if termErr != nil {
			return termErr
		}
		prepared.DuckLakeAttempt = termination.DuckLakeAttempt
		prepared.DeliveryAttempt = termination.DeliveryAttempt
	}
	if prepared.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate {
		return fmt.Errorf("%w: successor delivery attempt did not normalize", deploymentnative.ErrConflict)
	}
	if target.State == "active" {
		if err := c.repository.ReleaseLeaseAfterAttemptTerminationTx(ctx, tx, deploymentnative.LeaseFence{LeaseID: target.LeaseID, TargetID: target.TargetID, OwnerID: target.OwnerID, FencingEpoch: target.FencingEpoch}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (c *NativeBuildCoordinator) appendNativeBuildSuccessor(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, reservation NativeBuildOperationReservationResult, plan nativeBuildPlan, contract NativeBuildContract, artifacts release.CandidateArtifactSet, prepared NativeBuildRecoveryPreparationResult, binding deploymentnative.BuildArtifactBinding, resolution []byte) (deploymentmodule.NativeDeliveryBuild, error) {
	successorAuthority := c.operations.(deploymentmodule.NativeBuildOperationSuccessorAuthority)
	duckLake, ok := c.attemptAdmission.(CandidateBuildAttemptSuccessorDuckLakeAdmission)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor DuckLake authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	op := prepared.Operation
	op.State = deploymentmodule.NativeOperationStateIndeterminate
	successor, err := AdmitNativeBuildSuccessor(ctx, c.repository, successorAuthority, NativeBuildSuccessorAdmissionInput{Operation: op, DeliveryAttempt: prepared.DeliveryAttempt, DeliveryLease: prepared.Lease, Artifact: CandidateBuildArtifactInput{ServingArtifactID: binding.ServingArtifactID, ServingArtifactDigest: binding.ServingArtifactDigest, ServingStateID: binding.ServingStateID}, CatalogID: contract.Catalog.CatalogID, Resolution: resolution, DuckLake: duckLake})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	return c.executeNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan, contract, artifacts, successor)
}

func (c *NativeBuildCoordinator) completeRecoveredNativeBuildSuccessor(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, reservation NativeBuildOperationReservationResult, plan nativeBuildPlan, contract NativeBuildContract, artifacts release.CandidateArtifactSet, prepared NativeBuildRecoveryPreparationResult, binding deploymentnative.BuildArtifactBinding, physical NativePhysicalBuildEvidence, leaf deploymentmodule.NativeOperationSuccessor) (deploymentmodule.NativeDeliveryBuild, error) {
	sources, models, err := nativeQualificationInputs(artifacts, physical.SourceObservations)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	qualification, err := QualifyNativeSnapshot(ctx, NativeQualificationRequest{Build: physical, CandidateID: prepared.CandidateID, SourceDigest: plan.SourceDigest, BindingGeneration: plan.Execution.BindingDigest, RuntimeVersion: c.runtimeVersion, Compatibility: contract.Compatibility, Sources: sources, Models: models, Bounds: c.bounds, Now: c.clock().UTC()}, c.qualificationFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	bindingRequest := nativeCandidateConnectionRequest(prepared.CandidateID, request.PrincipalID, request.TargetID, artifacts)
	bindingEvidence, bindingDigest, err := resolveNativeCandidateBindingEvidence(ctx, c.bindingEvidence, bindingRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if bindingDigest != plan.Execution.BindingDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered successor candidate connection evidence differs from planned binding identity", deploymentdomain.ErrDeliveryConflict)
	}
	sourceRevision, err := c.nativeSourceRevision(ctx, plan, request)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	sealID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "seal")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	attemptAdmission := CandidateBuildAttemptAdmissionResult{Lease: prepared.Lease, Attempt: prepared.DeliveryAttempt, Artifact: binding, DuckLakeAttempt: prepared.DuckLakeAttempt}
	assembled, err := AssembleRecoveredNativeGenerationAdmissionInput(NativeRecoveredSealEvidenceAssemblerInput{Build: physical, AttemptAdmission: attemptAdmission, PoolContract: contract.PoolContract, CatalogIdentity: contract.Catalog, Compatibility: contract.Compatibility, Plan: plan.DeliveryPlan, Artifacts: artifacts, Bindings: bindingEvidence, SourceRevision: sourceRevision, RuntimeVersion: c.runtimeVersion, Qualification: qualification, SealID: sealID, GenerationID: prepared.GenerationID, TenantDomain: contract.TenantDomain, EncryptionDomain: contract.EncryptionDomain, ObjectNamespace: contract.ObjectNamespace})
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
	if _, err := lockNativeBuildOperationTx(ctx, tx, c.operations, reservation.Operation, deploymentmodule.NativeOperationStateIndeterminate); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	leafAuthority, ok := c.operations.(deploymentmodule.NativeBuildOperationSuccessorLockAuthority)
	if !ok {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor operation leaf lock authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	leafLease := deploymentmodule.NativeOperationLease{Scope: reservation.Operation.Scope, IdempotencyKey: reservation.Operation.IdempotencyKey, OperationID: reservation.Operation.OperationID, OwnerID: reservation.Operation.OwnerID, FencingGeneration: leaf.Lease.FencingGeneration, LeaseExpiresAt: leaf.Lease.LeaseExpiresAt, AttemptID: leaf.AttemptID, AttemptIdentity: leaf.AttemptIdentity}
	lockedLeaf, err := leafAuthority.LockSuccessorAttemptTx(ctx, tx, leafLease)
	if err != nil || lockedLeaf.State != deploymentmodule.NativeOperationStateIndeterminate {
		if err == nil {
			err = deploymentmodule.ErrNativeOperationConflict
		}
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	lockedLease, err := lockNativeBuildLeaseTx(ctx, tx, c.repository, prepared.Lease, "released", "active")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	canonicalMarker, err := physical.Marker.CanonicalJSON()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	termination, err := c.attemptTermination.ReconcileAttemptTx(ctx, tx, AttemptReconciliationInput{AttemptID: prepared.AttemptID, OwnerID: prepared.DeliveryAttempt.OwnerID, FencingEpoch: prepared.DeliveryAttempt.FencingEpoch, PhysicalPoolID: physical.Marker.PhysicalPoolID, CatalogID: physical.CatalogID, SnapshotID: physical.SnapshotID, CommitMarker: json.RawMessage([]byte(canonicalMarker)), State: deploymentnative.AttemptCommitted, SessionIdentity: prepared.DeliveryAttempt.SessionIdentity})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if termination.DeliveryAttempt.State != deploymentnative.AttemptCommitted || termination.DuckLakeAttempt.State != "committed" {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered attempt reconciliation differs", deploymentdomain.ErrDeliveryConflict)
	}
	if err := c.repository.ReleaseLeaseAfterAttemptTerminationTx(ctx, tx, deploymentnative.LeaseFence{LeaseID: lockedLease.LeaseID, TargetID: lockedLease.TargetID, OwnerID: lockedLease.OwnerID, FencingEpoch: lockedLease.FencingEpoch}); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
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
	payload, err := json.Marshal(nativeBuildEventPayload{OperationID: reservation.Operation.OperationID, ProjectID: request.ProjectID.String(), ResourceID: prepared.GenerationID, Status: "sealed"})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	event, err := c.events.AppendDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{EventID: eventID, ScopeID: request.TargetID, AggregateType: "delivery_build", AggregateID: reservation.Operation.OperationID, EventType: "delivery.build.sealed", SchemaVersion: 1, CorrelationID: reservation.Operation.OperationID, Payload: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if event.EventID != eventID || event.AggregateVersion <= 0 || !sameNativeJSON(event.Payload, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered event identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{AuditID: auditID, DomainEventID: eventID, ScopeID: request.TargetID, ActorID: request.PrincipalID, Action: "delivery.build.sealed", ResourceKind: "build", ResourceID: reservation.Operation.OperationID, Outcome: "accepted", Operation: "build", RequestDigest: requestDigest, CorrelationID: reservation.Operation.OperationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if audit.AuditID != auditID || audit.EventID != eventID || !sameNativeJSON(audit.Metadata, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered audit identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	predecessorDepth, err := nativeBuildSuccessorDepth(reservation.Operation.OperationID, leaf.PredecessorID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	successorDepth := predecessorDepth + 1
	if successorDepth <= 0 || successorDepth > maxNativeBuildSuccessorDepth {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor depth is outside the bounded chain", deploymentmodule.ErrNativeOperationConflict)
	}
	outcome := nativeBuildOutcome{OperationID: reservation.Operation.OperationID, OperationOwnerID: reservation.Operation.OwnerID, PlanID: plan.ID, CandidateID: prepared.CandidateID, AttemptID: prepared.AttemptID, LeaseID: prepared.LeaseID, AttemptIdentity: leaf.AttemptIdentity, PredecessorAttemptID: leaf.PredecessorID, SuccessorDepth: successorDepth, GenerationID: prepared.GenerationID, SealID: sealID, EventID: eventID, AuditID: auditID, ServingArtifactID: artifacts.Generation.ServingArtifactID, ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment, ActorID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, QualificationDigest: assembled.QualificationDigest, ServingArtifactDigest: artifacts.Generation.ArtifactDigest, Status: "sealed"}
	outcomeJSON, err := encodeNativeBuildOutcome(outcome, request, deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: reservation.Operation.OwnerID})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	resolutionJSON, err := json.Marshal(nativeBuildSuccessorResolutionEvidence{SchemaVersion: 1, OperationID: reservation.Operation.OperationID, PredecessorID: leaf.PredecessorID, AttemptID: prepared.AttemptID, AttemptIdentity: leaf.AttemptIdentity, GenerationID: prepared.GenerationID, SealID: sealID, CandidateID: prepared.CandidateID, RequestDigest: requestDigest, PlanDigest: plan.Digest, PhysicalPoolID: physical.Marker.PhysicalPoolID, CatalogID: physical.CatalogID, SnapshotID: physical.SnapshotID, FencingEpoch: prepared.DeliveryAttempt.FencingEpoch, CommitMarker: json.RawMessage([]byte(canonicalMarker))})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	reconciled, err := c.operations.ReconcileAttemptTx(ctx, tx, deploymentmodule.NativeOperationReconcileAttemptInput{Scope: request.TargetID, IdempotencyKey: request.IdempotencyKey, AttemptID: prepared.AttemptID, AttemptIdentity: leaf.AttemptIdentity, State: deploymentmodule.NativeOperationStateCompleted, Outcome: outcomeJSON, Evidence: resolutionJSON})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if reconciled.Operation.State != deploymentmodule.NativeOperationStateCompleted {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered operation did not complete", deploymentdomain.ErrDeliveryConflict)
	}
	completedAttempt, err := c.repository.BuildAttemptTx(ctx, tx, prepared.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if completedAttempt.State != deploymentnative.AttemptCommitted {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: successor recovered delivery attempt did not commit", deploymentdomain.ErrDeliveryConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed = true
	lease, err := c.repository.Lease(ctx, prepared.LeaseID)
	if err != nil {
		lease = lockedLease
	}
	return nativeBuildProjection(outcome, plan.BaseGenerationID, completedAttempt, lease, generation.CandidateRevision)
}

// validateNativeBuildSuccessorFenceChain validates each append ledger against
// its own deterministic root. Target fencing is global to a target and may be
// ahead of operation fencing because unrelated writers acquired leases before
// this build; only the per-ledger depth increment must agree.
func validateNativeBuildSuccessorFenceChain(rootOperationFence, rootDeliveryFence int64, predecessorDepth int, leafOperationFence, leafDeliveryFence int64) error {
	if rootOperationFence <= 0 || rootDeliveryFence <= 0 || predecessorDepth < 0 || predecessorDepth >= maxNativeBuildSuccessorDepth {
		return fmt.Errorf("%w: successor fence chain root or depth is invalid", deploymentdomain.ErrDeliveryConflict)
	}
	leafDepth := int64(predecessorDepth) + 1
	maxInt64 := int64(^uint64(0) >> 1)
	if rootOperationFence > maxInt64-leafDepth || rootDeliveryFence > maxInt64-leafDepth {
		return fmt.Errorf("%w: successor fencing generation overflow", deploymentdomain.ErrDeliveryConflict)
	}
	expectedOperationFence := rootOperationFence + leafDepth
	expectedDeliveryFence := rootDeliveryFence + leafDepth
	if leafOperationFence != expectedOperationFence || leafDeliveryFence != expectedDeliveryFence {
		return fmt.Errorf("%w: successor operation or delivery fence differs from its rooted chain", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}
