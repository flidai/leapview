package http

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestPipelineWaitingIntentsAreScopedToVisiblePipelines(t *testing.T) {
	h := &BrowserHandler{
		Environment: "dev",
		ReadPipelineIntents: func(_ context.Context, scope refreshrun.ReadScope) ([]PipelineWaitingIntent, error) {
			if scope.ProjectID != projectgraph.ResourceID("project:test") || scope.Environment != "dev" {
				t.Fatalf("scope = %#v", scope)
			}
			return []PipelineWaitingIntent{
				{IntentID: "visible", PipelineID: "pipeline:sales", Status: "waiting"},
				{IntentID: "hidden", PipelineID: "pipeline:private", Status: "waiting"},
			}, nil
		},
	}
	request := httptest.NewRequest("GET", "/pipelines/runs", nil)
	intents, err := h.pipelineWaitingIntents(request, "project:test", []string{"pipeline:sales"})
	if err != nil {
		t.Fatal(err)
	}
	if len(intents) != 1 || intents[0].IntentID != "visible" {
		t.Fatalf("intents = %#v", intents)
	}
}

func TestPipelineCancelIntentConflictUsesSafeSpecificMessage(t *testing.T) {
	got := publicPipelineError(apigenfailure.New("conflict", "private database detail"), "cancel-intent")
	if got != "This request has already started. Reload to see its run." {
		t.Fatalf("cancel conflict message = %q", got)
	}
	if got := publicPipelineError(errors.New("private database detail"), "cancel-intent"); got != "Pipeline operation failed; review the run history and try again." {
		t.Fatalf("unexpected cancellation failure message = %q", got)
	}
}

func TestPipelineCancelRunAfterStartUsesSafeSpecificMessage(t *testing.T) {
	got := publicPipelineError(refreshrun.ErrRunNotCancellable, "cancel")
	if got != "This run has already started and cannot be cancelled. Reload to see its current status." {
		t.Fatalf("cancel started run message = %q", got)
	}
	if got := publicPipelineError(errors.New("private database detail"), "cancel"); got != "Pipeline operation failed; review the run history and try again." {
		t.Fatalf("unexpected cancellation failure message = %q", got)
	}
}
