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
		if request.ProjectID != projectID || request.PlanID != planID || request.PrincipalID != "operator" || request.IdempotencyKey != "build-key" {
			t.Fatalf("native build request = %#v", request)
		}
		now := time.Now().UTC()
		return NativeDeliveryBuild{ID: attemptID, PlanID: planID, PlanDigest: nativeDigest('a'), SourceDigest: nativeDigest('b'), ExecutionDigest: nativeDigest('c'), PhysicalPoolID: "pool", WriterLeaseID: leaseID, CandidateID: candidateID, SealID: sealID, ServingArtifactID: "artifact-native", ServingArtifactDigest: nativeDigest('d'), ServingStateID: generationID, Status: string(deploymentgen.DeliveryBuildStatusSealed), CreatedAt: now, UpdatedAt: now, TerminalAt: now, Revision: 1}, nil
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
	if response.Id != attemptID.String() || response.PlanId != planID.String() || response.WriterLeaseId != leaseID.String() {
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
