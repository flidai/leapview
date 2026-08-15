package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
	workspaceview "github.com/flidai/leapview/internal/workspace"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

func TestPipelineMonitorPageUsesGlobalListAndRunHistory(t *testing.T) {
	state := PipelineMonitorState{
		Environment: "dev",
		Capacity:    PipelineMonitorCapacity{Running: 1, Queued: 2, MaximumRunning: 1},
		Pipelines: []PipelineMonitorPipeline{{
			Workspace: workspaceview.WorkspaceView{ID: "sales", Title: "Sales Workspace"},
			Asset: workspaceview.AssetView{ID: "refresh_pipeline:sales-refresh", Key: "sales.sales-refresh", Title: "Sales refresh", Payload: map[string]any{
				"semanticModel": "sales",
				"schedules":     []any{map[string]any{"cron": "0 6 * * *", "timezone": "Europe/Copenhagen"}},
			}},
			Refresh: AssetRefreshState{
				NextRun:          time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC),
				Latest:           AssetRefreshRun{ID: "matrun_queued", Status: "queued", TriggerType: "manual", StartedAt: "2026-08-14T10:00:00Z", PrincipalDisplayName: "Ada"},
				LatestSuccessful: AssetRefreshRun{ID: "matrun_success", Status: "succeeded", TriggerType: "schedule", StartedAt: "2026-08-14T08:00:00Z", FinishedAt: "2026-08-14T08:03:00Z"},
				Runs: []AssetRefreshRun{
					{ID: "matrun_queued", Status: "queued", TriggerType: "manual", StartedAt: "2026-08-14T10:00:00Z", PrincipalDisplayName: "Ada"},
					{ID: "matrun_failed", Status: "failed", TriggerType: "retry", CreatedAt: "2026-08-14T08:59:59Z", StartedAt: "2026-08-14T09:00:00Z", FinishedAt: "2026-08-14T09:01:00Z", Error: "source unavailable", PrincipalID: "principal_ada", PrincipalDisplayName: "Ada", RetryOf: "matrun_prior", ServingStateID: "serving_42", TargetGeneration: 42, Environment: "dev"},
				},
			},
			CanRun: true, CanCancel: true,
		}},
	}
	page := pipelineMonitorPageSignal(state, "runs")
	if page.Kind != uisignals.RoutePipelines || page.ActiveTab != "runs" {
		t.Fatalf("pipeline page route = %#v", page)
	}
	if len(page.Pipelines) != 1 || page.Pipelines[0].Workspace != "Sales Workspace" || page.Pipelines[0].Status != "queued" {
		t.Fatalf("pipeline rows = %#v", page.Pipelines)
	}
	if got := len(page.RunsTable.Rows); got != 2 {
		t.Fatalf("run rows = %d, want 2", got)
	}
	if page.RunsTable.RowAction == nil || *page.RunsTable.RowAction != "detail" {
		t.Fatalf("run table row action = %v, want detail", page.RunsTable.RowAction)
	}
	for _, column := range page.RunsTable.Columns {
		if column.ID == "error" {
			t.Fatal("long run errors must be shown in the detail drawer, not a cramped table column")
		}
	}
	if len(page.Metrics) != 4 || page.Metrics[3].Value != "1 / 1" {
		t.Fatalf("metrics = %#v", page.Metrics)
	}
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sales-refresh", "matrun_queued", "matrun_failed", "Europe/Copenhagen", "Sales Workspace"} {
		if !strings.Contains(string(payload), want) {
			t.Fatalf("pipeline payload missing %q: %s", want, payload)
		}
	}
	rows := page.RunsTable.Rows
	if got := fmt.Sprint(rows[0]["actions"]); !strings.Contains(got, "cancel") {
		t.Fatalf("queued run actions = %v, want cancel", rows[0]["actions"])
	}
	if got := fmt.Sprint(rows[1]["actions"]); !strings.Contains(got, "retry") {
		t.Fatalf("failed run actions = %v, want retry", rows[1]["actions"])
	}
	for key, want := range map[string]any{
		"error": "source unavailable", "principal_id": "principal_ada", "retry_of": "matrun_prior",
		"serving_state_id": "serving_42", "target_generation": int64(42), "environment": "dev",
	} {
		if got := rows[1][key]; got != want {
			t.Fatalf("failed run detail %s = %v, want %v", key, got, want)
		}
	}
}

func TestPipelinesPageUsesSharedWorkspaceBundleAndGlobalUpdates(t *testing.T) {
	page := PipelinesPage(catalog.Catalog{}, PipelineMonitorState{
		CSRFToken:     "csrf-pipelines",
		RunCommand:    apigenui.MustAction("workspace.refresh.run", "createRefreshRun"),
		CancelCommand: apigenui.MustAction("workspace.refresh.cancel", "cancelRefreshRun"),
	}, "pipelines", "Owner")
	var rendered strings.Builder
	if err := page.Render(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<lv-pipelines-page`, `/static/workspace-page.js`, `route=pipelines`, `data-on:lv-pipeline-command`, `createRefreshRun`, `cancelRefreshRun`, `csrf-pipelines`} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("pipelines page missing %q:\n%s", want, rendered.String())
		}
	}
}
