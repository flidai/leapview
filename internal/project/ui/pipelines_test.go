package ui

import (
	"bytes"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestPipelineCollectionBindsWaitingRequestCancellation(t *testing.T) {
	state := PipelineMonitorState{
		RunCommand:    refreshgen.GenUIActionCreateRefreshRun(),
		CancelCommand: refreshgen.GenUIActionCancelRefreshRun(),
		CSRFToken:     "csrf-test",
	}
	var rendered bytes.Buffer
	if err := PipelinesPage(catalog.Catalog{}, state, "pipelines", "").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"cancel-intent", "cancelRefreshRun"} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("pipeline collection command bridge missing %q", expected)
		}
	}
}

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
	if item.Status != "failed" || len(item.RecentRuns) != 1 || item.RecentRuns[0].Href != "/pipelines/pipeline:sales/runs/failed-run" || uisignals.ValueOrZero(item.LastPublishedAt) != publishedAt.Format(time.RFC3339) {
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

func TestPipelineListRecentRunsAreNewestRootInvocationsOnly(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"},
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{
			{ID: "run-2", Status: "failed", StartedAt: "2026-09-22T12:02:00Z"},
			{ID: "child", ParentRunID: "run-2", Status: "failed", StartedAt: "2026-09-22T12:09:00Z"},
			{ID: "run-1", Status: "succeeded", StartedAt: "2026-09-22T12:01:00Z"},
			{ID: "run-6", Status: "succeeded", StartedAt: "2026-09-22T12:06:00Z"},
			{ID: "run-5", Status: "succeeded", StartedAt: "2026-09-22T12:05:00Z"},
			{ID: "run-4", Status: "succeeded", StartedAt: "2026-09-22T12:04:00Z"},
			{ID: "run-3", Status: "succeeded", StartedAt: "2026-09-22T12:03:00Z"},
		}},
	}}}
	runs := pipelineMonitorPageSignal(state, "pipelines").Pipelines[0].RecentRuns
	if len(runs) != 5 {
		t.Fatalf("recent runs = %#v, want five root runs", runs)
	}
	for index, id := range []string{"run-6", "run-5", "run-4", "run-3", "run-2"} {
		if runs[index].ID != id || runs[index].Href != "/pipelines/pipeline:sales/runs/"+id {
			t.Fatalf("recent run %d = %#v, want %s", index, runs[index], id)
		}
	}
	if runs[4].Status != "failed" {
		t.Fatalf("failed run status = %q", runs[4].Status)
	}
}

func TestPipelineListRecentRunsUsesLatestFallback(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset:   projectview.DevelopAssetView{ID: "pipeline:sales"},
		Refresh: AssetRefreshState{Latest: AssetRefreshRun{ID: "run-latest", Status: "succeeded"}},
	}}}
	runs := pipelineMonitorPageSignal(state, "pipelines").Pipelines[0].RecentRuns
	if len(runs) != 1 || runs[0].ID != "run-latest" {
		t.Fatalf("recent runs = %#v, want latest fallback", runs)
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

func TestPipelineRunHistoryDoesNotOfferRunAgain(t *testing.T) {
	state := PipelineMonitorState{Pipelines: []PipelineMonitorPipeline{{
		Asset:   projectview.DevelopAssetView{ID: "pipeline:sales", Key: "sales", Title: "Sales refresh"},
		CanRun:  true,
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "run:finished", Status: "succeeded"}}},
	}}}
	page := pipelineMonitorPageSignal(state, "runs")
	if len(page.RunsTable.Rows) != 1 {
		t.Fatalf("run rows = %#v", page.RunsTable.Rows)
	}
	actions := page.RunsTable.Rows[0]["actions"].([]map[string]any)
	if len(actions) != 1 || actions[0]["action"] != "detail" {
		t.Fatalf("terminal run actions = %#v, want details only", actions)
	}
}

func TestPipelineListSeparatesDefinitionsFromRunMonitor(t *testing.T) {
	state := PipelineMonitorState{Environment: "dev"}
	list := pipelineMonitorPageSignal(state, "pipelines")
	if list.Title != "Pipelines" || list.ActiveTab != "pipelines" {
		t.Fatalf("pipeline list = %#v, want catalog heading and tab", list)
	}
	runs := pipelineMonitorPageSignal(state, "runs")
	if runs.Title != "Runs" || runs.ActiveTab != "runs" {
		t.Fatalf("runs = %#v, want dedicated monitor heading and tab", runs)
	}
}

func TestPipelineRunMonitorUsesServerPage(t *testing.T) {
	state := PipelineMonitorState{Environment: "dev", Pipelines: []PipelineMonitorPipeline{{
		Asset: projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"}, CanRun: true,
		Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "stale", Status: "queued"}}},
	}}, RunMonitor: &PipelineRunMonitor{Range: "24h", Pipeline: "pipeline:sales", Page: 2, PageSize: 25, Total: 27,
		Runs: []PipelineMonitorRun{{PipelineID: "pipeline:sales", Run: AssetRefreshRun{ID: "run-current", Status: "failed", CreatedAt: "2026-09-13T12:00:00Z"}}}}}
	page := pipelineMonitorPageSignal(state, "runs")
	if page.RunMonitor == nil || page.RunMonitor.Page != 2 || page.RunMonitor.Total != 27 || page.RunMonitor.Pipeline != "pipeline:sales" || len(page.RunsTable.Rows) != 1 || page.RunsTable.Rows[0]["run_id"] != "run-current" {
		t.Fatalf("page = %#v", page)
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

func TestPipelineWaitingIntentsStaySeparateAndDisappearOnceTheirRunIsVisible(t *testing.T) {
	state := PipelineMonitorState{
		Pipelines: []PipelineMonitorPipeline{{
			Asset:   projectview.DevelopAssetView{ID: "pipeline:sales", Title: "Sales refresh"},
			Refresh: AssetRefreshState{Runs: []AssetRefreshRun{{ID: "run:attached", Status: "running"}}},
		}},
		WaitingIntents: []PipelineWaitingIntent{
			{IntentID: "request:queued", PipelineID: "pipeline:sales", Status: "waiting", CreatedAt: "2026-09-24T09:00:00Z", QueuePosition: 2, CancelAllowed: true},
			{IntentID: "request:attached", PipelineID: "pipeline:sales", Status: "attached", CreatedAt: "2026-09-24T08:00:00Z", QueuePosition: 1, RunID: "run:attached"},
			{IntentID: "request:cancelled", PipelineID: "pipeline:sales", Status: "cancelled", CreatedAt: "2026-09-24T07:00:00Z"},
		},
	}
	page := pipelineMonitorPageSignal(state, "pipelines")
	if len(page.RunsTable.Rows) != 1 || page.RunsTable.Rows[0]["run_id"] != "run:attached" {
		t.Fatalf("execution run table = %#v", page.RunsTable.Rows)
	}
	if len(page.WaitingIntents) != 1 {
		t.Fatalf("waiting intents = %#v, want only the unattached queued request", page.WaitingIntents)
	}
	queued := page.WaitingIntents[0]
	if queued.IntentID != "request:queued" || queued.Status != "waiting" || queued.CreatedAt != "2026-09-24T09:00:00Z" || uisignals.ValueOrZero(queued.QueuePosition) != 2 || !queued.CancelAllowed || queued.RunID != nil {
		t.Fatalf("queued intent signal = %#v", queued)
	}

	state.Pipelines[0].Refresh.Runs = nil
	page = pipelineMonitorPageSignal(state, "pipelines")
	if len(page.WaitingIntents) != 2 || page.WaitingIntents[0].IntentID != "request:attached" || uisignals.ValueOrZero(page.WaitingIntents[0].RunID) != "run:attached" {
		t.Fatalf("attached intent without visible run = %#v, want a separately linked intent", page.WaitingIntents)
	}
}

func TestPipelineWaitingIntentProjectsStaleOutcomeAndReason(t *testing.T) {
	page := pipelineMonitorPageSignal(PipelineMonitorState{WaitingIntents: []PipelineWaitingIntent{{
		IntentID: "request:stale", PipelineID: "pipeline:sales", Status: "stale", CreatedAt: "2026-09-24T09:00:00Z",
	}}}, "runs")
	if len(page.WaitingIntents) != 1 {
		t.Fatalf("waiting intents = %#v, want stale terminal request", page.WaitingIntents)
	}
	intent := page.WaitingIntents[0]
	if intent.Status != "stale" || uisignals.ValueOrZero(intent.Reason) != "Pipeline definition changed while waiting; start a new request" || intent.CancelAllowed || len(page.RunsTable.Rows) != 0 {
		t.Fatalf("stale request projection = %#v, run rows = %#v", intent, page.RunsTable.Rows)
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
