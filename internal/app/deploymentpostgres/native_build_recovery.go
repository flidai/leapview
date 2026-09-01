package deploymentpostgres

import (
	"context"
	"fmt"
	"strings"

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
	prepared, err := PrepareNativeBuildRecovery(ctx, c.repository, c.operations, c.attemptTermination, NativeBuildRecoveryPreparationInput{
		Request: request, RequestDigest: requestDigest, Operation: reservation.Operation,
		PhysicalPoolID: c.physicalPoolID, CatalogID: contract.Catalog.CatalogID,
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
		Artifact: artifactBinding, DuckLakeAttempt: prepared.DuckLakeAttempt,
	}
	physicalInput, err := deriveNativeBuildRecoveryPhysicalInput(request, plan, prepared, contract, artifacts, marker, c.markerResolverFactory, c.observationReader, c.snapshotFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	physical, err := RecoverNativePhysicalBuild(ctx, physicalInput)
	if err != nil {
		// In particular, marker absence remains indeterminate/retryable. No
		// terminal operation transition is attempted on this path.
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
	sealID, err := nativeBuildConsequenceID(prepared.Operation.OperationID, "seal")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	assembled, err := AssembleRecoveredNativeGenerationAdmissionInput(NativeRecoveredSealEvidenceAssemblerInput{
		Build: physical, AttemptAdmission: attemptAdmission, PoolContract: contract.PoolContract,
		CatalogIdentity: contract.Catalog, Compatibility: contract.Compatibility, Plan: plan.DeliveryPlan,
		Artifacts: artifacts, RuntimeVersion: c.runtimeVersion, Qualification: qualification,
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
		MarkerResolverFactory: markerResolverFactory, ObservationReader: observationReader, SnapshotFactory: snapshotFactory,
	}, nil
}
