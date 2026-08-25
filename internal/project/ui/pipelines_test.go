package ui

import (
	"testing"

	projectview "github.com/flidai/leapview/internal/project"
)

func TestPipelineMonitorSignalUsesCanonicalAssetIDForActionsAndRuns(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset:  projectview.DevelopAssetView{ID: "pipeline:sales", Key: "sales", Title: "Sales refresh"},
		CanRun: true, CanCancel: true,
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "run:queued", Status: "queued"}}},
	}}}

	page := pipelineMonitorPageSignal(state, "runs")
	if len(page.Pipelines) != 1 {
		t.Fatalf("pipelines = %#v", page.Pipelines)
	}
	item := page.Pipelines[0]
	if item.ID != "pipeline:sales" || item.AssetID != "pipeline:sales" || item.PipelineID != "pipeline:sales" {
		t.Fatalf("pipeline identity = %#v, want canonical asset ID", item)
	}
	if len(page.RunsTable.Rows) != 1 {
		t.Fatalf("run rows = %#v", page.RunsTable.Rows)
	}
	if got := page.RunsTable.Rows[0]["pipeline_id"]; got != "pipeline:sales" {
		t.Fatalf("run pipeline_id = %#v, want canonical asset ID", got)
	}
	if got := page.RunsTable.Rows[0]["actions"].([]map[string]any); len(got) != 2 || got[1]["action"] != "cancel" {
		t.Fatalf("run actions = %#v, want cancel action", got)
	}
}

func TestPipelineMonitorSignalHidesMutationActionsWithoutUseCapability(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset:   projectview.DevelopAssetView{ID: "pipeline:sales", Key: "sales", Title: "Sales refresh"},
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "run:queued", Status: "queued"}}},
	}}}
	page := pipelineMonitorPageSignal(state, "runs")
	actions := page.RunsTable.Rows[0]["actions"].([]map[string]any)
	if len(actions) != 1 || actions[0]["action"] != "detail" {
		t.Fatalf("read-only run actions = %#v, want details only", actions)
	}
}
