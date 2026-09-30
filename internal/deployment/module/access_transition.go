package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// AccessTransitionApprovalScope is the exact durable publication evidence
// needed by the offline approval callback. The app composition converts it to
// a request-local access authorization marker after checking the admitted host
// fence and staged policy head.
type AccessTransitionApprovalScope struct {
	TargetID, Environment, IntentDigest string
	ProjectID                           projectgraph.ResourceID
	ExpectedActiveGenerationID          string
	CandidateID, CandidateGenerationID  string
	PublicationID                       string
	PlanPolicySnapshotDigest            string
	PublisherPrincipalID                string
	ReviewerPrincipalID                 string
}

// AccessTransitionSnapshotDigests keeps the planning fingerprint (compiled
// with the stable candidate-policy identity) distinct from the immutable
// serving snapshot (compiled with the concrete generation identity).
type AccessTransitionSnapshotDigests struct {
	PlanPolicySnapshotDigest    string
	ServingPolicySnapshotDigest string
}

type AccessTransitionApprovalContext func(context.Context, AccessTransitionApprovalScope) (context.Context, AccessTransitionSnapshotDigests, error)

// NativeAccessTransitionRequest drives one publication over the exact frozen
// source retained by the currently active predecessor generation. It does not
// write serving snapshots directly: the native plan/build/publication and
// independent approval authorities remain the only mutation path.
type NativeAccessTransitionRequest struct {
	TargetID, Environment, IntentDigest string
	ProjectID                           projectgraph.ResourceID
	ExpectedActiveGenerationID          string
	PublisherPrincipalID                string
	ReviewerPrincipalID                 string
	OperationID                         string
	PublisherActor                      ApprovalActor
	ReviewerActor                       ApprovalActor
	ApprovalContext                     AccessTransitionApprovalContext
}

type NativeAccessTransitionResult struct {
	PlanID, CandidateID, GenerationID, PublicationID, ApprovalRequestID string
	PlanPolicySnapshotDigest, ServingPolicySnapshotDigest               string
	Status                                                              string
}

// ExecuteNativeAccessTransition composes existing native delivery operations
// for the one admitted permissions transition. The active predecessor plan is
// the only allowed source owner/digest input.
func (m *Module) ExecuteNativeAccessTransition(ctx context.Context, request NativeAccessTransitionRequest) (NativeAccessTransitionResult, error) {
	if m == nil || m.nativeDeliveryReader == nil || m.nativeDeliveryMutations == nil || m.nativeDeliveryPublication == nil || m.nativeDeliveryApproval == nil || m.candidateAdmission == nil || request.ApprovalContext == nil {
		return NativeAccessTransitionResult{}, ErrDeliveryInputUnavailable
	}
	if ctx == nil || strings.TrimSpace(request.TargetID) == "" || strings.TrimSpace(request.Environment) == "" || request.ProjectID.Validate() != nil ||
		strings.TrimSpace(request.ExpectedActiveGenerationID) == "" || strings.TrimSpace(request.PublisherPrincipalID) == "" || strings.TrimSpace(request.ReviewerPrincipalID) == "" || request.PublisherPrincipalID == request.ReviewerPrincipalID ||
		!validAccessTransitionDigest(request.IntentDigest) || !strings.HasPrefix(request.OperationID, "access-transition:") || request.OperationID != strings.TrimSpace(request.OperationID) {
		return NativeAccessTransitionResult{}, deployment.ErrDeliveryInvalid
	}
	if request.PublisherActor.PrincipalID != request.PublisherPrincipalID || request.ReviewerActor.PrincipalID != request.ReviewerPrincipalID || request.PublisherActor.CredentialID == "" || request.ReviewerActor.CredentialID == "" || request.PublisherActor.CredentialID == request.ReviewerActor.CredentialID ||
		request.PublisherActor.CredentialClass != "workload" || request.ReviewerActor.CredentialClass != "workload" {
		return NativeAccessTransitionResult{}, deployment.ErrDeliveryInvalid
	}

	target, err := m.nativeDeliveryReader.OperatorSnapshot(ctx, request.TargetID)
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if target.TargetID != request.TargetID || target.ProjectID != request.ProjectID.String() || target.Environment != request.Environment {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: access transition target scope changed", deployment.ErrDeliveryConflict)
	}
	if target.ActiveGenerationID != request.ExpectedActiveGenerationID {
		return m.replayActivatedAccessTransition(ctx, request, target)
	}
	predecessor, err := m.nativeDeliveryReader.LoadGeneration(ctx, request.ExpectedActiveGenerationID)
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if predecessor.GenerationID != request.ExpectedActiveGenerationID || predecessor.TargetID != request.TargetID || predecessor.CandidateID == "" || predecessor.PlanID == "" {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: active predecessor generation is incomplete", deployment.ErrDeliveryConflict)
	}
	predecessorPlanRow, err := m.nativeDeliveryReader.LoadPlan(ctx, predecessor.PlanID)
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	var predecessorPlan deployment.DeliveryPlan
	if err := json.Unmarshal(predecessorPlanRow.PlanDocument, &predecessorPlan); err != nil {
		return NativeAccessTransitionResult{}, fmt.Errorf("decode retained predecessor plan: %w", err)
	}
	if predecessorPlan.ID != predecessor.PlanID || predecessorPlan.TargetID != request.TargetID || predecessorPlan.ProjectID != request.ProjectID || predecessorPlan.Environment != request.Environment ||
		predecessorPlan.SourceOwnerID == "" || predecessorPlan.SourceDigest == "" || predecessorPlan.Provenance.AttestationDigest == "" {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: predecessor does not retain an exact source attestation", deployment.ErrDeliveryConflict)
	}

	planRequest := NativeDeliveryPlanRequest{
		ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment,
		PrincipalID: request.PublisherPrincipalID, SourceOwnerID: predecessorPlan.SourceOwnerID,
		Operation: string(deployment.DeliveryOperationPolicyChange), SourceDigest: predecessorPlan.SourceDigest,
		SourceAttestationDigest: predecessorPlan.Provenance.AttestationDigest,
		IdempotencyKey:          accessTransitionIdempotencyKey(request.IntentDigest, "plan"),
	}
	createdPlan, err := m.nativeDeliveryMutations.CreatePlan(ctx, planRequest)
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if err := createdPlan.validate(planRequest, m.handlerEnvironment()); err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if err := completeNativePlanCommand(ctx, m.nativeDeliveryMutations, createdPlan); err != nil {
		return NativeAccessTransitionResult{}, err
	}
	planRow, err := m.nativeDeliveryReader.LoadPlan(ctx, createdPlan.ID.String())
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	var persistedPlan deployment.DeliveryPlan
	if err := json.Unmarshal(planRow.PlanDocument, &persistedPlan); err != nil {
		return NativeAccessTransitionResult{}, fmt.Errorf("decode typed transition plan: %w", err)
	}
	if persistedPlan.ID != createdPlan.ID.String() || persistedPlan.TargetID != request.TargetID || persistedPlan.ProjectID != request.ProjectID || persistedPlan.Environment != request.Environment ||
		persistedPlan.Operation != deployment.DeliveryOperationPolicyChange || persistedPlan.SourceOwnerID != predecessorPlan.SourceOwnerID ||
		persistedPlan.SourceDigest != predecessorPlan.SourceDigest || persistedPlan.Provenance.AttestationDigest != predecessorPlan.Provenance.AttestationDigest ||
		persistedPlan.BaseGenerationID != request.ExpectedActiveGenerationID || persistedPlan.Authorization == nil || !validAccessTransitionDigest(persistedPlan.Authorization.SnapshotDigest) {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition plan has no exact typed authorization snapshot", deployment.ErrDeliveryConflict)
	}
	if !persistedPlan.Governance.RequiresApproval || persistedPlan.Governance.ApprovalPolicyRevision <= 0 {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition plan does not require independent publication approval", deployment.ErrDeliveryConflict)
	}

	lease, err := m.candidateAdmission.AcquireCandidatePreparation(ctx)
	if err != nil {
		return NativeAccessTransitionResult{}, candidatePreparationError(err)
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return NativeAccessTransitionResult{}, ErrDeliveryInputUnavailable
	}
	buildCtx := lease.Context()
	built, buildErr := m.nativeDeliveryMutations.BuildPlan(buildCtx, NativeDeliveryBuildRequest{
		ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment,
		PlanID: createdPlan.ID, PrincipalID: request.PublisherPrincipalID,
		IdempotencyKey: accessTransitionIdempotencyKey(request.IntentDigest, "build"),
	})
	if buildErr != nil {
		lease.Release()
		return NativeAccessTransitionResult{}, buildErr
	}
	if err := built.validate(NativeDeliveryBuildRequest{ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment, PlanID: createdPlan.ID, PrincipalID: request.PublisherPrincipalID, IdempotencyKey: accessTransitionIdempotencyKey(request.IntentDigest, "build")}); err != nil {
		lease.Release()
		return NativeAccessTransitionResult{}, err
	}
	if built.PlanDigest != persistedPlan.Digest || built.SourceDigest != persistedPlan.SourceDigest || built.ExecutionDigest != persistedPlan.ExecutionDigest {
		lease.Release()
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition build differs from the persisted plan", deployment.ErrDeliveryConflict)
	}
	if err := completeNativeBuildCommand(buildCtx, m.nativeDeliveryMutations, built); err != nil {
		lease.Release()
		return NativeAccessTransitionResult{}, err
	}
	lease.Release()
	if built.CandidateID == uuid.Nil {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition build returned no candidate", deployment.ErrDeliveryConflict)
	}

	publication, err := m.nativeDeliveryPublication.PublishCandidate(ctx, NativeDeliveryPublishRequest{
		ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment,
		CandidateID: built.CandidateID, PrincipalID: request.PublisherPrincipalID,
		IdempotencyKey: accessTransitionIdempotencyKey(request.IntentDigest, "publish"),
	})
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if err := publication.validate(request.ProjectID, request.TargetID, request.Environment); err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if publication.CandidateID != built.CandidateID || publication.PlanID != createdPlan.ID || publication.PlanDigest != persistedPlan.Digest ||
		publication.ActorID != request.PublisherPrincipalID || publication.IdempotencyKey != accessTransitionIdempotencyKey(request.IntentDigest, "publish") ||
		publication.ExpectedBaseGenerationID.String() != request.ExpectedActiveGenerationID || publication.GenerationID == uuid.Nil {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition publication does not bind the requested predecessor and candidate", deployment.ErrDeliveryConflict)
	}
	if err := completeNativePublishCommand(ctx, m.nativeDeliveryPublication, publication); err != nil {
		return NativeAccessTransitionResult{}, err
	}

	approvalKey := accessTransitionIdempotencyKey(request.IntentDigest, "approval-request")
	requestID := deterministicApprovalUUID("request:" + publication.ID.String() + ":" + approvalKey)
	approvalLookup := NativeApprovalLookup{ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment, PublicationID: publication.ID.String(), RequestID: requestID}
	approval, err := m.nativeDeliveryApproval.GetPublicationApproval(ctx, approvalLookup)
	if errors.Is(err, depauth.ErrApprovalNotFound) {
		if publication.Status != "pending" {
			return NativeAccessTransitionResult{}, fmt.Errorf("%w: committed transition publication has no independent approval request", depauth.ErrApprovalConflict)
		}
		approvalCtx, digests, scopeErr := request.ApprovalContext(ctx, AccessTransitionApprovalScope{
			TargetID: request.TargetID, Environment: request.Environment, ProjectID: request.ProjectID,
			ExpectedActiveGenerationID: request.ExpectedActiveGenerationID, CandidateID: built.CandidateID.String(),
			CandidateGenerationID: publication.GenerationID.String(), PublicationID: publication.ID.String(),
			PlanPolicySnapshotDigest: persistedPlan.Authorization.SnapshotDigest, IntentDigest: request.IntentDigest,
			PublisherPrincipalID: request.PublisherPrincipalID, ReviewerPrincipalID: request.ReviewerPrincipalID,
		})
		if scopeErr != nil {
			return NativeAccessTransitionResult{}, scopeErr
		}
		if err := validateAccessTransitionSnapshotDigests(digests, persistedPlan.Authorization.SnapshotDigest); err != nil {
			return NativeAccessTransitionResult{}, err
		}
		approval, err = m.nativeDeliveryApproval.RequestPublicationApproval(approvalCtx, NativeApprovalRequest{
			ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment,
			PublicationID: publication.ID, PrincipalID: request.PublisherPrincipalID, IdempotencyKey: approvalKey, Actor: request.PublisherActor,
		})
	}
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if approval.RequestID != requestID || approval.PublicationID != publication.ID.String() || approval.GenerationID != publication.GenerationID.String() || approval.RequestedBy.PrincipalID != request.PublisherPrincipalID {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: persisted transition approval differs from the admitted publisher and publication", depauth.ErrApprovalConflict)
	}
	if publication.Status == "committed" && (approval.LatestDecision == nil || approval.LatestDecision.Decision != depauth.ApprovalActionApprove || approval.LatestDecision.DecidedBy.PrincipalID != request.ReviewerPrincipalID) {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: committed transition publication lacks the independent reviewer approval", depauth.ErrApprovalConflict)
	}
	if approval.LatestDecision != nil && (approval.LatestDecision.Decision != depauth.ApprovalActionApprove || approval.LatestDecision.DecidedBy.PrincipalID != request.ReviewerPrincipalID) {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: persisted transition approval has a conflicting reviewer decision", depauth.ErrApprovalConflict)
	}
	if approval.LatestDecision == nil {
		decisionCtx, digests, scopeErr := request.ApprovalContext(ctx, AccessTransitionApprovalScope{
			TargetID: request.TargetID, Environment: request.Environment, ProjectID: request.ProjectID,
			ExpectedActiveGenerationID: request.ExpectedActiveGenerationID, CandidateID: built.CandidateID.String(),
			CandidateGenerationID: publication.GenerationID.String(), PublicationID: publication.ID.String(),
			PlanPolicySnapshotDigest: persistedPlan.Authorization.SnapshotDigest, IntentDigest: request.IntentDigest,
			PublisherPrincipalID: request.PublisherPrincipalID, ReviewerPrincipalID: request.ReviewerPrincipalID,
		})
		if scopeErr != nil {
			return NativeAccessTransitionResult{}, scopeErr
		}
		if err := validateAccessTransitionSnapshotDigests(digests, persistedPlan.Authorization.SnapshotDigest); err != nil {
			return NativeAccessTransitionResult{}, err
		}
		approval, err = m.nativeDeliveryApproval.ApprovePublicationApproval(decisionCtx, NativeApprovalDecision{
			ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment,
			PublicationID: publication.ID.String(), RequestID: requestID, ExpectedRevision: approvalDecisionRevision(approval),
			IdempotencyKey: accessTransitionIdempotencyKey(request.IntentDigest, "approval-approve"), Actor: request.ReviewerActor,
		})
		if err != nil {
			return NativeAccessTransitionResult{}, err
		}
	}
	if approval.LatestDecision == nil || approval.LatestDecision.Decision != depauth.ApprovalActionApprove || approval.LatestDecision.DecidedBy.PrincipalID != request.ReviewerPrincipalID {
		return NativeAccessTransitionResult{}, fmt.Errorf("%w: transition approval was not independently granted", depauth.ErrApprovalConflict)
	}
	// Re-check the exact typed policy and staged policy head for an exact
	// approval retry as well as the first request/decision path. This also
	// supplies the generation-bound digest used by the host activation waiter.
	_, snapshotDigests, err := request.ApprovalContext(ctx, AccessTransitionApprovalScope{
		TargetID: request.TargetID, Environment: request.Environment, ProjectID: request.ProjectID,
		ExpectedActiveGenerationID: request.ExpectedActiveGenerationID, CandidateID: built.CandidateID.String(),
		CandidateGenerationID: publication.GenerationID.String(), PublicationID: publication.ID.String(),
		PlanPolicySnapshotDigest: persistedPlan.Authorization.SnapshotDigest, IntentDigest: request.IntentDigest,
		PublisherPrincipalID: request.PublisherPrincipalID, ReviewerPrincipalID: request.ReviewerPrincipalID,
	})
	if err != nil {
		return NativeAccessTransitionResult{}, err
	}
	if err := validateAccessTransitionSnapshotDigests(snapshotDigests, persistedPlan.Authorization.SnapshotDigest); err != nil {
		return NativeAccessTransitionResult{}, err
	}
	return NativeAccessTransitionResult{
		PlanID: createdPlan.ID.String(), CandidateID: built.CandidateID.String(), GenerationID: publication.GenerationID.String(),
		PublicationID: publication.ID.String(), ApprovalRequestID: requestID,
		PlanPolicySnapshotDigest: snapshotDigests.PlanPolicySnapshotDigest, ServingPolicySnapshotDigest: snapshotDigests.ServingPolicySnapshotDigest,
		Status: publication.Status,
	}, nil
}

// replayActivatedAccessTransition recovers the receipt when the publication
// worker committed and activated it but the host lost the command's stdout
// before recording its private checkpoint. It uses only exact terminal
// idempotency records and the active target pointer; it cannot create or
// approve a replacement publication.
func (m *Module) replayActivatedAccessTransition(
	ctx context.Context,
	request NativeAccessTransitionRequest,
	target depauth.DeliveryOperatorSnapshot,
) (NativeAccessTransitionResult, error) {
	var empty NativeAccessTransitionResult
	if target.ActiveGenerationID == "" || target.ActivePublicationID == "" {
		return empty, fmt.Errorf("%w: advanced target has no active publication to recover", deployment.ErrDeliveryConflict)
	}
	predecessor, err := m.nativeDeliveryReader.LoadGeneration(ctx, request.ExpectedActiveGenerationID)
	if err != nil {
		return empty, err
	}
	if predecessor.GenerationID != request.ExpectedActiveGenerationID || predecessor.TargetID != request.TargetID || predecessor.CandidateID == "" || predecessor.PlanID == "" {
		return empty, fmt.Errorf("%w: admitted access-transition predecessor is unavailable", deployment.ErrDeliveryConflict)
	}
	predecessorPlanRow, err := m.nativeDeliveryReader.LoadPlan(ctx, predecessor.PlanID)
	if err != nil {
		return empty, err
	}
	var predecessorPlan deployment.DeliveryPlan
	if err := json.Unmarshal(predecessorPlanRow.PlanDocument, &predecessorPlan); err != nil {
		return empty, fmt.Errorf("decode retained predecessor plan: %w", err)
	}
	if predecessorPlan.ID != predecessor.PlanID || predecessorPlan.TargetID != request.TargetID || predecessorPlan.ProjectID != request.ProjectID || predecessorPlan.Environment != request.Environment ||
		predecessorPlan.SourceOwnerID == "" || predecessorPlan.SourceDigest == "" || predecessorPlan.Provenance.AttestationDigest == "" {
		return empty, fmt.Errorf("%w: predecessor does not retain an exact source attestation", deployment.ErrDeliveryConflict)
	}

	activeGeneration, err := m.nativeDeliveryReader.LoadGeneration(ctx, target.ActiveGenerationID)
	if err != nil {
		return empty, err
	}
	if activeGeneration.GenerationID != target.ActiveGenerationID || activeGeneration.TargetID != request.TargetID || activeGeneration.CandidateID == "" || activeGeneration.PlanID == "" {
		return empty, fmt.Errorf("%w: advanced target generation is incomplete", deployment.ErrDeliveryConflict)
	}
	activePlanRow, err := m.nativeDeliveryReader.LoadPlan(ctx, activeGeneration.PlanID)
	if err != nil {
		return empty, err
	}
	var activePlan deployment.DeliveryPlan
	if err := json.Unmarshal(activePlanRow.PlanDocument, &activePlan); err != nil {
		return empty, fmt.Errorf("decode active access-transition plan: %w", err)
	}
	planKey := accessTransitionIdempotencyKey(request.IntentDigest, "plan")
	buildKey := accessTransitionIdempotencyKey(request.IntentDigest, "build")
	publicationKey := accessTransitionIdempotencyKey(request.IntentDigest, "publish")
	approvalKey := accessTransitionIdempotencyKey(request.IntentDigest, "approval-request")
	if activePlan.ID != activeGeneration.PlanID || activePlan.TargetID != request.TargetID || activePlan.ProjectID != request.ProjectID || activePlan.Environment != request.Environment ||
		activePlan.Operation != deployment.DeliveryOperationPolicyChange || activePlan.SourceOwnerID != predecessorPlan.SourceOwnerID ||
		activePlan.SourceDigest != predecessorPlan.SourceDigest || activePlan.Provenance.AttestationDigest != predecessorPlan.Provenance.AttestationDigest ||
		activePlan.ActorID != request.PublisherPrincipalID || activePlan.BaseGenerationID != request.ExpectedActiveGenerationID ||
		activePlan.Digest != activeGeneration.PlanDigest || activePlan.Digest != activePlanRow.PlanDigest ||
		activePlan.Authorization == nil || !validAccessTransitionDigest(activePlan.Authorization.SnapshotDigest) ||
		!activePlan.Governance.RequiresApproval || activePlan.Governance.ApprovalPolicyRevision <= 0 {
		return empty, fmt.Errorf("%w: active generation is not the admitted approved transition plan", deployment.ErrDeliveryConflict)
	}

	planRequest := NativeDeliveryPlanRequest{
		ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment,
		PrincipalID: request.PublisherPrincipalID, SourceOwnerID: predecessorPlan.SourceOwnerID,
		Operation: string(deployment.DeliveryOperationPolicyChange), SourceDigest: predecessorPlan.SourceDigest,
		SourceAttestationDigest: predecessorPlan.Provenance.AttestationDigest, IdempotencyKey: planKey,
	}
	planRequestDigest, err := NativeDeliveryPlanRequestDigest(planRequest)
	if err != nil {
		return empty, err
	}
	planOperation, err := m.lookupCompletedAccessTransitionOperation(ctx, NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: "delivery.plan.create", IdempotencyKey: planKey, RequestDigest: planRequestDigest, OwnerID: request.PublisherPrincipalID,
	})
	if err != nil {
		return empty, err
	}
	var planOutcome struct {
		OperationID             string `json:"operationId"`
		PlanID                  string `json:"planId"`
		ProjectID               string `json:"projectId"`
		TargetID                string `json:"targetId"`
		SourceDigest            string `json:"sourceDigest"`
		SourceAttestationDigest string `json:"sourceAttestationDigest"`
		Status                  string `json:"status"`
		PlanDigest              string `json:"planDigest"`
	}
	if err := json.Unmarshal(planOperation.Outcome, &planOutcome); err != nil || planOutcome.Status != "accepted" || planOutcome.OperationID != activePlan.ID || planOutcome.PlanID != activePlan.ID ||
		planOutcome.ProjectID != request.ProjectID.String() || planOutcome.TargetID != request.TargetID || planOutcome.SourceDigest != predecessorPlan.SourceDigest ||
		planOutcome.SourceAttestationDigest != predecessorPlan.Provenance.AttestationDigest || planOutcome.PlanDigest != activePlan.Digest {
		return empty, fmt.Errorf("%w: persisted plan idempotency outcome differs from the active transition", deployment.ErrDeliveryConflict)
	}

	planID, err := uuid.Parse(activePlan.ID)
	if err != nil || planID == uuid.Nil || planID.String() != activePlan.ID {
		return empty, fmt.Errorf("%w: active transition plan identity is invalid", deployment.ErrDeliveryConflict)
	}
	buildRequest := NativeDeliveryBuildRequest{
		ProjectID: request.ProjectID, TargetID: request.TargetID, Environment: request.Environment,
		PlanID: planID, PrincipalID: request.PublisherPrincipalID, IdempotencyKey: buildKey,
	}
	buildRequestDigest, err := NativeDeliveryBuildRequestDigest(buildRequest)
	if err != nil {
		return empty, err
	}
	buildOperation, err := m.lookupCompletedAccessTransitionOperation(ctx, NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: "delivery.plan.build", IdempotencyKey: buildKey, RequestDigest: buildRequestDigest, OwnerID: request.PublisherPrincipalID,
	})
	if err != nil {
		return empty, err
	}
	var buildOutcome struct {
		PlanID, CandidateID, GenerationID                  string
		ProjectID, TargetID, Environment                   string
		ActorID, IdempotencyKey, RequestDigest, PlanDigest string
	}
	if err := json.Unmarshal(buildOperation.Outcome, &buildOutcome); err != nil || buildOutcome.PlanID != activePlan.ID || buildOutcome.CandidateID != activeGeneration.CandidateID ||
		buildOutcome.GenerationID != activeGeneration.GenerationID || buildOutcome.ProjectID != request.ProjectID.String() || buildOutcome.TargetID != request.TargetID ||
		buildOutcome.Environment != request.Environment || buildOutcome.ActorID != request.PublisherPrincipalID || buildOutcome.IdempotencyKey != buildKey ||
		buildOutcome.RequestDigest != buildRequestDigest || buildOutcome.PlanDigest != activePlan.Digest {
		return empty, fmt.Errorf("%w: persisted build idempotency outcome differs from the active transition", deployment.ErrDeliveryConflict)
	}

	publicationDigest := nativeOperationDigest("publish", request.ProjectID.String(), request.TargetID, request.Environment,
		activeGeneration.CandidateID, uuid.Nil.String(), request.PublisherPrincipalID, publicationKey)
	publicationOperation, err := m.lookupCompletedAccessTransitionOperation(ctx, NativeOperationAcquireInput{
		Scope: request.TargetID, OperationType: "delivery.publication.create", IdempotencyKey: publicationKey, RequestDigest: publicationDigest, OwnerID: request.PublisherPrincipalID,
	})
	if err != nil {
		return empty, err
	}
	var publicationOutcome struct {
		PublicationID string `json:"publicationId"`
	}
	if publicationOperation.OperationID != target.ActivePublicationID ||
		json.Unmarshal(publicationOperation.Outcome, &publicationOutcome) != nil || publicationOutcome.PublicationID != target.ActivePublicationID {
		return empty, fmt.Errorf("%w: persisted publication idempotency outcome differs from the active transition", deployment.ErrDeliveryConflict)
	}
	publication, err := m.nativeDeliveryReader.LoadPublication(ctx, target.ActivePublicationID)
	if err != nil {
		return empty, err
	}
	if publication.PublicationID != target.ActivePublicationID || publication.TargetID != request.TargetID || publication.GenerationID != target.ActiveGenerationID ||
		publication.CandidateID != activeGeneration.CandidateID || publication.ExpectedBaseGenerationID != request.ExpectedActiveGenerationID ||
		publication.ActorID != request.PublisherPrincipalID || publication.RequestDigest != publicationDigest || publication.State != "committed" {
		return empty, fmt.Errorf("%w: active publication is not the exact admitted committed transition", deployment.ErrDeliveryConflict)
	}

	requestID := deterministicApprovalUUID("request:" + publication.PublicationID + ":" + approvalKey)
	approval, err := m.nativeDeliveryApproval.GetPublicationApproval(ctx, NativeApprovalLookup{
		ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment,
		PublicationID: publication.PublicationID, RequestID: requestID,
	})
	if err != nil {
		return empty, err
	}
	if approval.RequestID != requestID || approval.PublicationID != publication.PublicationID || approval.TargetID != request.TargetID ||
		approval.CandidateID != activeGeneration.CandidateID || approval.GenerationID != activeGeneration.GenerationID ||
		approval.RequestedBy.PrincipalID != request.PublisherPrincipalID || approval.LatestDecision == nil ||
		approval.LatestDecision.Decision != depauth.ApprovalActionApprove || approval.LatestDecision.DecidedBy.PrincipalID != request.ReviewerPrincipalID {
		return empty, fmt.Errorf("%w: active transition lacks the exact independent reviewer approval", depauth.ErrApprovalConflict)
	}
	_, snapshotDigests, err := request.ApprovalContext(ctx, AccessTransitionApprovalScope{
		TargetID: request.TargetID, Environment: request.Environment, ProjectID: request.ProjectID,
		ExpectedActiveGenerationID: request.ExpectedActiveGenerationID, CandidateID: activeGeneration.CandidateID,
		CandidateGenerationID: activeGeneration.GenerationID, PublicationID: publication.PublicationID,
		PlanPolicySnapshotDigest: activePlan.Authorization.SnapshotDigest, IntentDigest: request.IntentDigest,
		PublisherPrincipalID: request.PublisherPrincipalID, ReviewerPrincipalID: request.ReviewerPrincipalID,
	})
	if err != nil {
		return empty, err
	}
	if err := validateAccessTransitionSnapshotDigests(snapshotDigests, activePlan.Authorization.SnapshotDigest); err != nil {
		return empty, err
	}
	return NativeAccessTransitionResult{
		PlanID: activePlan.ID, CandidateID: activeGeneration.CandidateID, GenerationID: activeGeneration.GenerationID,
		PublicationID: publication.PublicationID, ApprovalRequestID: requestID,
		PlanPolicySnapshotDigest: snapshotDigests.PlanPolicySnapshotDigest, ServingPolicySnapshotDigest: snapshotDigests.ServingPolicySnapshotDigest,
		Status: publication.State,
	}, nil
}

func validateAccessTransitionSnapshotDigests(digests AccessTransitionSnapshotDigests, expectedPlanDigest string) error {
	if !validAccessTransitionDigest(expectedPlanDigest) || digests.PlanPolicySnapshotDigest != expectedPlanDigest ||
		!validAccessTransitionDigest(digests.ServingPolicySnapshotDigest) {
		return fmt.Errorf("%w: candidate authorization snapshot does not match the transition plan", deployment.ErrDeliveryConflict)
	}
	return nil
}

type nativeAccessTransitionOperationLookup interface {
	Lookup(context.Context, NativeOperationAcquireInput) (NativeOperationRecord, bool, error)
}

func (m *Module) lookupCompletedAccessTransitionOperation(ctx context.Context, input NativeOperationAcquireInput) (NativeOperationRecord, error) {
	lookup, ok := m.nativeOperationAuthority.(nativeAccessTransitionOperationLookup)
	if !ok {
		return NativeOperationRecord{}, fmt.Errorf("%w: exact transition replay lookup is unavailable", ErrDeliveryInputUnavailable)
	}
	record, found, err := lookup.Lookup(ctx, input)
	if err != nil {
		return NativeOperationRecord{}, err
	}
	if !found || record.Scope != input.Scope || record.OperationType != input.OperationType || record.IdempotencyKey != input.IdempotencyKey ||
		record.RequestDigest != input.RequestDigest || (input.OwnerID != "" && record.OwnerID != input.OwnerID) ||
		record.State != NativeOperationStateCompleted || len(record.Outcome) == 0 {
		return NativeOperationRecord{}, fmt.Errorf("%w: exact transition operation is not durably completed", deployment.ErrDeliveryConflict)
	}
	return record, nil
}

func approvalDecisionRevision(approval depauth.ApprovalRequest) int64 {
	if approval.LatestDecision == nil {
		return 0
	}
	return approval.LatestDecision.Revision
}

func validAccessTransitionDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func accessTransitionIdempotencyKey(intentDigest, action string) string {
	return "access-transition:" + strings.TrimPrefix(intentDigest, "sha256:")[:16] + ":" + action
}
