package ui

import (
	"math"
	"net/url"
	"testing"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestPipelineListSeparatesLatestRunFromConfirmedPublication(t *testing.T) {
	publishedAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"},
		Refresh: AssetRefreshState{
			Latest:      AssetRefreshRun{ID: "failed-run", Status: "failed"},
			DataVersion: AssetDataVersion{Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline:sales", RunID: "older-success", SnapshotID: 42, RefreshedAt: publishedAt},
		},
	}}}
	item := pipelineMonitorPageSignal(state, "pipelines").Pipelines[0]
	if item.Status != "failed" || uisignals.ValueOrZero(item.LatestRunHref) != "/pipelines/pipeline:sales/runs/failed-run" || uisignals.ValueOrZero(item.LastPublishedAt) != publishedAt.Format(time.RFC3339) {
		t.Fatalf("pipeline row = %#v", item)
	}
	state.Pipelines[0].Refresh.DataVersion.PipelineID = "pipeline:other"
	item = pipelineMonitorPageSignal(state, "pipelines").Pipelines[0]
	if item.LastPublishedAt != nil {
		t.Fatalf("other pipeline publication leaked into row: %#v", item)
	}
}

func TestPipelineListUsesReadableSemanticModelAndNeverRunLabel(t *testing.T) {
	page := pipelineMonitorPageSignal(PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh", Payload: map[string]any{"semanticModel": "semantic-model:sales"}}, SemanticModelTitle: "Revenue",
	}}}, "pipelines")
	if got := page.Pipelines[0].SemanticModel; got != "Revenue Semantic Model" {
		t.Fatalf("semantic model = %q, want a display name", got)
	}
	if got := page.Pipelines[0].Status; got != "never run" {
		t.Fatalf("status = %q, want never run", got)
	}
}

func TestPipelineSemanticModelDisplayNameFallsBackToReadableResourceReference(t *testing.T) {
	if got := pipelineSemanticModelDisplayName("semantic-model:sales"); got != "Sales Semantic Model" {
		t.Fatalf("semantic model display fallback = %q", got)
	}
}

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
	if got := page.RunsTable.Rows[0]["pipeline_href"]; got != "/pipelines/pipeline:sales/details" {
		t.Fatalf("run pipeline_href = %#v, want pipeline detail route", got)
	}
	if got := page.RunsTable.Rows[0]["run_href"]; got != "/pipelines/pipeline:sales/runs/run:queued" {
		t.Fatalf("run_href = %#v, want canonical run detail route", got)
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

func TestPipelineMonitorShowsPreparedAsActiveWithoutInventingCapacity(t *testing.T) {
	state := PipelineMonitorState{
		Capacity: PipelineMonitorCapacity{Running: 1, Queued: 2, Prepared: 1},
		Pipelines: []PipelineMonitorPipeline{{
			Asset:   projectview.DevelopAssetView{ID: "pipeline:sales", Key: "sales"},
			Refresh: AssetRefreshState{Latest: AssetRefreshRun{Status: "prepared"}},
		}},
	}
	page := pipelineMonitorPageSignal(state, "runs")
	if !page.Pipelines[0].Running {
		t.Fatal("prepared pipeline is not marked active")
	}
	if len(page.Metrics) != 3 || page.Metrics[2].Label != "Prepared" || page.Metrics[2].Value != "1" {
		t.Fatalf("metrics = %#v, want a prepared count instead of unknown node capacity", page.Metrics)
	}
}

func TestPipelineListSeparatesDefinitionsFromRunMonitor(t *testing.T) {
	state := PipelineMonitorState{Environment: "dev", Capacity: PipelineMonitorCapacity{Running: 1}}
	list := pipelineMonitorPageSignal(state, "pipelines")
	if list.Title != "Pipelines" || len(list.Metrics) != 0 {
		t.Fatalf("pipeline list = %#v, want catalog heading without monitor metrics", list)
	}
	runs := pipelineMonitorPageSignal(state, "runs")
	if runs.Title != "Runs" || len(runs.Metrics) == 0 {
		t.Fatalf("runs = %#v, want dedicated monitor heading and metrics", runs)
	}
}

func TestPipelineRunMonitorUsesServerPageAndScopedCounts(t *testing.T) {
	state := PipelineMonitorState{Environment: "dev", Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"}, CanRun: true,
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "stale", Status: "queued"}}},
	}}, RunMonitor: &PipelineRunMonitor{Range: "24h", Pipeline: "pipeline:sales", Page: 2, PageSize: 25, Total: 27, Failed: 2, Completed: 4, Active: 1,
		Runs: []PipelineMonitorRun{{PipelineID: "pipeline:sales", Run: AssetRefreshRun{ID: "run-current", Status: "failed", CreatedAt: "2026-09-13T12:00:00Z"}}}}}
	page := pipelineMonitorPageSignal(state, "runs")
	if page.RunMonitor == nil || page.RunMonitor.Page != 2 || page.RunMonitor.Total != 27 || page.RunMonitor.Pipeline != "pipeline:sales" || len(page.RunsTable.Rows) != 1 || page.RunsTable.Rows[0]["run_id"] != "run-current" {
		t.Fatalf("page = %#v", page)
	}
	if len(page.Metrics) != 3 || page.Metrics[0].Label != "Active now" || page.Metrics[0].Value != "1" || page.Metrics[1].Value != "2" || page.Metrics[2].Label != "Succeeded in range" || page.Metrics[2].Value != "4" {
		t.Fatalf("metrics = %#v", page.Metrics)
	}
	if got := uisignals.ValueOrZero(page.Metrics[1].Detail); got != "Matches selected time range and filters" {
		t.Fatalf("failed metric scope = %q", got)
	}
	if got := uisignals.ValueOrZero(page.Metrics[2].Detail); got != "Matches selected time range and filters" {
		t.Fatalf("completed metric scope = %q", got)
	}
}

func TestPipelineRunsTableOmitsChildRefreshRuns(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"},
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{
			{ID: "pipeline-run", Status: "succeeded"},
			{ID: "child-model-run", ParentRunID: "pipeline-run", Status: "failed"},
		}},
	}}}

	page := pipelineMonitorPageSignal(state, "runs")
	if len(page.RunsTable.Rows) != 1 || page.RunsTable.Rows[0]["run_id"] != "pipeline-run" {
		t.Fatalf("pipeline runs = %#v, want only the root pipeline invocation", page.RunsTable.Rows)
	}
}

func TestPipelineRunMonitorUpdatesURLPreservesInt64Page(t *testing.T) {
	updates, err := url.Parse(projectRouteUpdatesURL(uisignals.RouteKindPipelines, catalog.Catalog{}, uisignals.PipelinePageSignal{
		ActiveTab: "runs",
		RunMonitor: &uisignals.PipelineRunMonitorSignal{
			Pipeline: "pipeline:sales", Page: math.MaxInt64,
		},
	}, projectDocumentExtras{}))
	if err != nil {
		t.Fatalf("parse updates URL: %v", err)
	}
	if got, want := updates.Query().Get("page"), "9223372036854775807"; got != want {
		t.Fatalf("page = %q, want %q", got, want)
	}
	if got, want := updates.Query().Get("pipeline"), "pipeline:sales"; got != want {
		t.Fatalf("pipeline = %q, want %q", got, want)
	}
}
