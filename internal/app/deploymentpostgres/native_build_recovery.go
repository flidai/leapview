package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

// recoverIndeterminateNativeBuild is the recovery-only continuation for an
// operation whose external commit outcome was fenced indeterminate. It uses
// only immutable candidate/physical evidence and never starts a fresh source,
// artifact, physical-build, or heartbeat phase.
func (c *NativeBuildCoordinator) recoverIndeterminateNativeBuild(
	ctx context.Context,
	request deploymentmodule.NativeDeliveryBuildRequest,
	requestDigest string,
	reservation NativeBuildOperationReservationResult,
	plan nativeBuildPlan,
) (deploymentmodule.NativeDeliveryBuild, error) {
	if c == nil || c.repository == nil || c.artifactRecovery == nil || c.contract == nil || c.attemptTermination == nil || c.generationAdmission == nil {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	contract, err := c.contract.Resolve(ctx, NativeBuildContractRequest{PhysicalPoolID: c.physicalPoolID, CompatibilityDigest: c.compatibilityDigest})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if contract.PhysicalPoolID != c.physicalPoolID || contract.CompatibilityDigest != c.compatibilityDigest || contract.PoolContract == nil || contract.Catalog.CatalogID == "" {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: resolved native build recovery contract identity differs", deploymentnative.ErrConflict)
	}
	// A successor leaf is the executable recovery tip once admitted. The
	// root-only preparation path intentionally refuses to reinterpret that leaf
	// as the deterministic root attempt; a dedicated leaf marker-resolution
	// continuation must resolve it before appending another child. This guard
	// prevents a retry from blindly re-executing the root physical build.
	if successorAuthority, ok := c.operations.(deploymentmodule.NativeBuildOperationSuccessorAuthority); ok {
		if _, found, successorErr := successorAuthority.CurrentSuccessorAttempt(ctx, reservation.Operation.OperationID); successorErr != nil {
			return deploymentmodule.NativeDeliveryBuild{}, successorErr
		} else if found {
			return c.recoverNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan, contract, successorAuthority)
		}
	}
	prepared, err := PrepareNativeBuildRecovery(ctx, c.repository, c.operations, c.attemptTermination, NativeBuildRecoveryPreparationInput{
		Request: request, RequestDigest: requestDigest, Operation: reservation.Operation,
		PhysicalPoolID: c.physicalPoolID,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if prepared.Operation.OperationID != reservation.Operation.OperationID || prepared.Operation.State != deploymentmodule.NativeOperationStateIndeterminate {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: prepared recovery operation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if prepared.Plan.PlanID != plan.ID || prepared.Plan.PlanDigest != plan.Digest || prepared.Plan.ArtifactDigest != plan.ArtifactDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: prepared recovery plan identity differs", deploymentdomain.ErrDeliveryConflict)
	}

	artifactRequest, artifactBinding, marker, err := deriveNativeBuildRecoveryArtifactValues(request, requestDigest, plan, prepared, c.physicalPoolID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	artifacts, err := c.artifactRecovery.RecoverCandidateArtifacts(ctx, artifactRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("recover candidate artifacts: %w", err)
	}
	if err := validateNativeBuildArtifacts(artifacts, request, plan.DeliveryPlan); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if artifacts.Generation.Identity.GenerationID != prepared.GenerationID || artifacts.Generation.ArtifactDigest != plan.ArtifactDigest || artifacts.Generation.ServingArtifactID != artifactBinding.ServingArtifactID {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered candidate artifact identity differs", deploymentdomain.ErrDeliveryConflict)
	}

	attemptAdmission := CandidateBuildAttemptAdmissionResult{
		Lease: prepared.Lease, Attempt: prepared.DeliveryAttempt,
		Artifact: artifactBinding,
	}
	physicalInput, err := deriveNativeBuildRecoveryPhysicalInput(request, plan, prepared, contract, artifacts, marker, c.markerResolverFactory, c.markerQuarantine, c.observationReader, c.snapshotFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	physical, err := RecoverNativePhysicalBuild(ctx, physicalInput)
	if err != nil {
		if errors.Is(err, ErrNativePhysicalMarkerAbsent) {
			resolvedAt := time.Now().UTC()
			if c.clock != nil {
				if candidate := c.clock().UTC(); !candidate.IsZero() {
					resolvedAt = candidate
				}
			}
			resolution, marshalErr := json.Marshal(deploymentnative.BuildAttemptMarkerResolutionEvidence{
				SchemaVersion: 1, PhysicalPoolID: prepared.DeliveryAttempt.PhysicalPoolID, CatalogID: contract.Catalog.CatalogID,
				AttemptID: prepared.DeliveryAttempt.AttemptID, RequestDigest: prepared.DeliveryAttempt.RequestDigest,
				PlanDigest: prepared.DeliveryAttempt.PlanDigest, MarkerAbsent: true, ResolvedAt: resolvedAt,
			})
			if marshalErr != nil {
				return deploymentmodule.NativeDeliveryBuild{}, marshalErr
			}
			successorAuthority, authorityOK := c.operations.(deploymentmodule.NativeBuildOperationSuccessorAuthority)
			if !authorityOK {
				return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build successor operation authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
			}
			physicalAdmission, admissionOK := c.attemptAdmission.(CandidateBuildAttemptPhysicalAdmission)
			if !admissionOK {
				return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build successor physical admission guard is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
			}
			successor, admissionErr := AdmitNativeBuildSuccessor(ctx, c.repository, successorAuthority, NativeBuildSuccessorAdmissionInput{
				Operation: prepared.Operation, DeliveryAttempt: prepared.DeliveryAttempt, DeliveryLease: prepared.Lease,
				Artifact:  CandidateBuildArtifactInput{ServingArtifactID: artifactBinding.ServingArtifactID, ServingArtifactDigest: artifactBinding.ServingArtifactDigest, ServingStateID: artifactBinding.ServingStateID},
				CatalogID: contract.Catalog.CatalogID, Physical: physicalAdmission, Resolution: resolution,
			})
			if admissionErr != nil {
				return deploymentmodule.NativeDeliveryBuild{}, admissionErr
			}
			return c.executeNativeBuildSuccessor(ctx, request, requestDigest, reservation, plan, contract, artifacts, successor)
		}
		// Resolver, close, anomaly, snapshot, and evidence errors remain
		// unresolved and never authorize a successor.
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	sources, models, err := nativeQualificationInputs(artifacts, physical.SourceObservations)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	qualification, err := QualifyNativeSnapshot(ctx, NativeQualificationRequest{
		Build: physical, CandidateID: prepared.CandidateID, SourceDigest: plan.SourceDigest,
		BindingGeneration: plan.Execution.BindingDigest, RuntimeVersion: c.runtimeVersion,
		Compatibility: contract.Compatibility, Sources: sources, Models: models, Bounds: c.bounds, Now: c.clock().UTC(),
	}, c.qualificationFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	bindingRequest := nativeCandidateConnectionRequest(prepared.CandidateID, request.PrincipalID, request.TargetID, artifacts)
	bindingEvidence, bindingDigest, err := resolveNativeCandidateBindingEvidence(ctx, c.bindingEvidence, bindingRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if bindingDigest != plan.Execution.BindingDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: recovered candidate connection evidence differs from planned binding identity", deploymentdomain.ErrDeliveryConflict)
	}
	sourceRevision, err := c.nativeSourceRevision(ctx, plan, request)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	sealID, err := nativeBuildConsequenceID(prepared.Operation.OperationID, "seal")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	assembled, err := AssembleRecoveredNativeGenerationAdmissionInput(NativeRecoveredSealEvidenceAssemblerInput{
		Build: physical, AttemptAdmission: attemptAdmission, PoolContract: contract.PoolContract,
		CatalogIdentity: contract.Catalog, Compatibility: contract.Compatibility, Plan: plan.DeliveryPlan,
		Artifacts: artifacts, Bindings: bindingEvidence, SourceRevision: sourceRevision, RuntimeVersion: c.runtimeVersion, Qualification: qualification,
		SealID: sealID, GenerationID: prepared.GenerationID, TenantDomain: contract.TenantDomain,
		EncryptionDomain: contract.EncryptionDomain, ObjectNamespace: contract.ObjectNamespace,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	recoveryReservation := reservation
	recoveryReservation.Operation = prepared.Operation
	recoveryReservation.Lease = deploymentmodule.NativeOperationLease{}
	recoveryReservation.Disposition = deploymentmodule.NativeOperationIndeterminate
	return c.completeRecoveredNativeBuild(ctx, nativeBuildRecoveryFinalizationInput{
		Request: request, RequestDigest: requestDigest, Reservation: recoveryReservation,
		Plan: plan.DeliveryPlan, Assembled: assembled, Artifacts: artifacts,
		Admission: attemptAdmission, Physical: physical, SealID: sealID, GenerationID: prepared.GenerationID,
	})
}

// deriveNativeBuildRecoveryArtifactValues lowers durable preparation evidence
// into the exact immutable artifact request, binding projection, and marker
// used by the recovery path. It performs no I/O and synthesizes only a
// content-addressed binding when the first attempt transaction never wrote it.
func deriveNativeBuildRecoveryArtifactValues(
	request deploymentmodule.NativeDeliveryBuildRequest,
	requestDigest string,
	plan nativeBuildPlan,
	prepared NativeBuildRecoveryPreparationResult,
	physicalPoolID string,
) (release.CandidateArtifactRecoveryRequest, deploymentnative.BuildArtifactBinding, catalogartifact.CommitMarker, error) {
	managedPins, err := nativeRecoveryPlanManagedDataPins(plan.DeliveryPlan)
	if err != nil {
		return release.CandidateArtifactRecoveryRequest{}, deploymentnative.BuildArtifactBinding{}, catalogartifact.CommitMarker{}, err
	}
	artifactIdentity := release.CandidateArtifactIdentity{ServingArtifactDigest: plan.ArtifactDigest, ServingStateID: prepared.GenerationID}
	if prepared.Artifact.AttemptID == "" {
		artifactIdentity.ServingArtifactID = "artifact-" + strings.TrimPrefix(plan.ArtifactDigest, "sha256:")
	} else {
		artifactIdentity.ServingArtifactID = prepared.Artifact.ServingArtifactID
		artifactIdentity.ServingArtifactDigest = prepared.Artifact.ServingArtifactDigest
		artifactIdentity.ServingStateID = prepared.Artifact.ServingStateID
	}
	servingIdentity, err := projectgraph.NewServingIdentity(request.ProjectID, request.Environment, prepared.GenerationID)
	if err != nil {
		return release.CandidateArtifactRecoveryRequest{}, deploymentnative.BuildArtifactBinding{}, catalogartifact.CommitMarker{}, err
	}
	marker := nativeBuildMarker(prepared.Operation.OperationID, prepared.GenerationID, prepared.AttemptID, requestDigest, request, plan.Digest, physicalPoolID, prepared.DeliveryAttempt.FencingEpoch)
	marker.FencingToken = fmt.Sprintf("%d", prepared.DeliveryAttempt.FencingEpoch)
	binding := deploymentnative.BuildArtifactBinding{AttemptID: prepared.AttemptID, ServingArtifactID: artifactIdentity.ServingArtifactID, ServingArtifactDigest: artifactIdentity.ServingArtifactDigest, ServingStateID: artifactIdentity.ServingStateID}
	return release.CandidateArtifactRecoveryRequest{CandidateID: prepared.CandidateID, ServingIdentity: servingIdentity, SourceDigest: plan.SourceDigest, ManagedDataPins: managedPins, Artifact: artifactIdentity}, binding, marker, nil
}

// nativeRecoveryPlanManagedDataPins lowers the exact pinned managed-data
// inputs retained in the canonical plan. The source-artifact input is the
// project source identity rather than a managed-data revision and is omitted.
func nativeRecoveryPlanManagedDataPins(plan deploymentdomain.DeliveryPlan) ([]release.ManagedDataPin, error) {
	result := make([]release.ManagedDataPin, 0, len(plan.Execution.DataInputs))
	seen := make(map[string]struct{}, len(plan.Execution.DataInputs))
	for _, input := range plan.Execution.DataInputs {
		if strings.TrimSpace(input.ID) == "source-artifact" {
			continue
		}
		if input.Mode != deploymentdomain.DeliveryDataPinned {
			continue
		}
		if strings.TrimSpace(input.ID) != input.ID || strings.TrimSpace(input.Revision) != input.Revision || input.ID == "" || input.Revision == "" {
			return nil, fmt.Errorf("%w: native recovery managed-data plan input is not an exact pinned revision", deploymentdomain.ErrDeliveryConflict)
		}
		if _, exists := seen[input.ID]; exists {
			return nil, fmt.Errorf("%w: native recovery managed-data plan input is duplicated", deploymentdomain.ErrDeliveryConflict)
		}
		seen[input.ID] = struct{}{}
		result = append(result, release.ManagedDataPin{ConnectionID: input.ID, RevisionID: input.Revision})
	}
	return result, nil
}

// deriveNativeBuildRecoveryPhysicalInput constructs the read-only physical
// recovery request from exact contract and artifact evidence. No authority is
// opened until RecoverNativePhysicalBuild receives this value.
func deriveNativeBuildRecoveryPhysicalInput(
	request deploymentmodule.NativeDeliveryBuildRequest,
	plan nativeBuildPlan,
	prepared NativeBuildRecoveryPreparationResult,
	contract NativeBuildContract,
	artifacts release.CandidateArtifactSet,
	marker catalogartifact.CommitMarker,
	markerResolverFactory NativePhysicalMarkerResolverFactory,
	markerQuarantine NativeMarkerQuarantineWriter,
	observationReader NativeSourceObservationReader,
	snapshotFactory NativePhysicalSnapshotInspectorFactory,
) (NativePhysicalRecoveryInput, error) {
	if contract.PoolContract == nil {
		return NativePhysicalRecoveryInput{}, fmt.Errorf("%w: recovery pool contract is required", deploymentnative.ErrInvalid)
	}
	physicalRoot, err := contract.PoolContract.Pool.DataPath()
	if err != nil {
		return NativePhysicalRecoveryInput{}, err
	}
	return NativePhysicalRecoveryInput{
		Attempt: prepared.DeliveryAttempt, Marker: marker,
		Request:   nativeMaterializationRequest(artifacts, request, prepared.GenerationID, prepared.CandidateID, prepared.DeliveryAttempt.Namespace, plan.DeliveryPlan),
		CatalogID: contract.Catalog.CatalogID, ObjectRoot: physicalRoot, Compatibility: contract.Compatibility,
		MarkerResolverFactory: markerResolverFactory, MarkerQuarantine: markerQuarantine, ObservationReader: observationReader, SnapshotFactory: snapshotFactory,
	}, nil
}
