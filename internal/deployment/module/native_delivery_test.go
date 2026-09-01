package module

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenruntime "github.com/flidai/leapview/internal/app/api/apigenruntime"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type nativeDeliveryTestPreparationLease struct {
	ctx      context.Context
	released *bool
}

func (l nativeDeliveryTestPreparationLease) Context() context.Context { return l.ctx }
func (l nativeDeliveryTestPreparationLease) Release() {
	if l.released != nil {
		*l.released = true
	}
}

func nativeDeliveryHandlerModule(port NativeDeliveryMutationPort) *Module {
	return &Module{
		nativeDeliveryMutations: port,
		candidateAdmission: CandidatePreparationAdmitterFunc(func(ctx context.Context) (CandidatePreparationLease, error) {
			return nativeDeliveryTestPreparationLease{ctx: ctx}, nil
		}),
		instanceID: "target",
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, true
			},
		}),
	}
}

func nativeDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func TestNativeDeliveryPlanHandlerUsesInjectedUUIDPort(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	planID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000101")
	called := false
	port := NativeDeliveryMutationFuncs{Plan: func(_ context.Context, request NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
		called = true
		if request.ProjectID != projectID || request.TargetID != "target" || request.Environment != "prod" || request.PrincipalID != "operator" || request.IdempotencyKey != "plan-key" {
			t.Fatalf("native plan request = %#v", request)
		}
		return NativeDeliveryPlan{
			ID: planID, ProjectID: projectID, TargetID: request.TargetID, Environment: request.Environment,
			Operation: request.Operation, SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest,
			ExecutionDigest: nativeDigest('b'), ProvenanceDigest: nativeDigest('c'), GovernanceDigest: nativeDigest('d'), EvidenceDigest: nativeDigest('e'), PlanDigest: nativeDigest('f'),
			Status: string(deploymentgen.DeliveryPlanStatusPlanned), ExpiresAt: time.Now().UTC().Add(time.Hour), CreatedAt: time.Now().UTC(),
		}, nil
	}}
	m := nativeDeliveryHandlerModule(port)
	body, _ := json.Marshal(deploymentgen.DeliveryPlanRequest{TargetId: "target", SourceDigest: nativeDigest('a'), SourceAttestationDigest: nativeDigest('1')})
	recorder := httptest.NewRecorder()
	m.CreateDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), "finance", "plan-key")
	if recorder.Code != http.StatusCreated || !called {
		t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); !strings.HasSuffix(got, planID.String()) {
		t.Fatalf("location = %q", got)
	}
	var response deploymentgen.DeliveryPlanPreviewResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Id != planID.String() || response.ProjectId != projectID.String() {
		t.Fatalf("response identity = %#v", response)
	}
}

func TestNativeDeliveryBuildHandlerUsesInjectedUUIDPort(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	planID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000102")
	attemptID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000103")
	leaseID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000104")
	candidateID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000105")
	sealID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000106")
	generationID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000107")
	type admissionContextKey struct{}
	called, acquired, released := false, false, false
	port := NativeDeliveryMutationFuncs{Build: func(ctx context.Context, request NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
		called = true
		if ctx.Value(admissionContextKey{}) != "admitted" || released {
			t.Fatalf("native build did not receive the live admitted context")
		}
		if request.ProjectID != projectID || request.TargetID != "target" || request.Environment != "prod" || request.PlanID != planID || request.PrincipalID != "operator" || request.IdempotencyKey != "build-key" {
			t.Fatalf("native build request = %#v", request)
		}
		now := time.Now().UTC()
		return NativeDeliveryBuild{ID: attemptID, PlanID: planID, PlanDigest: nativeDigest('a'), SourceDigest: nativeDigest('b'), ExecutionDigest: nativeDigest('c'), PhysicalPoolID: "pool", WriterLeaseID: leaseID, CandidateID: candidateID, SealID: sealID, ServingArtifactID: "artifact-native", ServingArtifactDigest: nativeDigest('d'), ServingStateID: generationID, Status: string(deploymentgen.DeliveryBuildStatusSealed), CreatedAt: now, UpdatedAt: now, TerminalAt: now, Revision: 7, CandidateRevision: 13}, nil
	}}
	m := nativeDeliveryHandlerModule(port)
	m.candidateAdmission = CandidatePreparationAdmitterFunc(func(ctx context.Context) (CandidatePreparationLease, error) {
		acquired = true
		return nativeDeliveryTestPreparationLease{ctx: context.WithValue(ctx, admissionContextKey{}, "admitted"), released: &released}, nil
	})
	recorder := httptest.NewRecorder()
	m.BuildDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", planID.String(), "build-key")
	if recorder.Code != http.StatusOK || !called || !acquired || !released {
		t.Fatalf("status = %d, called = %v, acquired = %v, released = %v, body = %s", recorder.Code, called, acquired, released, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); !strings.HasSuffix(got, attemptID.String()) {
		t.Fatalf("location = %q", got)
	}
	var response deploymentgen.DeliveryBuildStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Id != attemptID.String() || response.PlanId != planID.String() || response.WriterLeaseId != leaseID.String() || response.Revision != 7 || response.CandidateRevision == nil || *response.CandidateRevision != 13 {
		t.Fatalf("response identity = %#v", response)
	}
}

func TestNativeDeliveryBuildAdmissionFailureDoesNotInvokeMutation(t *testing.T) {
	called := false
	port := NativeDeliveryMutationFuncs{Build: func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
		called = true
		return NativeDeliveryBuild{}, nil
	}}
	m := nativeDeliveryHandlerModule(port)
	m.candidateAdmission = CandidatePreparationAdmitterFunc(func(context.Context) (CandidatePreparationLease, error) {
		return nil, errors.New("workload admission unavailable")
	})
	recorder := httptest.NewRecorder()
	m.BuildDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", "0198f2c0-7c7a-7f00-8a11-000000000102", "build-key")
	if recorder.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
	}
}

func TestNativeDeliveryBuildRejectsMissingAdmissionContext(t *testing.T) {
	called, released := false, false
	port := NativeDeliveryMutationFuncs{Build: func(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
		called = true
		return NativeDeliveryBuild{}, nil
	}}
	m := nativeDeliveryHandlerModule(port)
	m.candidateAdmission = CandidatePreparationAdmitterFunc(func(context.Context) (CandidatePreparationLease, error) {
		return nativeDeliveryTestPreparationLease{released: &released}, nil
	})
	recorder := httptest.NewRecorder()
	m.BuildDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", "0198f2c0-7c7a-7f00-8a11-000000000102", "build-key")
	if recorder.Code != http.StatusServiceUnavailable || called || !released {
		t.Fatalf("status = %d, called = %v, released = %v, body = %s", recorder.Code, called, released, recorder.Body.String())
	}
}

func TestNativeDeliveryHandlersFailClosedWithoutBuilder(t *testing.T) {
	m := nativeDeliveryHandlerModule(nil)
	recorder := httptest.NewRecorder()
	m.BuildDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", "0198f2c0-7c7a-7f00-8a11-000000000106", "build-key")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "DELIVERY_INPUT_UNAVAILABLE") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicationOnlyPortDoesNotSatisfyPlanBuildReadiness(t *testing.T) {
	publicationPort := NativeDeliveryPublicationFuncs{
		Publish: func(context.Context, NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
			return NativeDeliveryPublication{}, nil
		},
	}
	m := nativeDeliveryHandlerModule(nil)
	m.nativeDeliveryPublication = publicationPort
	body, _ := json.Marshal(deploymentgen.DeliveryPlanRequest{TargetId: "target", SourceDigest: nativeDigest('a'), SourceAttestationDigest: nativeDigest('b')})
	recorder := httptest.NewRecorder()
	m.CreateDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)), "finance", "plan-key")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "DELIVERY_INPUT_UNAVAILABLE") {
		t.Fatalf("publication-only plan response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestNativeDeliveryPublicationHandlersPreferNativePort(t *testing.T) {
	projectID := projectgraph.ResourceID("finance")
	candidateID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000201")
	generationID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000202")
	publicationID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000203")
	eventID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000204")
	auditID := uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000205")
	calledPublish, calledRollback := false, false
	now := time.Now().UTC()
	base := NativeDeliveryPublication{ID: publicationID, OperationID: publicationID, EventID: eventID, AuditID: auditID, ProjectID: projectID, TargetID: "target", Environment: "prod", PlanID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000206"), PlanDigest: nativeDigest('a'), CandidateID: candidateID, GenerationID: generationID, ExpectedTargetRevision: 1, RequestDigest: nativeDigest('b'), ActorID: "operator", Status: "pending", CreatedAt: now}
	port := NativeDeliveryPublicationFuncs{
		Publish: func(_ context.Context, request NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
			calledPublish = true
			if request.ProjectID != projectID || request.CandidateID != candidateID || request.IdempotencyKey != "publish-key" {
				t.Fatalf("publish request = %#v", request)
			}
			return base, nil
		},
		Rollback: func(_ context.Context, request NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error) {
			calledRollback = true
			if request.ProjectID != projectID || request.GenerationID != generationID || request.IdempotencyKey != "rollback-key" {
				t.Fatalf("rollback request = %#v", request)
			}
			return base, nil
		},
	}
	m := nativeDeliveryHandlerModule(nil)
	m.nativeDeliveryPublication = port
	publishRecorder := httptest.NewRecorder()
	m.PublishDeliveryCandidate(publishRecorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", candidateID.String(), "publish-key")
	if publishRecorder.Code != http.StatusAccepted || !calledPublish {
		t.Fatalf("publish response = %d called=%v body=%s", publishRecorder.Code, calledPublish, publishRecorder.Body.String())
	}
	rollbackRecorder := httptest.NewRecorder()
	m.RollbackDeliveryGeneration(rollbackRecorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", generationID.String(), "rollback-key")
	if rollbackRecorder.Code != http.StatusAccepted || !calledRollback {
		t.Fatalf("rollback response = %d called=%v body=%s", rollbackRecorder.Code, calledRollback, rollbackRecorder.Body.String())
	}
}

type nativeGuardMutationPort struct {
	planCompletions  int
	buildCompletions int
	completeErr      error
	plan             NativeDeliveryPlan
	build            NativeDeliveryBuild
}

func (p *nativeGuardMutationPort) CreatePlan(context.Context, NativeDeliveryPlanRequest) (NativeDeliveryPlan, error) {
	return p.plan, nil
}

func (p *nativeGuardMutationPort) BuildPlan(context.Context, NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
	return p.build, nil
}

func (p *nativeGuardMutationPort) CompleteNativePlanCommand(context.Context, NativeDeliveryPlan) error {
	p.planCompletions++
	return p.completeErr
}

func (p *nativeGuardMutationPort) CompleteNativeBuildCommand(context.Context, NativeDeliveryBuild) error {
	p.buildCompletions++
	return p.completeErr
}

type nativeGuardPublicationPort struct {
	publishCompletions  int
	rollbackCompletions int
	completeErr         error
}

func (p *nativeGuardPublicationPort) PublishCandidate(context.Context, NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
	return NativeDeliveryPublication{}, nil
}

func (p *nativeGuardPublicationPort) RollbackGeneration(context.Context, NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error) {
	return NativeDeliveryPublication{}, nil
}

func (p *nativeGuardPublicationPort) CompleteNativePublishCommand(context.Context, NativeDeliveryPublication) error {
	p.publishCompletions++
	return p.completeErr
}

func (p *nativeGuardPublicationPort) CompleteNativeRollbackCommand(context.Context, NativeDeliveryPublication) error {
	p.rollbackCompletions++
	return p.completeErr
}

func nativeGuardContext(t *testing.T, operation string) (context.Context, *apigencommand.Guard) {
	t.Helper()
	contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		t.Fatalf("missing generated contract %q", operation)
	}
	ctx, guard, err := apigencommand.Begin(context.Background(), contract)
	if err != nil {
		t.Fatalf("begin %s: %v", operation, err)
	}
	return ctx, guard
}

func TestNativeDeliveryCommandCompletionMarksGeneratedGuards(t *testing.T) {
	mutation := &nativeGuardMutationPort{}
	publication := &nativeGuardPublicationPort{}
	cases := []struct {
		name string
		op   string
		call func(context.Context) error
		seen func() int
	}{
		{name: "plan", op: deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID(), call: func(ctx context.Context) error {
			return completeNativePlanCommand(ctx, mutation, NativeDeliveryPlan{})
		}, seen: func() int { return mutation.planCompletions }},
		{name: "build", op: deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID(), call: func(ctx context.Context) error {
			return completeNativeBuildCommand(ctx, mutation, NativeDeliveryBuild{})
		}, seen: func() int { return mutation.buildCompletions }},
		{name: "publish", op: deploymentgen.GenCommandOperationPublishDeliveryCandidate().APIGenOperationID(), call: func(ctx context.Context) error {
			return completeNativePublishCommand(ctx, publication, NativeDeliveryPublication{})
		}, seen: func() int { return publication.publishCompletions }},
		{name: "rollback", op: deploymentgen.GenCommandOperationRollbackDeliveryGeneration().APIGenOperationID(), call: func(ctx context.Context) error {
			return completeNativeRollbackCommand(ctx, publication, NativeDeliveryPublication{})
		}, seen: func() int { return publication.rollbackCompletions }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, guard := nativeGuardContext(t, test.op)
			if err := test.call(ctx); err != nil {
				t.Fatalf("completion = %v", err)
			}
			if !guard.Completed() {
				t.Fatal("generated command guard was not completed")
			}
			if test.seen() != 1 {
				t.Fatalf("durable completer calls = %d, want 1", test.seen())
			}
		})
	}
}

func TestGeneratedNativePlanRouteCompletesCommandGuard(t *testing.T) {
	projectID := projectgraph.ResourceID("finance")
	now := time.Now().UTC()
	port := &nativeGuardMutationPort{plan: NativeDeliveryPlan{
		ID: uuid.MustParse("0198f2c0-7c7a-7f00-8a11-000000000301"), ProjectID: projectID, TargetID: "target", Environment: "prod",
		Operation: string(deploymentgen.DeliveryOperationKindCodeChange), SourceDigest: nativeDigest('a'), SourceAttestationDigest: nativeDigest('b'),
		ExecutionDigest: nativeDigest('c'), ProvenanceDigest: nativeDigest('d'), GovernanceDigest: nativeDigest('e'), EvidenceDigest: nativeDigest('f'), PlanDigest: nativeDigest('1'),
		Status: string(deploymentgen.DeliveryPlanStatusPlanned), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	m := nativeDeliveryHandlerModule(port)
	dispatch := func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		if operationID != deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID() {
			return false
		}
		m.CreateDeliveryPlan(w, r, "finance", "0198f2c0-7c7a-7f00-8a11-000000000302")
		return true
	}
	handler, err := apigenruntime.Build(generatedDeliveryAuthorizer{}, dispatch, deploymentgen.GetAPIGenCommandRuntimeContract)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/finance/delivery", strings.NewReader(`{"targetId":"target","sourceDigest":"`+nativeDigest('a')+`","sourceAttestationDigest":"`+nativeDigest('b')+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "0198f2c0-7c7a-7f00-8a11-000000000302")
	route := chi.NewRouteContext()
	route.URLParams.Add("project", "finance")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.HandleAPIGen(deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID(), recorder, request)
	if recorder.Code != http.StatusCreated || strings.Contains(recorder.Body.String(), "COMMAND_CONTRACT_NOT_EXECUTED") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if port.planCompletions != 1 {
		t.Fatalf("plan completer calls = %d, want 1", port.planCompletions)
	}
}

func TestNativeDeliveryCommandCompletionFailsClosedOnMismatchOrVerificationError(t *testing.T) {
	mutation := &nativeGuardMutationPort{completeErr: errors.New("durable evidence mismatch")}
	ctx, guard := nativeGuardContext(t, deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID())
	if err := completeNativePlanCommand(ctx, mutation, NativeDeliveryPlan{}); !errors.Is(err, apigencommand.ErrOperationMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	if guard.Completed() {
		t.Fatal("guard completed after operation mismatch")
	}

	ctx, guard = nativeGuardContext(t, deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID())
	if err := completeNativePlanCommand(ctx, mutation, NativeDeliveryPlan{}); err == nil {
		t.Fatal("expected durable verification failure")
	}
	if guard.Completed() {
		t.Fatal("guard completed after durable verification failure")
	}
}

type nativeGuardApprovalPort struct {
	requestCompletions  int
	decisionCompletions int
	completeErr         error
}

func (p *nativeGuardApprovalPort) RequestPublicationApproval(context.Context, NativeApprovalRequest) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nil
}

func (p *nativeGuardApprovalPort) GetPublicationApproval(context.Context, NativeApprovalLookup) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nil
}

func (p *nativeGuardApprovalPort) ApprovePublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nil
}

func (p *nativeGuardApprovalPort) DenyPublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nil
}

func (p *nativeGuardApprovalPort) RevokePublicationApproval(context.Context, NativeApprovalDecision) (nativepostgres.ApprovalRequest, error) {
	return nativepostgres.ApprovalRequest{}, nil
}

func (p *nativeGuardApprovalPort) CompleteNativeApprovalRequestCommand(context.Context, nativepostgres.ApprovalRequest) error {
	p.requestCompletions++
	return p.completeErr
}

func (p *nativeGuardApprovalPort) CompleteNativeApprovalDecisionCommand(context.Context, nativepostgres.ApprovalRequest, nativepostgres.ApprovalAction) error {
	p.decisionCompletions++
	return p.completeErr
}

func TestNativeApprovalCommandCompletionMarksGeneratedGuards(t *testing.T) {
	port := &nativeGuardApprovalPort{}
	cases := []struct {
		name, operation, action string
	}{
		{name: "request", operation: deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval().APIGenOperationID(), action: string(nativepostgres.ApprovalActionRequest)},
		{name: "approve", operation: deploymentgen.GenCommandOperationApproveDeliveryPublicationApproval().APIGenOperationID(), action: string(nativepostgres.ApprovalActionApprove)},
		{name: "deny", operation: deploymentgen.GenCommandOperationDenyDeliveryPublicationApproval().APIGenOperationID(), action: string(nativepostgres.ApprovalActionDeny)},
		{name: "revoke", operation: deploymentgen.GenCommandOperationRevokeDeliveryPublicationApproval().APIGenOperationID(), action: string(nativepostgres.ApprovalActionRevoke)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, guard := nativeGuardContext(t, test.operation)
			if err := completeNativeApprovalCommand(ctx, port, test.operation, "delivery.publication."+map[string]string{"request": "approval_requested", "approve": "approved", "deny": "denied", "revoke": "approval_revoked"}[test.name], nativepostgres.ApprovalRequest{}, nativepostgres.ApprovalAction(test.action)); err != nil {
				t.Fatalf("completion = %v", err)
			}
			if !guard.Completed() {
				t.Fatal("generated command guard was not completed")
			}
		})
	}
	if port.requestCompletions != 1 || port.decisionCompletions != 3 {
		t.Fatalf("approval completer calls request=%d decision=%d", port.requestCompletions, port.decisionCompletions)
	}
}

func TestNativeApprovalEvidenceAllowsIdempotentReplayProjection(t *testing.T) {
	approval := nativepostgres.ApprovalRequest{
		RequestID: "0198f2c0-7c7a-7f00-8a11-000000000401", PublicationID: "0198f2c0-7c7a-7f00-8a11-000000000402", TargetID: "target",
		CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000403", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000000404", RequestDigest: nativeDigest('a'),
		Evidence: nativepostgres.ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000000405", EventID: "0198f2c0-7c7a-7f00-8a11-000000000406", AuditID: "0198f2c0-7c7a-7f00-8a11-000000000407"},
		LatestDecision: &nativepostgres.ApprovalDecision{
			DecisionID: "0198f2c0-7c7a-7f00-8a11-000000000408", RequestID: "0198f2c0-7c7a-7f00-8a11-000000000401", Decision: nativepostgres.ApprovalActionRevoke,
			Evidence: nativepostgres.ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000000409", EventID: "0198f2c0-7c7a-7f00-8a11-000000000410", AuditID: "0198f2c0-7c7a-7f00-8a11-000000000411"},
		},
	}
	if err := validateNativeApprovalEvidence(approval, nativepostgres.ApprovalActionRequest); err != nil {
		t.Fatalf("request replay validation = %v", err)
	}
	if err := validateNativeApprovalEvidence(approval, nativepostgres.ApprovalActionApprove); err != nil {
		t.Fatalf("decision replay validation = %v", err)
	}
}
