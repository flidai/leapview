package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestPipelineRunResponseForExposesOnlyPipelineContract(t *testing.T) {
	run := refreshrun.RunRecord{
		ID: "run_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusQueued, CreatedAt: "2026-07-19T06:00:00Z",
	}
	response, ok := PipelineRunResponseFor(run)
	if !ok {
		t.Fatal("PipelineRunResponseFor() rejected a root pipeline run")
	}
	if response.PipelineID != "sales-refresh" || response.SemanticModel != "sales" || response.Trigger != refreshrun.TriggerManual {
		t.Fatalf("response = %#v", response)
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, internalField := range []string{"modelId", "servingStateId", "targetType", "targetId", "triggerType", "parentRunId"} {
		if strings.Contains(string(payload), `"`+internalField+`"`) {
			t.Fatalf("public response contains internal field %q: %s", internalField, payload)
		}
	}
}

func TestPipelineRunResponseForRejectsDependencyRun(t *testing.T) {
	_, ok := PipelineRunResponseFor(refreshrun.RunRecord{
		ID: "task_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: refreshrun.TargetModelTable,
		TargetID: "sales.orders", ParentRunID: "run_1", TriggerType: refreshrun.TriggerDependency,
	})
	if ok {
		t.Fatal("PipelineRunResponseFor() accepted an internal dependency run")
	}
}

func TestPipelineRunResponseForNormalizesSQLiteTimestamps(t *testing.T) {
	response, ok := PipelineRunResponseFor(refreshrun.RunRecord{
		ID: "run_1", Identity: testIdentity(), SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: refreshrun.TriggerManual,
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
		TriggerType: refreshrun.TriggerManual, Status: refreshrun.RunStatusSucceeded, CreatedAt: "2026-07-19T06:00:00Z",
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
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/refresh-runs", nil)
	listResponse := httptest.NewRecorder()
	handler.ListRuns(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || viewChecks != 1 || runChecks != 0 {
		t.Fatalf("list response=%d viewChecks=%d runChecks=%d body=%s", listResponse.Code, viewChecks, runChecks, listResponse.Body.String())
	}

	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/refresh-runs", strings.NewReader(`{"pipelineId":"sales-refresh"}`))
	createResponse := httptest.NewRecorder()
	handler.CreateRun(createResponse, createRequest)
	if createResponse.Code != http.StatusForbidden || viewChecks != 1 || runChecks != 1 {
		t.Fatalf("create response=%d viewChecks=%d runChecks=%d body=%s", createResponse.Code, viewChecks, runChecks, createResponse.Body.String())
	}
}

type authorizationRunRepository struct {
	runs []refreshrun.RunRecord
}

func (r *authorizationRunRepository) CreateRun(context.Context, refreshrun.RunInput) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (r *authorizationRunRepository) GetRun(_ context.Context, _ projectgraph.ServingIdentity, runID string) (refreshrun.RunRecord, error) {
	for _, run := range r.runs {
		if run.ID == runID {
			return run, nil
		}
	}
	return refreshrun.RunRecord{}, sql.ErrNoRows
}
func (r *authorizationRunRepository) ListRuns(context.Context, projectgraph.ServingIdentity, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return append([]refreshrun.RunRecord(nil), r.runs...), nil
}
func (r *authorizationRunRepository) ListTargetRuns(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (r *authorizationRunRepository) ListChildRuns(context.Context, projectgraph.ServingIdentity, string) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (r *authorizationRunRepository) LatestTargetRun(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return refreshrun.RunRecord{}, false, nil
}
func (r *authorizationRunRepository) LatestSuccessfulTargetRun(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
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
