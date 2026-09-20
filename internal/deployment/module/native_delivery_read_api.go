package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
)

// nativeGenerationLifecycleReader is an optional extension implemented by
// the native PostgreSQL authority. The base read port remains compatible with
// narrow test/offline readers, while the production authority can project
// lifecycle timestamps retained by publication and retention-root rows.
type nativeGenerationLifecycleReader interface {
	HistoricalCommittedPublication(context.Context, string) (nativepostgres.DeliveryPublication, error)
	GenerationRetentionRoot(context.Context, string) (nativepostgres.DeliveryRetentionRoot, error)
	GenerationRollbackUntil(context.Context, string) (time.Time, error)
}

func (m *Module) nativeGenerationLifecycle(ctx context.Context, generation nativepostgres.DeliveryGeneration, operator nativepostgres.DeliveryOperatorSnapshot) (bool, time.Time, time.Time, time.Time, error) {
	active := operator.ActiveGenerationID == generation.GenerationID
	var activatedAt time.Time
	if active && operator.ActivePublicationID != "" {
		publication, err := m.nativeDeliveryReader.Publication(ctx, operator.ActivePublicationID)
		if err != nil {
			return false, time.Time{}, time.Time{}, time.Time{}, nativeReadError(err)
		}
		if publication.PublicationID != operator.ActivePublicationID || publication.GenerationID != generation.GenerationID || publication.TargetID != generation.TargetID || publication.State != "committed" {
			return false, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%w: active generation publication identity is inconsistent", deployment.ErrDeliveryConflict)
		}
		activatedAt = publication.CommittedAt
	}
	var retiredAt, rollbackUntil time.Time
	lifecycle, ok := m.nativeDeliveryReader.(nativeGenerationLifecycleReader)
	if !ok {
		return active, activatedAt, retiredAt, rollbackUntil, nil
	}
	if publication, err := lifecycle.HistoricalCommittedPublication(ctx, generation.GenerationID); err == nil {
		if publication.GenerationID != generation.GenerationID || publication.TargetID != generation.TargetID || publication.State != "committed" {
			return false, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%w: generation publication identity is inconsistent", deployment.ErrDeliveryConflict)
		}
		if activatedAt.IsZero() {
			activatedAt = publication.CommittedAt
		}
	} else if !errors.Is(err, nativepostgres.ErrNotFound) {
		return false, time.Time{}, time.Time{}, time.Time{}, nativeReadError(err)
	}
	if root, err := lifecycle.GenerationRetentionRoot(ctx, generation.GenerationID); err == nil {
		if root.GenerationID != generation.GenerationID || root.TargetID != generation.TargetID || root.RootKind != "generation" {
			return false, time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%w: generation retention identity is inconsistent", deployment.ErrDeliveryConflict)
		}
		retiredAt = root.RetiredAt
	} else if !errors.Is(err, nativepostgres.ErrNotFound) {
		return false, time.Time{}, time.Time{}, time.Time{}, nativeReadError(err)
	}
	if rollback, err := lifecycle.GenerationRollbackUntil(ctx, generation.GenerationID); err == nil {
		rollbackUntil = rollback
	} else if !errors.Is(err, nativepostgres.ErrNotFound) {
		return false, time.Time{}, time.Time{}, time.Time{}, nativeReadError(err)
	}
	return active, activatedAt, retiredAt, rollbackUntil, nil
}

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
	case nativepostgres.AttemptAborted:
		return deploymentgen.DeliveryBuildStatusFailed
	case nativepostgres.AttemptIndeterminate:
		return deploymentgen.DeliveryBuildStatusAbandoned
	default:
		return deploymentgen.DeliveryBuildStatusBuilding
	}
}

// nativeBuildFailureCode returns the bounded classification retained in the
// attempt termination evidence. The native build authority deliberately does
// not persist raw error text; classification is the stable operator-facing
// failure code available to this read model.
func nativeBuildFailureCode(attempt nativepostgres.DeliveryBuildAttempt) *string {
	if attempt.State != nativepostgres.AttemptAborted && attempt.State != nativepostgres.AttemptIndeterminate {
		return nil
	}
	var evidence struct {
		Classification string `json:"classification"`
	}
	if len(attempt.TerminationEvidence) == 0 || json.Unmarshal(attempt.TerminationEvidence, &evidence) != nil {
		return nil
	}
	return optionalText(evidence.Classification)
}

func nativeSealStatus(seal nativepostgres.SnapshotSeal, attemptState nativepostgres.BuildAttemptState) deploymentgen.DeliverySealStatus {
	if attemptState == nativepostgres.AttemptAborted || attemptState == nativepostgres.AttemptIndeterminate {
		return deploymentgen.DeliverySealStatusFailed
	}
	if seal.QualifiedAt.IsZero() {
		return deploymentgen.DeliverySealStatusUploaded
	}
	return deploymentgen.DeliverySealStatusVerified
}

func nativeCandidateStatus(status string) deploymentgen.DeliveryCandidateStatus {
	switch strings.ToLower(status) {
	case "qualified", "admitted":
		return deploymentgen.DeliveryCandidateStatusReady
	case "rejected", "failed":
		return deploymentgen.DeliveryCandidateStatusFailed
	case "retired":
		return deploymentgen.DeliveryCandidateStatusRetired
	default:
		return deploymentgen.DeliveryCandidateStatusPreparing
	}
}

func nativeGenerationStatus(active bool, retiredAt time.Time) deploymentgen.DeliveryGenerationStatus {
	if active {
		return deploymentgen.DeliveryGenerationStatusActive
	}
	if !retiredAt.IsZero() {
		return deploymentgen.DeliveryGenerationStatusRetired
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
	response := planPreviewResponse(plan)
	// Expiry is derived from the immutable governance deadline so a plan that
	// was never revisited by a writer still reports its terminal lifecycle
	// state during incident discovery.
	if response.Status == deploymentgen.DeliveryPlanStatusPlanned &&
		!plan.Governance.ExpiresAt.IsZero() && plan.Expired(time.Now().UTC()) {
		response.Status = deploymentgen.DeliveryPlanStatusExpired
	}
	return response
}

func nativeBuildResponse(attempt nativepostgres.DeliveryBuildAttempt, plan deployment.DeliveryPlan, candidate nativepostgres.DeliveryCandidate, seal nativepostgres.SnapshotSeal) deploymentgen.DeliveryBuildStatusResponse {
	sealed := attempt.State == nativepostgres.AttemptCommitted && attempt.CandidateID != "" && seal.SealID != ""
	response := deploymentgen.DeliveryBuildStatusResponse{
		Id: attempt.AttemptID, PlanId: attempt.PlanID, PlanDigest: attempt.PlanDigest,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		BaseGenerationId: optionalText(plan.BaseGenerationID), PhysicalPoolId: attempt.PhysicalPoolID,
		Status: nativeBuildStatus(attempt.State, sealed), Revision: attempt.FencingEpoch,
		CreatedAt: isoTime(attempt.CreatedAt), UpdatedAt: isoTime(attempt.UpdatedAt),
		SnapshotSealId: optionalText(seal.SealID), CandidateId: optionalText(attempt.CandidateID),
		CandidateRevision: optionalNativeInt64(candidate.CandidateRevision),
	}
	response.FailureCode = nativeBuildFailureCode(attempt)
	if seal.SealID != "" {
		response.DucklakeSnapshotId = optionalNativeInt64(seal.DuckLakeSnapshotID)
		response.RelationManifestDigest = optionalText(seal.RelationManifestDigest)
		response.ClosureDigest = optionalText(seal.ClosureDigest)
	}
	response.QualificationDigest = optionalText(candidate.QualificationDigest)
	if attempt.FencingEpoch <= 0 {
		response.Revision = 1
	}
	response.TerminalAt = optionalText(isoTime(attempt.FinishedAt))
	if attempt.CandidateID == "" {
		response.CandidateId = nil
	}
	if seal.SealID == "" {
		response.SnapshotSealId = nil
	}
	return response
}

func nativeSealResponse(seal nativepostgres.SnapshotSeal, attempt nativepostgres.DeliveryBuildAttempt, candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan) deploymentgen.DeliverySealStatusResponse {
	response := deploymentgen.DeliverySealStatusResponse{
		SnapshotSealId: seal.SealID, AttemptId: seal.AttemptID, PlanId: plan.ID, PlanDigest: seal.PlanDigest,
		ExecutionDigest: plan.ExecutionDigest, PhysicalPoolId: seal.PhysicalPoolID,
		DucklakeSnapshotId: seal.DuckLakeSnapshotID, RelationManifestDigest: seal.RelationManifestDigest, ClosureDigest: seal.ClosureDigest,
		CompatibilityDigest: seal.CompatibilityDigest, ServingArtifactId: seal.ServingArtifactID,
		ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateId: "", QualificationDigest: optionalText(candidate.QualificationDigest),
		Status: nativeSealStatus(seal, attempt.State), CreatedAt: isoTime(seal.CreatedAt), VerifiedAt: optionalText(isoTime(seal.QualifiedAt)),
	}
	response.FailureCode = nativeBuildFailureCode(attempt)
	return response
}

type nativeResolvedInputsRecord struct {
	Inputs         []deployment.DeliveryResolvedDataInput `json:"inputs"`
	PolicyDigest   string                                 `json:"policyDigest"`
	EvidenceDigest string                                 `json:"evidenceDigest"`
}

func nativeCandidateResolvedInputs(candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal) (deployment.DeliveryResolvedBuildInputs, error) {
	if nativeResolvedInputsEmpty(candidate.ResolvedInputs) {
		if !nativeResolvedInputsEmpty(seal.ResolvedInputs) {
			return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: candidate resolved-input evidence is missing from its snapshot seal", deployment.ErrDeliveryConflict)
		}
		return deployment.DeliveryResolvedBuildInputs{Inputs: []deployment.DeliveryResolvedDataInput{}}, nil
	}
	var record nativeResolvedInputsRecord
	if err := strictjson.DecodeWithOptions(candidate.ResolvedInputs, &record, strictjson.Options{MaxBytes: 32 << 10, MaxDepth: 24, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: false}); err != nil {
		return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: decode candidate resolved-input evidence: %v", deployment.ErrDeliveryInvalid, err)
	}
	if candidate.ResolvedInputsDigest == "" || record.EvidenceDigest != candidate.ResolvedInputsDigest {
		return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: candidate resolved-input digest is incomplete", deployment.ErrDeliveryConflict)
	}
	if !nativeResolvedInputsEmpty(seal.ResolvedInputs) {
		var sealRecord nativeResolvedInputsRecord
		if err := strictjson.DecodeWithOptions(seal.ResolvedInputs, &sealRecord, strictjson.Options{MaxBytes: 32 << 10, MaxDepth: 24, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: false}); err != nil {
			return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: decode seal resolved-input evidence: %v", deployment.ErrDeliveryInvalid, err)
		}
		if seal.ResolvedInputsDigest != candidate.ResolvedInputsDigest || sealRecord.EvidenceDigest != record.EvidenceDigest || !sameJSON(seal.ResolvedInputs, candidate.ResolvedInputs) {
			return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: candidate and seal resolved-input evidence differ", deployment.ErrDeliveryConflict)
		}
	}
	qualification, err := decodeNativePreviewGateEvidence(seal.QualificationEvidence)
	if err != nil {
		return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: candidate gate evidence unavailable: %v", deployment.ErrDeliveryInvalid, err)
	}
	if qualification.CandidateID != candidate.CandidateID || qualification.AttemptID != seal.AttemptID || (candidate.AttemptID != "" && qualification.AttemptID != candidate.AttemptID) || qualification.Digest != candidate.QualificationDigest {
		return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: candidate gate evidence identity differs", deployment.ErrDeliveryConflict)
	}
	resolved, err := deployment.ValidateDeliveryResolvedBuildInputs(plan, deployment.DeliveryResolvedBuildInputs{Inputs: record.Inputs, PolicyDigest: record.PolicyDigest, EvidenceDigest: record.EvidenceDigest, GateEvidence: &qualification.Gates})
	if err != nil {
		return deployment.DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: validate candidate resolved-input evidence: %v", deployment.ErrDeliveryConflict, err)
	}
	return resolved, nil
}

func nativeResolvedInputsEmpty(raw []byte) bool {
	return len(raw) == 0 || strings.TrimSpace(string(raw)) == "{}"
}

func nativeCandidateResponse(candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal, servingStateID string) (deploymentgen.DeliveryCandidateStatusResponse, error) {
	resolved, err := nativeCandidateResolvedInputs(candidate, plan, seal)
	if err != nil {
		return deploymentgen.DeliveryCandidateStatusResponse{}, err
	}
	response := deploymentgen.DeliveryCandidateStatusResponse{
		Id: candidate.CandidateID, PlanId: candidate.PlanID, PlanDigest: plan.Digest,
		TargetId: candidate.TargetID, ProjectId: plan.ProjectID.String(), Environment: plan.Environment,
		SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest,
		BaseGenerationId: optionalText(plan.BaseGenerationID), BaseTargetRevision: plan.BaseTargetRevision,
		SnapshotSealId: optionalText(candidate.SnapshotSealID), DucklakeSnapshotId: optionalNativeInt64(seal.DuckLakeSnapshotID),
		RelationManifestDigest: optionalText(seal.RelationManifestDigest), ClosureDigest: optionalText(seal.ClosureDigest), CompatibilityDigest: seal.CompatibilityDigest,
		PhysicalPoolId: seal.PhysicalPoolID, ServingArtifactId: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
		ServingStateId: servingStateID,
		Status:         nativeCandidateStatus(candidate.Status), ResolvedInputs: deliveryResolvedInputViews(resolved),
		CreatedAt: isoTime(candidate.CreatedAt), ReadyAt: optionalText(isoTime(candidate.QualifiedAt)), RetiredAt: optionalText(isoTime(candidate.RetiredAt)),
		QualificationDigest: optionalText(candidate.QualificationDigest),
	}
	if candidate.SnapshotSealID == "" {
		response.SnapshotSealId = nil
	}
	response.ResolvedInputsDigest = optionalText(resolved.EvidenceDigest)
	return response, nil
}

func resolveNativeCandidateServingState(ctx context.Context, reader NativeDeliveryReader, candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal) (string, error) {
	status := nativeCandidateStatus(candidate.Status)
	if (status != deploymentgen.DeliveryCandidateStatusReady && status != deploymentgen.DeliveryCandidateStatusRetired) || candidate.SnapshotSealID == "" {
		return "", nil
	}
	resolution, err := reader.ResolveCandidateGeneration(ctx, candidate.CandidateID)
	if err != nil {
		// A qualified candidate may be waiting for publication, and a retired
		// candidate may never have been published. Preserve those lifecycle
		// states without inventing a serving identity; malformed multi-generation
		// history still fails closed as a conflict.
		if (status == deploymentgen.DeliveryCandidateStatusReady || status == deploymentgen.DeliveryCandidateStatusRetired) && errors.Is(err, nativepostgres.ErrNotFound) {
			return "", nil
		}
		return "", nativeReadError(err)
	}
	if resolution.CandidateID != candidate.CandidateID ||
		resolution.TargetID != candidate.TargetID ||
		resolution.PlanID != candidate.PlanID ||
		resolution.SnapshotSealID != seal.SealID ||
		resolution.Status != candidate.Status ||
		resolution.CandidateRevision != candidate.CandidateRevision ||
		resolution.ArtifactDigest != candidate.ArtifactDigest ||
		resolution.ProjectID != plan.ProjectID.String() ||
		resolution.Environment != plan.Environment ||
		resolution.GenerationCount != 1 ||
		strings.TrimSpace(resolution.GenerationID) == "" {
		return "", fmt.Errorf("%w: native candidate generation resolution is inconsistent", deployment.ErrDeliveryConflict)
	}
	generation, err := reader.Generation(ctx, resolution.GenerationID)
	if err != nil {
		return "", nativeReadError(err)
	}
	if generation.GenerationID != resolution.GenerationID ||
		generation.CandidateID != candidate.CandidateID ||
		generation.TargetID != candidate.TargetID ||
		generation.PlanID != candidate.PlanID ||
		generation.SnapshotSealID != seal.SealID ||
		generation.PlanDigest != plan.Digest ||
		generation.ServingArtifactDigest != seal.ServingArtifactDigest {
		return "", fmt.Errorf("%w: native candidate generation identity is inconsistent", deployment.ErrDeliveryConflict)
	}
	return generation.GenerationID, nil
}

func nativeGenerationResponse(generation nativepostgres.DeliveryGeneration, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal, active bool, activatedAt, retiredAt, rollbackUntil time.Time) deploymentgen.DeliveryGenerationStatusResponse {
	return deploymentgen.DeliveryGenerationStatusResponse{
		Id: generation.GenerationID, CandidateId: generation.CandidateID, PlanId: generation.PlanID,
		PlanDigest: generation.PlanDigest, TargetId: generation.TargetID, ProjectId: plan.ProjectID.String(), Environment: plan.Environment,
		SnapshotSealId: seal.SealID, DucklakeSnapshotId: seal.DuckLakeSnapshotID, RelationManifestDigest: seal.RelationManifestDigest, ClosureDigest: seal.ClosureDigest,
		PhysicalPoolId: seal.PhysicalPoolID, ServingArtifactId: seal.ServingArtifactID,
		ServingArtifactDigest: generation.ServingArtifactDigest, ServingStateId: generation.GenerationID,
		CompatibilityDigest: seal.CompatibilityDigest, RollbackClass: deploymentgen.DeliveryRollbackClass(plan.Evidence.Rollback.Class),
		Status: nativeGenerationStatus(active, retiredAt), CreatedAt: isoTime(generation.CreatedAt), ActivatedAt: optionalText(isoTime(activatedAt)), RetiredAt: optionalText(isoTime(retiredAt)), RollbackUntil: optionalText(isoTime(rollbackUntil)),
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
	// The native reader owns target identity and active pointers only. Mark the
	// projection degraded while detail authorities are unavailable so an empty
	// detail set cannot be mistaken for a healthy zero-resource target.
	response := deploymentgen.DeliveryOperatorSnapshotResponse{
		ProjectId: snapshot.ProjectID, Environment: snapshot.Environment, TargetId: snapshot.TargetID,
		TargetRevision: snapshot.TargetRevision, Degraded: true,
		DegradedReasons: []string{"detailed_evidence_unavailable"},
	}
	response.ActiveGeneration = optionalText(snapshot.ActiveGenerationID)
	return response
}
