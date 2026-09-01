package module

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func nativeDeliveryHandlerModule(port NativeDeliveryMutationPort) *Module {
	return &Module{
		nativeDeliveryMutations: port,
		instanceID:              "target",
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
	called := false
	port := NativeDeliveryMutationFuncs{Build: func(_ context.Context, request NativeDeliveryBuildRequest) (NativeDeliveryBuild, error) {
		called = true
		if request.ProjectID != projectID || request.TargetID != "target" || request.Environment != "prod" || request.PlanID != planID || request.PrincipalID != "operator" || request.IdempotencyKey != "build-key" {
			t.Fatalf("native build request = %#v", request)
		}
		now := time.Now().UTC()
		return NativeDeliveryBuild{ID: attemptID, PlanID: planID, PlanDigest: nativeDigest('a'), SourceDigest: nativeDigest('b'), ExecutionDigest: nativeDigest('c'), PhysicalPoolID: "pool", WriterLeaseID: leaseID, CandidateID: candidateID, SealID: sealID, ServingArtifactID: "artifact-native", ServingArtifactDigest: nativeDigest('d'), ServingStateID: generationID, Status: string(deploymentgen.DeliveryBuildStatusSealed), CreatedAt: now, UpdatedAt: now, TerminalAt: now, Revision: 7, CandidateRevision: 13}, nil
	}}
	m := nativeDeliveryHandlerModule(port)
	recorder := httptest.NewRecorder()
	m.BuildDeliveryPlan(recorder, httptest.NewRequest(http.MethodPost, "/", nil), "finance", planID.String(), "build-key")
	if recorder.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
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
