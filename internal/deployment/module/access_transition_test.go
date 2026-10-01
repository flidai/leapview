package module

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func TestNativeAccessTransitionRequiresPersistedApprovalBeforeBuildAndPublish(t *testing.T) {
	fixture := newNativeAccessTransitionFixture(t, false)
	_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
	if !errors.Is(err, deployment.ErrDeliveryConflict) || !strings.Contains(err.Error(), "independent publication approval") {
		t.Fatalf("unprotected persisted plan error = %v", err)
	}
	if fixture.buildCalls != 0 || fixture.publishCalls != 0 || fixture.approvalCalls != 0 {
		t.Fatalf("unprotected plan crossed later boundaries: builds=%d publishes=%d approvals=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls)
	}
}

func TestNativeAccessTransitionUsesPersistedApprovalDecisionRevision(t *testing.T) {
	fixture := newNativeAccessTransitionFixture(t, true)
	result, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.GenerationID != fixture.generationID.String() ||
		result.PlanPolicySnapshotDigest != fixture.transitionPlan.Authorization.SnapshotDigest ||
		result.ServingPolicySnapshotDigest != fixture.servingPolicySnapshotDigest ||
		result.PlanPolicySnapshotDigest == result.ServingPolicySnapshotDigest {
		t.Fatalf("transition result = %+v", result)
	}
	if fixture.expectedApprovalRevision != 0 || fixture.buildCalls != 1 || fixture.publishCalls != 1 || fixture.approvalCalls != 1 {
		t.Fatalf("transition boundaries build=%d publish=%d approve=%d expectedRevision=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls, fixture.expectedApprovalRevision)
	}
}

func TestNativeAccessTransitionResumesOnlyExactExistingApproval(t *testing.T) {
	fixture := newNativeAccessTransitionFixture(t, true)
	approval := fixture.module.nativeDeliveryApproval.(nativeAccessTransitionApprovalStub)
	approval.latest = &nativepostgres.ApprovalDecision{
		DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000320", Revision: 4,
		Decision: nativepostgres.ApprovalActionApprove, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: "reviewer"},
	}
	fixture.module.nativeDeliveryApproval = approval

	result, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || fixture.approvalCalls != 0 || fixture.approvalContextCalls != 1 ||
		result.PlanPolicySnapshotDigest != fixture.transitionPlan.Authorization.SnapshotDigest ||
		result.ServingPolicySnapshotDigest != fixture.servingPolicySnapshotDigest {
		t.Fatalf("transition result=%+v approval calls=%d approval context calls=%d; exact approval should resume without a new decision and still recheck snapshot evidence", result, fixture.approvalCalls, fixture.approvalContextCalls)
	}
}

func TestNativeAccessTransitionRejectsConflictingExistingApprovalDecision(t *testing.T) {
	tests := []struct {
		name     string
		decision nativepostgres.ApprovalDecision
	}{
		{
			name: "denied by reviewer",
			decision: nativepostgres.ApprovalDecision{
				DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000321", Revision: 4,
				Decision: nativepostgres.ApprovalActionDeny, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: "reviewer"},
			},
		},
		{
			name: "revoked by reviewer",
			decision: nativepostgres.ApprovalDecision{
				DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000322", Revision: 4,
				Decision: nativepostgres.ApprovalActionRevoke, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: "reviewer"},
			},
		},
		{
			name: "approved by another reviewer",
			decision: nativepostgres.ApprovalDecision{
				DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000323", Revision: 4,
				Decision: nativepostgres.ApprovalActionApprove, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: "other-reviewer"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNativeAccessTransitionFixture(t, true)
			approval := fixture.module.nativeDeliveryApproval.(nativeAccessTransitionApprovalStub)
			approval.latest = &test.decision
			fixture.module.nativeDeliveryApproval = approval

			_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
			if !errors.Is(err, nativepostgres.ErrApprovalConflict) {
				t.Fatalf("transition error = %v, want approval conflict", err)
			}
			if fixture.approvalCalls != 0 {
				t.Fatalf("transition overwrote existing decision with %d approval calls", fixture.approvalCalls)
			}
		})
	}
}

func TestNativeAccessTransitionCannotApproveAnAlreadyCommittedUnreviewedPublication(t *testing.T) {
	fixture := newNativeAccessTransitionFixture(t, true)
	fixture.publicationStatus = "committed"
	_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
	if !errors.Is(err, nativepostgres.ErrApprovalConflict) {
		t.Fatalf("committed unreviewed publication error = %v, want approval conflict", err)
	}
	if fixture.approvalCalls != 0 {
		t.Fatalf("transition tried to approve after publication committed: approvals=%d", fixture.approvalCalls)
	}
}

type nativeAccessTransitionFixture struct {
	module                      *Module
	request                     NativeAccessTransitionRequest
	transitionPlan              deployment.DeliveryPlan
	generationID                uuid.UUID
	publicationStatus           string
	servingPolicySnapshotDigest string
	buildCalls, publishCalls    int
	approvalCalls               int
	approvalContextCalls        int
	expectedApprovalRevision    int64
}

func newNativeAccessTransitionFixture(t *testing.T, requiresApproval bool) *nativeAccessTransitionFixture {
	t.Helper()
	const (
		targetID      = "target"
		environment   = "prod"
		publisherID   = "publisher"
		reviewerID    = "reviewer"
		predecessorID = "0198f2c0-7c7a-7f00-8a11-000000000301"
		planID        = "0198f2c0-7c7a-7f00-8a11-000000000302"
		candidateID   = "0198f2c0-7c7a-7f00-8a11-000000000303"
		generationID  = "0198f2c0-7c7a-7f00-8a11-000000000304"
		publicationID = "0198f2c0-7c7a-7f00-8a11-000000000305"
	)
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	baseRows := nativeReadRowsFixture(t, targetID)
	predecessor, err := baseRows.plan.RichPlan()
	if err != nil {
		t.Fatal(err)
	}
	predecessor.ID = predecessorID
	predecessor.Operation = deployment.DeliveryOperationCodeChange
	predecessor.Provenance.AttestationDigest = nativeReadDigest('d')
	predecessor.Digest = ""
	predecessor, err = deployment.NewDeliveryPlan(predecessor)
	if err != nil {
		t.Fatal(err)
	}
	transitionPlan := predecessor
	transitionPlan.ID = planID
	transitionPlan.Operation = deployment.DeliveryOperationPolicyChange
	transitionPlan.BaseGenerationID = predecessorID
	transitionPlan.Governance.RequiresApproval = requiresApproval
	transitionPlan.Authorization = nativeAccessTransitionAuthorization(t, projectID, targetID)
	transitionPlan.Digest = ""
	transitionPlan, err = deployment.NewDeliveryPlan(transitionPlan)
	if err != nil {
		t.Fatal(err)
	}
	predecessorRow := nativeAccessTransitionPlanRow(t, baseRows.plan, predecessor)
	transitionRow := nativeAccessTransitionPlanRow(t, baseRows.plan, transitionPlan)
	activeGenerationID := uuid.MustParse(predecessorID)
	newGenerationID := uuid.MustParse(generationID)
	planUUID := uuid.MustParse(planID)
	candidateUUID := uuid.MustParse(candidateID)
	publicationUUID := uuid.MustParse(publicationID)
	reader := nativeAccessTransitionReader{
		nativeReadFixture: nativeReadFixture{},
		predecessorRow:    predecessorRow, transitionRow: transitionRow,
		predecessor: nativepostgres.DeliveryGeneration{
			GenerationID: predecessorID, TargetID: targetID, CandidateID: candidateID, PlanID: predecessorID,
		},
		target: nativepostgres.DeliveryOperatorSnapshot{
			TargetID: targetID, ProjectID: projectID.String(), Environment: environment,
			ActiveGenerationID: predecessorID, TargetRevision: 9,
		},
	}
	createdAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fixture := &nativeAccessTransitionFixture{
		transitionPlan: transitionPlan, generationID: newGenerationID, publicationStatus: "pending",
		servingPolicySnapshotDigest: nativeReadDigest('b'),
	}
	mutations := NativeDeliveryMutationFuncs{
		Plan: func(_ context.Context, request NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
			return NativeDeliveryPlan{
				ID: planUUID, ProjectID: projectID, TargetID: targetID, Environment: environment,
				Operation: request.Operation, SourceDigest: transitionPlan.SourceDigest,
				SourceAttestationDigest: transitionPlan.Provenance.AttestationDigest,
				PlanDigest:              transitionPlan.Digest, ExecutionDigest: transitionPlan.ExecutionDigest,
				ProvenanceDigest: transitionPlan.ProvenanceDigest, GovernanceDigest: transitionPlan.GovernanceDigest,
				EvidenceDigest: transitionPlan.EvidenceDigest, Status: string(deployment.DeliveryPlanPlanned),
				CreatedAt: transitionPlan.CreatedAt, ExpiresAt: transitionPlan.Governance.ExpiresAt,
			}, nil
		},
		Build: func(_ context.Context, request NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
			fixture.buildCalls++
			return NativeDeliveryBuild{
				ID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000306"), PlanID: request.PlanID,
				PlanDigest: transitionPlan.Digest, SourceDigest: transitionPlan.SourceDigest, ExecutionDigest: transitionPlan.ExecutionDigest,
				PhysicalPoolID: "pool:transition", WriterLeaseID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000307"),
				ServingArtifactID: "artifact-transition", ServingArtifactDigest: nativeReadDigest('a'),
				ServingStateID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000308"),
				Status:         string(deploymentgen.DeliveryBuildStatusSealed), SealID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000309"),
				CandidateID: candidateUUID, CreatedAt: createdAt, UpdatedAt: createdAt, TerminalAt: createdAt,
				Revision: 1, CandidateRevision: 1,
			}, nil
		},
	}
	publication := NativeDeliveryPublicationFuncs{Publish: func(_ context.Context, request NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
		fixture.publishCalls++
		resultRevision := int64(0)
		completedAt := time.Time{}
		if fixture.publicationStatus == "committed" {
			resultRevision = 10
			completedAt = createdAt
		}
		return NativeDeliveryPublication{
			ID: publicationUUID, OperationID: publicationUUID,
			EventID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000310"),
			AuditID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000311"),
			ActorID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey,
			ProjectID: projectID, TargetID: targetID, Environment: environment,
			PlanID: planUUID, PlanDigest: transitionPlan.Digest, CandidateID: request.CandidateID,
			GenerationID: newGenerationID, ExpectedBaseGenerationID: activeGenerationID,
			ExpectedTargetRevision: 9, ResultTargetRevision: resultRevision, RequestDigest: nativeReadDigest('e'),
			Status: fixture.publicationStatus, CreatedAt: createdAt, CompletedAt: completedAt,
		}, nil
	}}
	approval := nativeAccessTransitionApprovalStub{
		publicationID: publicationID, generationID: generationID,
		requestID: deterministicApprovalUUID("request:" + publicationID + ":" + accessTransitionIdempotencyKey(nativeReadDigest('f'), "approval-request")),
		onApprove: func(input NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
			fixture.approvalCalls++
			fixture.expectedApprovalRevision = input.ExpectedRevision
			result := approvalApprovalRequest("publisher", reviewerID, publicationID, generationID, input.RequestID, 5, nativepostgres.ApprovalActionApprove)
			return result, nil
		},
	}
	// Keep the fixture's request digest explicit so the fake durable approval
	// row has the same deterministic identity the transition computes.
	intentDigest := nativeReadDigest('f')
	approval.requestID = deterministicApprovalUUID("request:" + publicationID + ":" + accessTransitionIdempotencyKey(intentDigest, "approval-request"))
	fixture.module = &Module{
		nativeDeliveryReader: reader, nativeDeliveryMutations: mutations,
		nativeDeliveryPublication: publication, nativeDeliveryApproval: approval,
		instanceID: targetID, instanceEnvironment: "prod",
		candidateAdmission: CandidatePreparationAdmitterFunc(func(ctx context.Context) (CandidatePreparationLease, error) {
			return nativeAccessTransitionLease{ctx: ctx}, nil
		}),
	}
	fixture.request = NativeAccessTransitionRequest{
		TargetID: targetID, Environment: environment, ProjectID: projectID,
		IntentDigest: intentDigest, ExpectedActiveGenerationID: predecessorID,
		PublisherPrincipalID: publisherID, ReviewerPrincipalID: reviewerID, OperationID: "access-transition:" + uuid.NewString(),
		PublisherActor: ApprovalActor{PrincipalID: publisherID, CredentialClass: "workload", CredentialID: "publisher-token"},
		ReviewerActor:  ApprovalActor{PrincipalID: reviewerID, CredentialClass: "workload", CredentialID: "reviewer-token"},
		ApprovalContext: func(ctx context.Context, scope AccessTransitionApprovalScope) (context.Context, AccessTransitionSnapshotDigests, error) {
			fixture.approvalContextCalls++
			if scope.TargetID != targetID || scope.ProjectID != projectID || scope.ExpectedActiveGenerationID != predecessorID || scope.CandidateID != candidateID || scope.CandidateGenerationID != generationID || scope.PublicationID != publicationID || scope.IntentDigest != intentDigest || scope.PlanPolicySnapshotDigest != transitionPlan.Authorization.SnapshotDigest {
				return ctx, AccessTransitionSnapshotDigests{}, errors.New("approval scope changed")
			}
			return ctx, AccessTransitionSnapshotDigests{
				PlanPolicySnapshotDigest:    scope.PlanPolicySnapshotDigest,
				ServingPolicySnapshotDigest: fixture.servingPolicySnapshotDigest,
			}, nil
		},
	}
	return fixture
}

func nativeAccessTransitionAuthorization(t *testing.T, projectID projectgraph.ResourceID, targetID string) *deployment.DeliveryAuthorizationExecution {
	t.Helper()
	plan, err := deployment.NewDeliveryAuthorizationPlan(projectID, targetID, nil, nil, nil, []deployment.DeliveryTransition{{Action: access.ActionDeliveryPlan}})
	if err != nil {
		t.Fatal(err)
	}
	plan.SnapshotDigest = nativeReadDigest('c')
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &deployment.DeliveryAuthorizationExecution{
		PlanDigest: digest, SnapshotDigest: plan.SnapshotDigest,
		DeliveryTransitions: []deployment.DeliveryTransition{{Action: access.ActionDeliveryPlan}},
	}
}

func nativeAccessTransitionPlanRow(t *testing.T, template nativepostgres.DeliveryPlan, plan deployment.DeliveryPlan) nativepostgres.DeliveryPlan {
	t.Helper()
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	template.PlanID = plan.ID
	template.TargetID = plan.TargetID
	template.PlanDigest = plan.Digest
	template.CompiledConfigDigest = plan.Execution.ConfigDigest
	template.SecurityDomainFingerprint = plan.Governance.AuthorizationDigest
	template.ArtifactDigest = plan.ServingArtifactDigest
	template.QualificationDigest = plan.Governance.QualificationDigest
	template.ApprovalRequired = plan.Governance.RequiresApproval
	template.ApprovalPolicyRevision = plan.Governance.ApprovalPolicyRevision
	template.PlanDocument = document
	return template
}

type nativeAccessTransitionReader struct {
	nativeReadFixture
	predecessorRow, transitionRow nativepostgres.DeliveryPlan
	predecessor                   nativepostgres.DeliveryGeneration
	target                        nativepostgres.DeliveryOperatorSnapshot
}

func (r nativeAccessTransitionReader) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	return r.target, nil
}

func (r nativeAccessTransitionReader) LoadGeneration(_ context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	if id == r.predecessor.GenerationID {
		return r.predecessor, nil
	}
	return nativepostgres.DeliveryGeneration{}, nativepostgres.ErrNotFound
}

func (r nativeAccessTransitionReader) LoadPlan(_ context.Context, id string) (nativepostgres.DeliveryPlan, error) {
	switch id {
	case r.predecessorRow.PlanID:
		return r.predecessorRow, nil
	case r.transitionRow.PlanID:
		return r.transitionRow, nil
	default:
		return nativepostgres.DeliveryPlan{}, nativepostgres.ErrNotFound
	}
}

type nativeAccessTransitionLease struct{ ctx context.Context }

func (l nativeAccessTransitionLease) Context() context.Context { return l.ctx }
func (nativeAccessTransitionLease) Release()                   {}

type nativeAccessTransitionApprovalStub struct {
	publicationID, generationID, requestID string
	candidateID, targetID                  string
	latest                                 *nativepostgres.ApprovalDecision
	onApprove                              func(NativeApprovalDecision) (nativepostgres.ApprovalRequest, error)
}

func (a nativeAccessTransitionApprovalStub) GetPublicationApproval(context.Context, NativeApprovalLookup) (nativepostgres.ApprovalRequest, error) {
	targetID := a.targetID
	if targetID == "" {
		targetID = "target"
	}
	candidateID := a.candidateID
	if candidateID == "" {
		candidateID = "0198f2c0-7c7a-7f00-8a11-000000000303"
	}
	request := approvalApprovalRequest("publisher", "reviewer", a.publicationID, a.generationID, a.requestID, a.latestRevision(), nativepostgres.ApprovalActionDeny, a.latest)
	request.TargetID = targetID
	request.CandidateID = candidateID
	return request, nil
}

func (a nativeAccessTransitionApprovalStub) RequestPublicationApproval(context.Context, NativeApprovalRequest) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nativepostgres.ErrApprovalConflict
}

func (a nativeAccessTransitionApprovalStub) ApprovePublicationApproval(_ context.Context, input NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return a.onApprove(input)
}

func (nativeAccessTransitionApprovalStub) DenyPublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nativepostgres.ErrApprovalConflict
}

func (nativeAccessTransitionApprovalStub) RevokePublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nativepostgres.ErrApprovalConflict
}

func (a nativeAccessTransitionApprovalStub) latestRevision() int64 {
	if a.latest == nil {
		return 0
	}
	return a.latest.Revision
}

func approvalApprovalRequest(publisher, reviewer, publication, generation, request string, revision int64, action nativepostgres.ApprovalAction, existing ...*nativepostgres.ApprovalDecision) nativepostgres.ApprovalRequest {
	var latest *nativepostgres.ApprovalDecision
	if len(existing) > 0 {
		latest = existing[0]
	} else if revision > 0 {
		latest = &nativepostgres.ApprovalDecision{DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000313", RequestID: request, Revision: revision, Decision: action, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: reviewer}}
	}
	return nativepostgres.ApprovalRequest{
		RequestID: request, PublicationID: publication, TargetID: "target", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000303",
		GenerationID: generation, RequestDigest: nativeReadDigest('e'), RequestedBy: nativepostgres.ApprovalActor{PrincipalID: publisher}, LatestDecision: latest,
	}
}

var _ NativeDeliveryApprovalPort = nativeAccessTransitionApprovalStub{}
