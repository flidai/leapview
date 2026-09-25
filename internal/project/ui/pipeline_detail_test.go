package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestPipelineDetailRunActionUsesAuthorizedCommandBinding(t *testing.T) {
	state := PipelineDetailState{
		Asset: pipelineDetailFixtureAssets()[3], Environment: "production", ActiveTab: "overview",
		CanRun: true, RunCommand: refreshgen.GenUIActionCreateRefreshRun(), CSRFToken: "csrf-test",
	}
	if page := pipelineDetailPageSignal(state, "overview"); !page.CanRun {
		t.Fatal("pipeline detail omitted the authorized Run now action")
	}
	var rendered bytes.Buffer
	if err := PipelineDetailPage(projectnavigation.Catalog{}, state, "").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"lv-pipeline-detail-page", "lv-pipeline-command", "surface=pipeline_detail", "pipeline%3Adaily"} {
		if !strings.Contains(rendered.String(), expected) {
			t.Fatalf("pipeline detail command bridge missing %q", expected)
		}
	}
	bootstrap := PipelineDetailBootstrapSignals(projectnavigation.Catalog{}, state, "")
	if bootstrap["pipelineCommand"] == nil || bootstrap["pipelineCommandStatus"] == nil {
		t.Fatalf("command bootstrap signals missing: %#v", bootstrap)
	}
}

func TestPipelineDetailSignalSeparatesLatestRunFromConfirmedPublication(t *testing.T) {
	nextRun := time.Date(2026, 9, 23, 6, 0, 0, 0, time.UTC)
	assets := pipelineDetailFixtureAssets()
	state := PipelineDetailState{
		Project: projectview.DevelopView{ID: "project:acme"},
		Asset:   assets[3], Assets: assets,
		Edges: []projectview.DevelopEdgeView{
			{FromAssetID: "pipeline:daily", ToAssetID: "semantic_model:sales", Type: "refreshes"},
			{FromAssetID: "semantic_model:sales", ToAssetID: "model:orders", Type: "uses"},
			{FromAssetID: "model:orders", ToAssetID: "source:orders", Type: "uses"},
			{FromAssetID: "semantic_model:sales", ToAssetID: "dashboard:executive", Type: "powers"},
		},
		Refresh: AssetRefreshState{
			Latest:           AssetRefreshRun{ID: "run:latest", Status: "failed", StartedAt: "2026-09-22T06:00:00Z", Error: "source unavailable"},
			LatestSuccessful: AssetRefreshRun{ID: "run:previous", Status: "succeeded", FinishedAt: "2026-09-21T06:05:00Z"},
			DataVersion:      AssetDataVersion{SnapshotID: 42, ServingStateID: "state:published", Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline:daily", RunID: "run:published", RefreshedAt: time.Date(2026, 9, 21, 6, 5, 0, 0, time.UTC)},
			NextRun:          nextRun,
		},
		Environment: "production",
	}

	page := pipelineDetailPageSignal(state, "overview")
	if page.ActiveTab != "overview" || page.Asset.ID != "pipeline:daily" {
		t.Fatalf("page identity/tab = %#v, want canonical pipeline identity on overview", page)
	}
	if len(page.Tabs) != 3 || !page.Tabs[0].Active || page.Tabs[1].Active || page.Tabs[2].Active {
		t.Fatalf("overview tabs = %#v, want only Overview active", page.Tabs)
	}
	if page.LatestRun == nil || page.LatestRun.ID != "run:latest" || page.LatestRun.Status != "failed" {
		t.Fatalf("latest run = %#v, want failed latest run independent of publication", page.LatestRun)
	}
	if page.LatestRun.Href != "/pipelines/pipeline:daily/runs/run:latest" {
		t.Fatalf("latest run href = %q, want canonical pipeline run detail path", page.LatestRun.Href)
	}
	if page.PublicationStatus != "confirmed" || page.Publication == nil || page.Publication.SnapshotID != 42 || page.Publication.ServingStateID != "state:published" || page.Publication.RunID == nil || *page.Publication.RunID != "run:published" {
		t.Fatalf("publication = %#v, want confirmed published snapshot", page.Publication)
	}
	if page.Timezone != "Europe/Copenhagen" || page.NextRunAt == nil || *page.NextRunAt != nextRun.Format(time.RFC3339) || page.ConcurrencyPolicy != "Forbid" {
		t.Fatalf("schedule projection = %#v, want timezone, next run, and overlap policy", page)
	}
	if len(page.Graph.Nodes) != 3 {
		t.Fatalf("compiler dependency graph nodes = %#v, want semantic model, model, and source only", page.Graph.Nodes)
	}
	for _, want := range []string{"semantic_model:sales", "model:orders", "source:orders"} {
		if !pipelineDetailHasGraphNode(page.Graph.Nodes, want) {
			t.Errorf("dependency graph omitted %q: %#v", want, page.Graph.Nodes)
		}
	}
	if pipelineDetailHasGraphNode(page.Graph.Nodes, "pipeline:daily") {
		t.Fatalf("pipeline orchestration node must not appear after the semantic-model sink: %#v", page.Graph.Nodes)
	}
	for _, node := range page.Graph.Nodes {
		if node.ID == "semantic_model:sales" && !uisignals.ValueOrZero(node.Selected) {
			t.Fatalf("selected data-flow sink = %#v, want semantic model selected", node)
		}
	}
	if pipelineDetailHasGraphNode(page.Graph.Nodes, "dashboard:executive") {
		t.Fatalf("dashboard consumer must be separated from executed dependency graph: %#v", page.Graph.Nodes)
	}
	if len(page.DashboardConsumers) != 1 || page.DashboardConsumers[0].ID != "dashboard:executive" || !strings.Contains(page.DashboardConsumersNote, "not executed") {
		t.Fatalf("dashboard consumers = %#v note=%q, want a clearly non-executed consumer", page.DashboardConsumers, page.DashboardConsumersNote)
	}
}

func TestPipelineDetailRunsSignalUsesFixedPipelineMonitorAndPagedTable(t *testing.T) {
	assets := pipelineDetailFixtureAssets()
	monitor := &PipelineRunMonitor{Query: "failed", Range: "7d", Status: "failed", Trigger: "schedule", Page: 2, PageSize: 25, Total: 31}
	state := PipelineDetailState{
		Asset: assets[3], ActiveTab: PipelineDetailRuns, CanRun: true, RunMonitor: monitor,
		RunCommand: refreshgen.GenUIActionCreateRefreshRun(),
		MonitorRuns: []PipelineMonitorRun{{PipelineID: assets[3].ID, Run: AssetRefreshRun{
			ID: "run:failed", Status: "failed", TriggerType: "schedule", CreatedAt: "2026-09-22T12:00:00Z", StartedAt: "2026-09-22T12:00:03Z", Error: "source unavailable",
		}}},
	}
	page := pipelineDetailPageSignal(state, PipelineDetailRuns)
	if page.RunMonitor == nil || page.RunMonitor.Pipeline != assets[3].ID || page.RunMonitor.Query != "failed" || page.RunMonitor.Range != "7d" || page.RunMonitor.Status != "failed" || page.RunMonitor.Trigger != "schedule" || page.RunMonitor.Page != 2 || page.RunMonitor.Total != 31 {
		t.Fatalf("scoped run monitor = %#v, want fixed pipeline and paging filters", page.RunMonitor)
	}
	if page.RunsTable == nil || len(page.RunsTable.Rows) != 1 {
		t.Fatalf("scoped runs table = %#v, want the monitor result row", page.RunsTable)
	}
	row := page.RunsTable.Rows[0]
	if row["run_id"] != "run:failed" || row["status_value"] != "failed" || row["run_href"] != "/pipelines/pipeline:daily/runs/run:failed" {
		t.Fatalf("scoped run row = %#v, want linked failed run", row)
	}
	var rendered bytes.Buffer
	if err := PipelineDetailPage(projectnavigation.Catalog{}, state, "").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"range=7d", "status=failed", "trigger=schedule", "page=2"} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("scoped Runs shell dropped filter %q from its update/command routes", want)
		}
	}
}

func TestPipelineDetailPrefersRunSpecificPublicationOverSharedModelVersion(t *testing.T) {
	assets := pipelineDetailFixtureAssets()
	state := PipelineDetailState{
		Project: projectview.DevelopView{ID: "project:acme"}, Asset: assets[3], Assets: assets,
		Refresh:          AssetRefreshState{DataVersion: AssetDataVersion{SnapshotID: 99, Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline:other", RunID: "other-run"}},
		PublicationRunID: "sales-run", PublicationSnapshotID: 42, PublicationServingStateID: "state:sales",
		PublicationAt: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
	}
	page := pipelineDetailPageSignal(state, "overview")
	if page.PublicationStatus != "confirmed" || page.Publication == nil || page.Publication.SnapshotID != 42 || uisignals.ValueOrZero(page.Publication.RunID) != "sales-run" {
		t.Fatalf("run-specific publication = %#v, status=%q", page.Publication, page.PublicationStatus)
	}
}

func TestPipelineDetailDefinitionUsesAuthoredYAMLAndStableIdentity(t *testing.T) {
	state := PipelineDetailState{Asset: pipelineDetailFixtureAssets()[3]}
	page := pipelineDetailPageSignal(state, "definition")
	if page.ActiveTab != "definition" || page.Asset.SourceFile != "pipelines/daily.yaml" || page.Asset.ContentHash != "sha256:authored" {
		t.Fatalf("definition identity = %#v, want authored source path and content hash", page.Asset)
	}
	if page.DefinitionYaml != "apiVersion: leapview.dev/v1\nkind: Pipeline\n" {
		t.Fatalf("definition YAML = %q, want exact compiler-projected authored YAML", page.DefinitionYaml)
	}
}

func TestPipelineDetailRequiresPipelineRunProvenanceForPublication(t *testing.T) {
	assets := pipelineDetailFixtureAssets()
	for name, test := range map[string]struct {
		refresh AssetRefreshState
		status  string
	}{
		"successful run without published data":        {refresh: AssetRefreshState{Latest: AssetRefreshRun{ID: "run:success", Status: "succeeded"}}, status: "none"},
		"refresh snapshot without provenance":          {refresh: AssetRefreshState{DataVersion: AssetDataVersion{SnapshotID: 17, Source: refreshschedule.DataVersionSourceRefresh}}, status: "unavailable"},
		"snapshot from another pipeline":               {refresh: AssetRefreshState{DataVersion: AssetDataVersion{SnapshotID: 18, ServingStateID: "state:other", Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline:other", RunID: "run:other"}}, status: "unavailable"},
		"baseline publish is not pipeline publication": {refresh: AssetRefreshState{DataVersion: AssetDataVersion{SnapshotID: 19, ServingStateID: "state:baseline", Source: refreshschedule.DataVersionSourcePublish, PipelineID: "pipeline:daily", RunID: "run:looks-related"}}, status: "unavailable"},
		"snapshot without id":                          {refresh: AssetRefreshState{DataVersion: AssetDataVersion{Source: refreshschedule.DataVersionSourcePublish}}, status: "none"},
	} {
		t.Run(name, func(t *testing.T) {
			page := pipelineDetailPageSignal(PipelineDetailState{Asset: assets[3], Refresh: test.refresh}, "overview")
			if page.PublicationStatus != test.status {
				t.Fatalf("publication status = %q, want %q", page.PublicationStatus, test.status)
			}
			if page.Publication != nil {
				t.Fatalf("publication = %#v, want no confirmed publication evidence", page.Publication)
			}
			if foreignPipelineID := test.refresh.DataVersion.PipelineID; foreignPipelineID != "" && foreignPipelineID != page.Asset.ID {
				encoded, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), foreignPipelineID) {
					t.Fatalf("signal exposes another pipeline's ID: %s", encoded)
				}
			}
		})
	}
}

func pipelineDetailFixtureAssets() []projectview.DevelopAssetView {
	return []projectview.DevelopAssetView{
		{ID: "source:orders", Type: "source", Key: "orders", Title: "Orders source"},
		{ID: "model:orders", Type: "model", Key: "orders", Title: "Orders"},
		{ID: "semantic_model:sales", Type: "semantic_model", Key: "sales", Title: "Sales"},
		{ID: "pipeline:daily", Type: "pipeline", Key: "daily", Title: "Daily refresh", SourceFile: "pipelines/daily.yaml", ContentHash: "sha256:authored", Payload: map[string]any{
			"SemanticModel": "semantic_model:sales", "Timezone": "Europe/Copenhagen", "ConcurrencyPolicy": "Forbid", "StartingDeadlineSeconds": int64(3600),
			"Schedules": []any{map[string]any{"Cron": "0 6 * * *"}}, "Configuration": "apiVersion: leapview.dev/v1\nkind: Pipeline\n",
		}},
		{ID: "dashboard:executive", Type: "dashboard", Key: "executive", Title: "Executive dashboard"},
	}
}

func pipelineDetailHasGraphNode(nodes []uisignals.AssetLineageNodeSignal, id string) bool {
	for _, node := range nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}
