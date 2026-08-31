package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

func nativeReadError(err error) error {
	if errors.Is(err, nativepostgres.ErrNotFound) {
		return fmt.Errorf("%w: %v", deployment.ErrNotFound, err)
	}
	if errors.Is(err, nativepostgres.ErrConflict) || errors.Is(err, nativepostgres.ErrCASConflict) {
		return fmt.Errorf("%w: %v", deployment.ErrDeliveryConflict, err)
	}
	if errors.Is(err, nativepostgres.ErrInvalid) {
		return fmt.Errorf("%w: %v", deployment.ErrDeliveryInvalid, err)
	}
	return err
}

func nativeReadPlan(ctx context.Context, reader NativeDeliveryReader, id string) (deployment.DeliveryPlan, error) {
	row, err := reader.Plan(ctx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, nativeReadError(err)
	}
	plan, err := row.RichPlan()
	if err != nil {
		return deployment.DeliveryPlan{}, nativeReadError(err)
	}
	return plan, nil
}

func validateNativeReadScope(m *Module, project string, plan deployment.DeliveryPlan) error {
	if plan.ProjectID.String() != project || (m.handlerEnvironment() != "" && plan.Environment != m.handlerEnvironment()) || (m.instanceID != "" && plan.TargetID != m.instanceID) {
		return fmt.Errorf("%w: native delivery object is outside requested scope", deployment.ErrNotFound)
	}
	return nil
}

func nativeBuildStatus(state nativepostgres.BuildAttemptState, sealed bool) deploymentgen.DeliveryBuildStatus {
	switch state {
	case nativepostgres.AttemptCommitted:
		if sealed {
			return deploymentgen.DeliveryBuildStatusSealed
		}
		return deploymentgen.DeliveryBuildStatusSealing
	case nativepostgres.AttemptAborted, nativepostgres.AttemptFenced, nativepostgres.AttemptIndeterminate:
		return deploymentgen.DeliveryBuildStatusAbandoned
	default:
		return deploymentgen.DeliveryBuildStatusBuilding
	}
}

func nativeSealStatus(seal nativepostgres.SnapshotSeal) deploymentgen.DeliverySealStatus {
	if seal.QualifiedAt.IsZero() {
		return deploymentgen.DeliverySealStatusUploaded
	}
	return deploymentgen.DeliverySealStatusVerified
}

func nativeCandidateStatus(status string) deploymentgen.DeliveryCandidateStatus {
	switch strings.ToLower(status) {
	case "qualified", "ready":
		return deploymentgen.DeliveryCandidateStatusReady
	case "rejected", "failed":
		return deploymentgen.DeliveryCandidateStatusFailed
	case "retired":
		return deploymentgen.DeliveryCandidateStatusRetired
	default:
		return deploymentgen.DeliveryCandidateStatusPreparing
	}
}

func nativeGenerationStatus(active bool) deploymentgen.DeliveryGenerationStatus {
	if active {
		return deploymentgen.DeliveryGenerationStatusActive
	}
	return deploymentgen.DeliveryGenerationStatusPrepared
}

func nativePublicationStatus(state string) deploymentgen.DeliveryPublicationStatus {
	switch strings.ToLower(state) {
	case "committed":
		return deploymentgen.DeliveryPublicationStatusCommitted
	case "rejected":
		return deploymentgen.DeliveryPublicationStatusRejected
	case "indeterminate":
		return deploymentgen.DeliveryPublicationStatusIndeterminate
	default:
		return deploymentgen.DeliveryPublicationStatusPending
	}
}

func nativePlanResponse(plan deployment.DeliveryPlan) deploymentgen.DeliveryPlanPreviewResponse {
	return planPreviewResponse(plan)
}

func nativeBuildResponse(attempt nativepostgres.DeliveryBuildAttempt, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal) deploymentgen.DeliveryBuildStatusResponse {
	sealed := attempt.State == nativepostgres.AttemptCommitted && attempt.CandidateID != "" && seal.SealID != ""
	response := deploymentgen.DeliveryBuildStatusResponse{
		Id: attempt.AttemptID, PlanId: attempt.PlanID, PlanDigest: attempt.PlanDigest,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		BaseGenerationId: optionalText(plan.BaseGenerationID), PhysicalPoolId: attempt.PhysicalPoolID,
		Status: nativeBuildStatus(attempt.State, sealed), Revision: attempt.FencingEpoch,
		CreatedAt: isoTime(attempt.CreatedAt), UpdatedAt: isoTime(attempt.UpdatedAt),
		SealId: optionalText(seal.SealID), CandidateId: optionalText(attempt.CandidateID),
	}
	if attempt.FencingEpoch <= 0 {
		response.Revision = 1
	}
	response.TerminalAt = optionalText(isoTime(attempt.FinishedAt))
	if attempt.CandidateID == "" {
		response.CandidateId = nil
	}
	if seal.SealID == "" {
		response.SealId = nil
	}
	return response
}

func nativeSealResponse(seal nativepostgres.SnapshotSeal, plan deployment.DeliveryPlan) deploymentgen.DeliverySealStatusResponse {
	catalogDigest := seal.ClosureDigest
	if catalogDigest == "" {
		catalogDigest = seal.RelationManifestDigest
	}
	return deploymentgen.DeliverySealStatusResponse{
		Id: seal.SealID, AttemptId: seal.AttemptID, PlanId: plan.ID, PlanDigest: seal.PlanDigest,
		ExecutionDigest: plan.ExecutionDigest, PhysicalPoolId: seal.PhysicalPoolID,
		CatalogDigest: catalogDigest, ClosureDigest: optionalText(seal.ClosureDigest),
		CompatibilityDigest: seal.CompatibilityDigest, ServingArtifactId: seal.ServingArtifactID,
		ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateId: "",
		Status: nativeSealStatus(seal), CreatedAt: isoTime(seal.QualifiedAt), VerifiedAt: optionalText(isoTime(seal.QualifiedAt)),
	}
}

func nativeCandidateResponse(candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal) deploymentgen.DeliveryCandidateStatusResponse {
	catalogDigest := seal.ClosureDigest
	if catalogDigest == "" {
		catalogDigest = seal.RelationManifestDigest
	}
	response := deploymentgen.DeliveryCandidateStatusResponse{
		Id: candidate.CandidateID, PlanId: candidate.PlanID, PlanDigest: plan.Digest,
		TargetId: candidate.TargetID, ProjectId: plan.ProjectID.String(), Environment: plan.Environment,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		BaseGenerationId: optionalText(plan.BaseGenerationID), BaseTargetRevision: plan.BaseTargetRevision,
		SealId: candidate.SnapshotSealID, CatalogDigest: catalogDigest, CompatibilityDigest: seal.CompatibilityDigest,
		PhysicalPoolId: seal.PhysicalPoolID, ServingArtifactId: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
		Status: nativeCandidateStatus(candidate.Status), ResolvedInputs: []deploymentgen.DeliveryResolvedInputView{},
		CreatedAt: isoTime(candidate.CreatedAt), ReadyAt: optionalText(isoTime(candidate.QualifiedAt)), RetiredAt: optionalText(isoTime(candidate.RetiredAt)),
		QualificationDigest: optionalText(candidate.QualificationDigest),
	}
	return response
}

func nativeGenerationResponse(generation nativepostgres.DeliveryGeneration, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal, active bool) deploymentgen.DeliveryGenerationStatusResponse {
	catalogDigest := seal.ClosureDigest
	if catalogDigest == "" {
		catalogDigest = seal.RelationManifestDigest
	}
	return deploymentgen.DeliveryGenerationStatusResponse{
		Id: generation.GenerationID, CandidateId: generation.CandidateID, PlanId: generation.PlanID,
		PlanDigest: generation.PlanDigest, TargetId: generation.TargetID, ProjectId: plan.ProjectID.String(), Environment: plan.Environment,
		CatalogDigest: catalogDigest, PhysicalPoolId: seal.PhysicalPoolID, ServingArtifactId: seal.ServingArtifactID,
		ServingArtifactDigest: generation.ServingArtifactDigest, ServingStateId: generation.GenerationID,
		CompatibilityDigest: seal.CompatibilityDigest, RollbackClass: deploymentgen.DeliveryRollbackClass(plan.Evidence.Rollback.Class),
		Status: nativeGenerationStatus(active), CreatedAt: isoTime(generation.CreatedAt),
	}
}

func nativePublicationResponse(publication nativepostgres.DeliveryPublication, generation nativepostgres.DeliveryGeneration, plan deployment.DeliveryPlan) deploymentgen.DeliveryPublicationEvidenceResponse {
	return deploymentgen.DeliveryPublicationEvidenceResponse{
		Id: publication.PublicationID, RequestDigest: publication.RequestDigest, TargetId: publication.TargetID,
		ProjectId: plan.ProjectID.String(), Environment: plan.Environment, PlanId: generation.PlanID, PlanDigest: generation.PlanDigest,
		CandidateId: publication.CandidateID, GenerationId: publication.GenerationID,
		ExpectedBaseGenerationId: optionalText(publication.ExpectedBaseGenerationID), ExpectedTargetRevision: publication.ExpectedTargetRevision,
		ResultTargetRevision: publication.ResultTargetRevision, Status: nativePublicationStatus(publication.State),
		CreatedAt: isoTime(publication.CreatedAt), CompletedAt: optionalText(isoTime(publication.CommittedAt)),
	}
}

func nativeOperatorResponse(snapshot nativepostgres.DeliveryOperatorSnapshot) deploymentgen.DeliveryOperatorSnapshotResponse {
	response := deploymentgen.DeliveryOperatorSnapshotResponse{ProjectId: snapshot.ProjectID, Environment: snapshot.Environment, TargetId: snapshot.TargetID, TargetRevision: snapshot.TargetRevision, DegradedReasons: []string{}, PhysicalPools: []deploymentgen.DeliveryPhysicalPoolAdmissionView{}, Roots: []deploymentgen.DeliveryRootView{}, QueryLeases: []deploymentgen.DeliveryQueryLeaseView{}, WriterLeases: []deploymentgen.DeliveryWriterLeaseView{}, GcCycles: []deploymentgen.DeliveryGCCycleView{}, GcDeleteIntents: []deploymentgen.DeliveryGCDeleteIntentView{}}
	response.ActiveGeneration = optionalText(snapshot.ActiveGenerationID)
	return response
}
