package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/go-chi/chi/v5"
)

func TestPipelineRunResponseForExposesOnlyPipelineContract(t *testing.T) {
	run := refreshrun.RunRecord{
		ID: "run_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusQueued, CreatedAt: "2026-07-19T06:00:00Z",
	}
	response, ok := PipelineRunResponseFor(run)
	if !ok {
		t.Fatal("PipelineRunResponseFor() rejected a root pipeline run")
	}
	if response.PipelineID != "sales-refresh" || response.SemanticModel != "sales" || response.InvocationSource != refreshrun.TriggerManual {
		t.Fatalf("response = %#v", response)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, internalField := range []string{"modelId", "servingStateId", "targetType", "targetId", "triggerType", "triggerId", "trigger", "parentRunId"} {
		if strings.Contains(string(payload), `"`+internalField+`"`) {
			t.Fatalf("public response contains internal field %q: %s", internalField, payload)
		}
	}
}

func TestPipelineRunResponseForRejectsDependencyRun(t *testing.T) {
	_, ok := PipelineRunResponseFor(refreshrun.RunRecord{
		ID: "task_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: refreshrun.TargetModel,
		TargetID: "sales.orders", ParentRunID: "run_1", TriggerType: refreshrun.TriggerDependency,
	})
	if ok {
		t.Fatal("PipelineRunResponseFor() accepted an internal dependency run")
	}
}

func TestPipelineRunResponseForNormalizesSQLiteTimestamps(t *testing.T) {
	response, ok := PipelineRunResponseFor(refreshrun.RunRecord{
		ID: "run_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19 06:00:00",
		StartedAt: "2026-07-19 06:00:00.123", FinishedAt: "2026-07-19T06:01:00+02:00",
	})
	if !ok {
		t.Fatal("PipelineRunResponseFor() rejected a valid pipeline run")
	}
	if response.CreatedAt != "2026-07-19T06:00:00Z" || response.StartedAt != "2026-07-19T06:00:00.123Z" || response.FinishedAt != "2026-07-19T04:01:00Z" {
		t.Fatalf("normalized timestamps = (%q, %q, %q)", response.CreatedAt, response.StartedAt, response.FinishedAt)
	}
}

func TestHandlerSeparatesPipelineVisibilityFromExecutionAuthorization(t *testing.T) {
	repo := &authorizationRunRepository{runs: []refreshrun.RunRecord{{
		ID: "run_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales-refresh",
		TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:00:00Z",
	}}}
	viewChecks := 0
	runChecks := 0
	handler := Handler{
		Repository:      func() (refreshrun.RunRepository, error) { return repo, nil },
		ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) { return testIdentity(), nil },
		AuthorizePipelineView: func(*http.Request, projectgraph.ServingIdentity, string) (bool, error) {
			viewChecks++
			return true, nil
		},
		AuthorizePipelineRun: func(*http.Request, projectgraph.ServingIdentity, string) (bool, error) {
			runChecks++
			return false, nil
		},
	}
	listRequest := withRouteParams(httptest.NewRequest(http.MethodGet, "/api/v1/projects/sales/refresh-runs", nil), map[string]string{"project": "sales"})
	listResponse := httptest.NewRecorder()
	handler.ListRuns(listResponse, listRequest, "sales")
	if listResponse.Code != http.StatusOK || viewChecks != 1 || runChecks != 0 {
		t.Fatalf("list response=%d viewChecks=%d runChecks=%d body=%s", listResponse.Code, viewChecks, runChecks, listResponse.Body.String())
	}

	createRequest := withRouteParams(httptest.NewRequest(http.MethodPost, "/api/v1/projects/sales/refresh-runs", strings.NewReader(`{"pipelineId":"sales-refresh"}`)), map[string]string{"project": "sales"})
	createResponse := httptest.NewRecorder()
	handler.CreateRun(createResponse, createRequest, "sales")
	if createResponse.Code != http.StatusNotFound || viewChecks != 1 || runChecks != 1 {
		t.Fatalf("create response=%d viewChecks=%d runChecks=%d body=%s", createResponse.Code, viewChecks, runChecks, createResponse.Body.String())
	}
}

func TestHandlerBindsProjectAndFiltersUnauthorizedRuns(t *testing.T) {
	repo := &authorizationRunRepository{runs: []refreshrun.RunRecord{
		{ID: "run-visible", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "visible", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "visible", InvocationSource: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:00:00Z"},
		{ID: "run-hidden", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "hidden", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "hidden", InvocationSource: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:01:00Z"},
	}}
	handler := Handler{
		Repository:      func() (refreshrun.RunRepository, error) { return repo, nil },
		ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) { return testIdentity(), nil },
		AuthorizePipelineView: func(_ *http.Request, _ projectgraph.ServingIdentity, pipelineID string) (bool, error) {
			return pipelineID == "visible", nil
		},
	}
	request := withRouteParams(httptest.NewRequest(http.MethodGet, "/api/v1/projects/sales/refresh-runs", nil), map[string]string{"project": "sales"})
	response := httptest.NewRecorder()
	handler.ListRuns(response, request, "sales")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "run-visible") || strings.Contains(response.Body.String(), "run-hidden") {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerProjectMismatchAndAuthorizationFailureDoNotEnumerate(t *testing.T) {
	called := false
	handler := Handler{
		Repository: func() (refreshrun.RunRepository, error) {
			return &authorizationRunRepository{runs: []refreshrun.RunRecord{{
				ID: "run-1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "daily", InvocationSource: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:00:00Z",
			}}}, nil
		},
		ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) { return testIdentity(), nil },
		AuthorizePipelineView: func(_ *http.Request, _ projectgraph.ServingIdentity, _ string) (bool, error) {
			called = true
			return false, errors.New("policy store unavailable")
		},
	}
	mismatch := withRouteParams(httptest.NewRequest(http.MethodGet, "/api/v1/projects/other/refresh-runs", nil), map[string]string{"project": "other"})
	mismatchResponse := httptest.NewRecorder()
	handler.ListRuns(mismatchResponse, mismatch, "other")
	if mismatchResponse.Code != http.StatusNotFound || called {
		t.Fatalf("mismatch response=%d called=%t body=%s", mismatchResponse.Code, called, mismatchResponse.Body.String())
	}

	visible := withRouteParams(httptest.NewRequest(http.MethodGet, "/api/v1/projects/sales/refresh-runs", nil), map[string]string{"project": "sales"})
	visibleResponse := httptest.NewRecorder()
	handler.ListRuns(visibleResponse, visible, "sales")
	if visibleResponse.Code != http.StatusServiceUnavailable || !called {
		t.Fatalf("authorization response=%d called=%t body=%s", visibleResponse.Code, called, visibleResponse.Body.String())
	}
}

func TestHandlerDeniedRunIsNonEnumeratingNotFound(t *testing.T) {
	repo := &authorizationRunRepository{runs: []refreshrun.RunRecord{{
		ID: "run-hidden", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "hidden", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "hidden", InvocationSource: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:00:00Z",
	}}}
	handler := Handler{
		Repository:            func() (refreshrun.RunRepository, error) { return repo, nil },
		ServingIdentity:       func(*http.Request) (projectgraph.ServingIdentity, error) { return testIdentity(), nil },
		AuthorizePipelineView: func(*http.Request, projectgraph.ServingIdentity, string) (bool, error) { return false, nil },
	}
	request := withRouteParams(httptest.NewRequest(http.MethodGet, "/api/v1/projects/sales/refresh-runs/run-hidden", nil), map[string]string{"project": "sales", "run": "run-hidden"})
	response := httptest.NewRecorder()
	handler.GetRun(response, request, "sales", "run-hidden")
	if response.Code != http.StatusNotFound {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

type authorizationRunRepository struct {
	runs []refreshrun.RunRecord
}

func (r *authorizationRunRepository) CreateRun(context.Context, refreshrun.RunInput) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (r *authorizationRunRepository) GetRun(_ context.Context, _ refreshrun.ReadScope, runID string) (refreshrun.RunRecord, error) {
	for _, run := range r.runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return refreshrun.RunRecord{}, sql.ErrNoRows
}
func (r *authorizationRunRepository) ListRuns(context.Context, refreshrun.ReadScope, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return append([]refreshrun.RunRecord(nil), r.runs...), nil
}
func (r *authorizationRunRepository) ListTargetRuns(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (r *authorizationRunRepository) ListChildRuns(context.Context, refreshrun.ReadScope, string) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (r *authorizationRunRepository) LatestTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return refreshrun.RunRecord{}, false, nil
}
func (r *authorizationRunRepository) LatestSuccessfulTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return refreshrun.RunRecord{}, false, nil
}
func (r *authorizationRunRepository) MarkRunRunning(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (r *authorizationRunRepository) MarkRunSucceeded(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (r *authorizationRunRepository) MarkRunFailed(context.Context, projectgraph.ServingIdentity, string, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}

func testIdentity() projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation"}
}

func withRouteParams(request *http.Request, params map[string]string) *http.Request {
	ctx := chi.NewRouteContext()
	for key, value := range params {
		ctx.URLParams.Add(key, value)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}
