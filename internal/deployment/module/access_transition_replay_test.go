package module

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
)

func TestNativeAccessTransitionReplaysOnlyExactActivatedPublication(t *testing.T) {
	t.Run("exact durable transition replays its receipt", func(t *testing.T) {
		fixture, operations := newActivatedAccessTransitionFixture(t)
		approvalContextCalls := 0
		fixture.request.ApprovalContext = func(ctx context.Context, scope AccessTransitionApprovalScope) (context.Context, AccessTransitionSnapshotDigests, error) {
			approvalContextCalls++
			if scope.ExpectedActiveGenerationID != fixture.request.ExpectedActiveGenerationID ||
				scope.CandidateID != "0198f2c0-7c7a-7f00-8a11-000000000314" ||
				scope.CandidateGenerationID != fixture.generationID.String() ||
				scope.PublicationID != "0198f2c0-7c7a-7f00-8a11-000000000305" ||
				scope.PlanPolicySnapshotDigest != fixture.transitionPlan.Authorization.SnapshotDigest ||
				scope.IntentDigest != fixture.request.IntentDigest ||
				scope.PublisherPrincipalID != fixture.request.PublisherPrincipalID ||
				scope.ReviewerPrincipalID != fixture.request.ReviewerPrincipalID {
				return ctx, AccessTransitionSnapshotDigests{}, errors.New("replayed approval scope changed")
			}
			return ctx, AccessTransitionSnapshotDigests{
				PlanPolicySnapshotDigest:    fixture.transitionPlan.Authorization.SnapshotDigest,
				ServingPolicySnapshotDigest: fixture.servingPolicySnapshotDigest,
			}, nil
		}

		result, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "committed" || result.GenerationID != fixture.generationID.String() ||
			result.CandidateID != "0198f2c0-7c7a-7f00-8a11-000000000314" ||
			result.PublicationID != "0198f2c0-7c7a-7f00-8a11-000000000305" ||
			result.PlanPolicySnapshotDigest != fixture.transitionPlan.Authorization.SnapshotDigest ||
			result.ServingPolicySnapshotDigest != fixture.servingPolicySnapshotDigest || approvalContextCalls != 1 {
			t.Fatalf("replayed result = %+v, approval context calls = %d", result, approvalContextCalls)
		}
		if fixture.buildCalls != 0 || fixture.publishCalls != 0 || fixture.approvalCalls != 0 || operations.lookupCalls != 3 {
			t.Fatalf("replay crossed mutation boundary: builds=%d publishes=%d approvals=%d lookups=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls, operations.lookupCalls)
		}
	})

	t.Run("active pointer cannot borrow another publication idempotency result", func(t *testing.T) {
		fixture, operations := newActivatedAccessTransitionFixture(t)
		operations.records["delivery.publication.create"] = accessTransitionCompletedRecord(t,
			operations.records["delivery.publication.create"], map[string]string{"publicationId": "0198f2c0-7c7a-7f00-8a11-000000000399"})
		_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
		if !errors.Is(err, deployment.ErrDeliveryConflict) {
			t.Fatalf("mismatched publication idempotency result error = %v, want delivery conflict", err)
		}
		if fixture.buildCalls != 0 || fixture.publishCalls != 0 || fixture.approvalCalls != 0 {
			t.Fatalf("mismatched replay crossed mutation boundary: builds=%d publishes=%d approvals=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls)
		}
	})

	t.Run("active pointer must match the operation primary identity", func(t *testing.T) {
		fixture, operations := newActivatedAccessTransitionFixture(t)
		record := operations.records["delivery.publication.create"]
		record.OperationID = "0198f2c0-7c7a-7f00-8a11-000000000399"
		operations.records["delivery.publication.create"] = record
		_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
		if !errors.Is(err, deployment.ErrDeliveryConflict) {
			t.Fatalf("mismatched publication operation identity error = %v, want delivery conflict", err)
		}
		if fixture.buildCalls != 0 || fixture.publishCalls != 0 || fixture.approvalCalls != 0 {
			t.Fatalf("mismatched operation identity crossed mutation boundary: builds=%d publishes=%d approvals=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls)
		}
	})

	t.Run("different intent cannot replay the activated result", func(t *testing.T) {
		fixture, _ := newActivatedAccessTransitionFixture(t)
		fixture.request.IntentDigest = nativeReadDigest('0')
		_, err := fixture.module.ExecuteNativeAccessTransition(t.Context(), fixture.request)
		if !errors.Is(err, deployment.ErrDeliveryConflict) {
			t.Fatalf("different intent replay error = %v, want delivery conflict", err)
		}
		if fixture.buildCalls != 0 || fixture.publishCalls != 0 || fixture.approvalCalls != 0 {
			t.Fatalf("different intent crossed mutation boundary: builds=%d publishes=%d approvals=%d", fixture.buildCalls, fixture.publishCalls, fixture.approvalCalls)
		}
	})
}

func newActivatedAccessTransitionFixture(t *testing.T) (*nativeAccessTransitionFixture, *nativeAccessTransitionOperationLookupStub) {
	t.Helper()
	fixture := newNativeAccessTransitionFixture(t, true)
	baseReader := fixture.module.nativeDeliveryReader.(nativeAccessTransitionReader)
	const (
		candidateID   = "0198f2c0-7c7a-7f00-8a11-000000000314"
		publicationID = "0198f2c0-7c7a-7f00-8a11-000000000305"
	)
	activePlan := fixture.transitionPlan
	activePlan.ActorID = fixture.request.PublisherPrincipalID
	activePlan, err := deployment.NewDeliveryPlan(activePlan)
	if err != nil {
		t.Fatal(err)
	}
	activePlanRow := nativeAccessTransitionPlanRow(t, baseReader.transitionRow, activePlan)
	activeGeneration := nativepostgres.DeliveryGeneration{
		GenerationID: fixture.generationID.String(), TargetID: fixture.request.TargetID,
		CandidateID: candidateID, PlanID: activePlan.ID, PlanDigest: activePlan.Digest,
	}
	target := baseReader.target
	target.ActiveGenerationID = fixture.generationID.String()
	target.ActivePublicationID = publicationID
	publicationKey := accessTransitionIdempotencyKey(fixture.request.IntentDigest, "publish")
	publicationDigest := nativeOperationDigest("publish", fixture.request.ProjectID.String(), fixture.request.TargetID,
		fixture.request.Environment, candidateID, uuid.Nil.String(), fixture.request.PublisherPrincipalID, publicationKey)
	activePublication := nativepostgres.DeliveryPublication{
		PublicationID: publicationID, TargetID: fixture.request.TargetID, GenerationID: fixture.generationID.String(),
		ExpectedBaseGenerationID: fixture.request.ExpectedActiveGenerationID, CandidateID: candidateID,
		ActorID: fixture.request.PublisherPrincipalID, State: "committed", RequestDigest: publicationDigest,
	}
	fixture.module.nativeDeliveryReader = nativeActivatedAccessTransitionReader{
		nativeAccessTransitionReader: baseReader, activeGeneration: activeGeneration,
		activePlanRow: activePlanRow, activePublication: activePublication, target: target,
	}
	approvalKey := accessTransitionIdempotencyKey(fixture.request.IntentDigest, "approval-request")
	requestID := deterministicApprovalUUID("request:" + publicationID + ":" + approvalKey)
	fixture.module.nativeDeliveryApproval = nativeAccessTransitionApprovalStub{
		publicationID: publicationID, generationID: fixture.generationID.String(), requestID: requestID,
		candidateID: candidateID, targetID: fixture.request.TargetID,
		latest: &nativepostgres.ApprovalDecision{
			DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000315", RequestID: requestID, Revision: 2,
			Decision: nativepostgres.ApprovalActionApprove, DecidedBy: nativepostgres.ApprovalActor{PrincipalID: fixture.request.ReviewerPrincipalID},
		},
	}
	operations := &nativeAccessTransitionOperationLookupStub{records: make(map[string]NativeOperationRecord)}
	planKey := accessTransitionIdempotencyKey(fixture.request.IntentDigest, "plan")
	var predecessorPlan deployment.DeliveryPlan
	if err := json.Unmarshal(baseReader.predecessorRow.PlanDocument, &predecessorPlan); err != nil {
		t.Fatal(err)
	}
	planRequest := NativeDeliveryPlanRequest{
		ProjectID: fixture.request.ProjectID, TargetID: fixture.request.TargetID, Environment: fixture.request.Environment,
		PrincipalID: fixture.request.PublisherPrincipalID, SourceOwnerID: predecessorPlan.SourceOwnerID,
		Operation: string(deployment.DeliveryOperationPolicyChange), SourceDigest: predecessorPlan.SourceDigest,
		SourceAttestationDigest: predecessorPlan.Provenance.AttestationDigest, IdempotencyKey: planKey,
	}
	planRequestDigest, err := NativeDeliveryPlanRequestDigest(planRequest)
	if err != nil {
		t.Fatal(err)
	}
	planOutcome := map[string]string{
		"operationId": activePlan.ID, "planId": activePlan.ID, "projectId": fixture.request.ProjectID.String(),
		"targetId": fixture.request.TargetID, "sourceDigest": predecessorPlan.SourceDigest,
		"sourceAttestationDigest": predecessorPlan.Provenance.AttestationDigest,
		"status":                  "accepted", "planDigest": activePlan.Digest,
	}
	operations.records["delivery.plan.create"] = accessTransitionRecord(t, fixture.request.TargetID, "delivery.plan.create", planKey, planRequestDigest, planOutcome)
	buildKey := accessTransitionIdempotencyKey(fixture.request.IntentDigest, "build")
	buildRequestDigest, err := NativeDeliveryBuildRequestDigest(NativeDeliveryBuildRequest{
		ProjectID: fixture.request.ProjectID, TargetID: fixture.request.TargetID, Environment: fixture.request.Environment,
		PlanID: uuid.MustParse(activePlan.ID), PrincipalID: fixture.request.PublisherPrincipalID, IdempotencyKey: buildKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	buildOutcome := map[string]string{
		"planId": activePlan.ID, "candidateId": candidateID, "generationId": fixture.generationID.String(),
		"projectId": fixture.request.ProjectID.String(), "targetId": fixture.request.TargetID, "environment": fixture.request.Environment,
		"actorId": fixture.request.PublisherPrincipalID, "idempotencyKey": buildKey,
		"requestDigest": buildRequestDigest, "planDigest": activePlan.Digest,
	}
	operations.records["delivery.plan.build"] = accessTransitionRecord(t, fixture.request.TargetID, "delivery.plan.build", buildKey, buildRequestDigest, buildOutcome)
	publicationRecord := accessTransitionRecord(t, fixture.request.TargetID,
		"delivery.publication.create", publicationKey, publicationDigest, map[string]string{"publicationId": publicationID})
	publicationRecord.OperationID = publicationID
	operations.records["delivery.publication.create"] = publicationRecord
	fixture.module.nativeOperationAuthority = operations
	return fixture, operations
}

type nativeActivatedAccessTransitionReader struct {
	nativeAccessTransitionReader
	activeGeneration  nativepostgres.DeliveryGeneration
	activePlanRow     nativepostgres.DeliveryPlan
	activePublication nativepostgres.DeliveryPublication
	target            nativepostgres.DeliveryOperatorSnapshot
}

func (r nativeActivatedAccessTransitionReader) OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	return r.target, nil
}

func (r nativeActivatedAccessTransitionReader) LoadGeneration(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	if id == r.activeGeneration.GenerationID {
		return r.activeGeneration, nil
	}
	return r.nativeAccessTransitionReader.LoadGeneration(ctx, id)
}

func (r nativeActivatedAccessTransitionReader) LoadPlan(ctx context.Context, id string) (nativepostgres.DeliveryPlan, error) {
	if id == r.activePlanRow.PlanID {
		return r.activePlanRow, nil
	}
	return r.nativeAccessTransitionReader.LoadPlan(ctx, id)
}

func (r nativeActivatedAccessTransitionReader) LoadPublication(_ context.Context, id string) (nativepostgres.DeliveryPublication, error) {
	if id != r.activePublication.PublicationID {
		return nativepostgres.DeliveryPublication{}, nativepostgres.ErrNotFound
	}
	return r.activePublication, nil
}

type nativeAccessTransitionOperationLookupStub struct {
	records     map[string]NativeOperationRecord
	lookupCalls int
}

func (s *nativeAccessTransitionOperationLookupStub) Lookup(_ context.Context, input NativeOperationAcquireInput) (NativeOperationRecord, bool, error) {
	s.lookupCalls++
	record, ok := s.records[input.OperationType]
	if !ok || record.Scope != input.Scope || record.IdempotencyKey != input.IdempotencyKey || record.RequestDigest != input.RequestDigest || record.OwnerID != input.OwnerID {
		return NativeOperationRecord{}, false, nil
	}
	return record, true, nil
}

func (*nativeAccessTransitionOperationLookupStub) AcquireTx(context.Context, NativeOperationTx, NativeOperationAcquireInput) (NativeOperationAcquireResult, error) {
	return NativeOperationAcquireResult{}, errors.New("unexpected operation acquisition")
}

func (*nativeAccessTransitionOperationLookupStub) CompleteTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error {
	return errors.New("unexpected operation completion")
}

func accessTransitionRecord(t *testing.T, scope, operationType, key, digest string, outcome any) NativeOperationRecord {
	t.Helper()
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	return NativeOperationRecord{
		Scope: scope, OperationType: operationType, IdempotencyKey: key, RequestDigest: digest, OwnerID: "publisher",
		OperationID: "0198f2c0-7c7a-7f00-8a11-000000000316", State: NativeOperationStateCompleted, Outcome: raw,
	}
}

func accessTransitionCompletedRecord(t *testing.T, record NativeOperationRecord, outcome any) NativeOperationRecord {
	t.Helper()
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	record.Outcome = raw
	return record
}
