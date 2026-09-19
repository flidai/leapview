package ui

import (
	"strings"
	"testing"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDashboardVisualsUseReadableTypesAndFieldLabels(t *testing.T) {
	parent := projectview.DevelopAssetView{ID: "dashboard:finance", Type: string(projectview.AssetTypeDashboard), Key: "finance", Title: "Finance"}
	visual := projectview.DevelopAssetView{
		ID: "visual:revenue", Type: "visual", ParentID: parent.ID, Key: "revenue", Title: "Revenue trend",
		Payload: map[string]any{
			"Type": "bar",
			"Query": map[string]any{
				"Metrics":    []any{map[string]any{"Alias": "Net sales", "FieldID": "net_sales"}},
				"Dimensions": []any{map[string]any{"FieldID": "purchase_month"}},
			},
		},
	}
	table := dashboardVisualsTable(parent, []projectview.DevelopAssetView{visual})
	row := table.Rows[0]
	if row["type"] != "Bar chart" || row["metrics"] != "Net sales" || row["dimensions"] != "Purchase Month" {
		t.Fatalf("visual row = %#v, want human-facing type and field labels", row)
	}
	if strings.Contains(row["metrics"].(string), "map[") || strings.Contains(row["dimensions"].(string), "map[") {
		t.Fatalf("visual fields contain serialized maps: %#v", row)
	}
	if got := dashboardVisualTypeLabel(map[string]any{"RendererID": "echarts", "Spec": map[string]any{"Kind": "cartesian", "Mark": "line"}}); got != "Line chart" {
		t.Fatalf("compiled visual type = %q, want Line chart", got)
	}
}

func TestAssetLineageProjectsOnlyRelevantDependenciesWithForwardEdges(t *testing.T) {
	connection := projectview.DevelopAssetView{ID: "connection:finance", Type: "connection", Key: "finance", Title: "Microsoft Financial Sample"}
	source := projectview.DevelopAssetView{ID: "source:forecast", Type: "source", Key: "forecast", Title: "13-Week Cash Forecast Fact"}
	model := projectview.DevelopAssetView{ID: "model:cfo", Type: "model", Key: "cfo", Title: "CFO Finance Model"}
	semantic := projectview.DevelopAssetView{ID: "semantic:cfo", Type: "semantic_model", Key: "cfo", Title: "CFO Finance Semantic Model"}
	dashboard := projectview.DevelopAssetView{ID: "dashboard:cfo", Type: "dashboard", Key: "cfo", Title: "CFO dashboard"}
	sibling := projectview.DevelopAssetView{ID: "model:sibling", Type: "model", Key: "sibling", Title: "Sibling model"}
	unrelated := projectview.DevelopAssetView{ID: "model:unrelated", Type: "model", Key: "unrelated", Title: "Unrelated model"}
	assets := []projectview.DevelopAssetView{connection, source, model, semantic, dashboard, sibling, unrelated}
	edges := []projectview.DevelopEdgeView{
		{ID: "source-connection", FromAssetID: source.ID, ToAssetID: connection.ID, Type: "uses"},
		{ID: "model-source", FromAssetID: model.ID, ToAssetID: source.ID, Type: "uses"},
		{ID: "sibling-source", FromAssetID: sibling.ID, ToAssetID: source.ID, Type: "uses"},
		{ID: "semantic-model", FromAssetID: semantic.ID, ToAssetID: model.ID, Type: "uses"},
		{ID: "dashboard-semantic", FromAssetID: dashboard.ID, ToAssetID: semantic.ID, Type: "uses"},
	}
	lineage := assetLineage("project:test", semantic, assets, edges)
	seen := map[string]bool{}
	for _, node := range lineage.Graph.Nodes {
		seen[node.ID] = true
		if node.ID == unrelated.ID || node.ID == sibling.ID {
			t.Fatalf("lineage included a node outside the selected asset path %#v", node)
		}
		if node.ID == semantic.ID && !uisignals.ValueOrZero(node.Selected) {
			t.Fatalf("selected semantic model node = %#v", node)
		}
	}
	for _, id := range []string{connection.ID, source.ID, model.ID, semantic.ID, dashboard.ID} {
		if !seen[id] {
			t.Fatalf("lineage omitted relevant node %q: %#v", id, lineage.Graph.Nodes)
		}
	}
	wantEdges := map[string]bool{
		connection.ID + "->" + source.ID:  true,
		source.ID + "->" + model.ID:       true,
		model.ID + "->" + semantic.ID:     true,
		semantic.ID + "->" + dashboard.ID: true,
	}
	for _, edge := range lineage.Graph.Edges {
		if edge.Label != nil {
			t.Fatalf("lineage edge %q retained visual label %q", edge.ID, *edge.Label)
		}
		delete(wantEdges, edge.Source+"->"+edge.Target)
	}
	if len(wantEdges) != 0 {
		t.Fatalf("lineage edges missing %v: %#v", wantEdges, lineage.Graph.Edges)
	}
}

func TestAssetLineageCollapsesSameLayerDependenciesAroundSelectedModel(t *testing.T) {
	connection := projectview.DevelopAssetView{ID: "connection:finance", Type: "connection"}
	source := projectview.DevelopAssetView{ID: "source:finance", Type: "source"}
	selected := projectview.DevelopAssetView{ID: "model:forecast", Type: "model"}
	intermediate := projectview.DevelopAssetView{ID: "model:performance", Type: "model"}
	semantic := projectview.DevelopAssetView{ID: "semantic:finance", Type: "semantic_model"}
	assets := []projectview.DevelopAssetView{connection, source, selected, intermediate, semantic}
	edges := []projectview.DevelopEdgeView{
		{FromAssetID: source.ID, ToAssetID: connection.ID, Type: "uses_connection"},
		{FromAssetID: intermediate.ID, ToAssetID: source.ID, Type: "reads_source"},
		{FromAssetID: selected.ID, ToAssetID: intermediate.ID, Type: "uses_model"},
		{FromAssetID: semantic.ID, ToAssetID: selected.ID, Type: "uses_model"},
	}
	lineage := assetLineage("project:test", selected, assets, edges)
	seen := map[string]bool{}
	for _, node := range lineage.Graph.Nodes {
		seen[node.ID] = true
	}
	if !seen[connection.ID] || !seen[source.ID] || !seen[selected.ID] || !seen[semantic.ID] || seen[intermediate.ID] {
		t.Fatalf("projected model lineage nodes = %#v", lineage.Graph.Nodes)
	}
}

func TestAssetLineageProjectsRefreshPipelinesAsVisibleConsumers(t *testing.T) {
	semantic := projectview.DevelopAssetView{ID: "semantic:sales", Type: "semantic_model", Key: "sales", Title: "Sales semantic model"}
	pipeline := projectview.DevelopAssetView{ID: "pipeline:sales", Type: "refresh_pipeline", Key: "sales", Title: "Sales refresh"}
	dashboard := projectview.DevelopAssetView{ID: "dashboard:sales", Type: "dashboard", Key: "sales", Title: "Sales dashboard"}
	assets := []projectview.DevelopAssetView{semantic, pipeline, dashboard}
	edges := []projectview.DevelopEdgeView{
		{ID: "pipeline-semantic", FromAssetID: pipeline.ID, ToAssetID: semantic.ID, Type: "refreshes"},
		{ID: "dashboard-semantic", FromAssetID: dashboard.ID, ToAssetID: semantic.ID, Type: "uses"},
	}

	lineage := assetLineage("project:test", pipeline, assets, edges)
	seen := map[string]bool{}
	for _, node := range lineage.Graph.Nodes {
		seen[node.ID] = true
	}
	if !seen[pipeline.ID] || !seen[semantic.ID] {
		t.Fatalf("refresh pipeline lineage nodes = %#v, want selected pipeline and semantic model", lineage.Graph.Nodes)
	}
	if seen[dashboard.ID] {
		t.Fatalf("refresh pipeline lineage included unrelated dashboard: %#v", lineage.Graph.Nodes)
	}
	if len(lineage.Graph.Edges) != 1 || lineage.Graph.Edges[0].Source != semantic.ID || lineage.Graph.Edges[0].Target != pipeline.ID {
		t.Fatalf("refresh pipeline lineage edges = %#v, want semantic model -> pipeline", lineage.Graph.Edges)
	}
	if len(lineage.Uses.Rows) != 1 || lineage.Uses.Rows[0]["asset"] != semantic.Title {
		t.Fatalf("refresh pipeline uses rows = %#v, want semantic model dependency", lineage.Uses.Rows)
	}
	if got := lineage.Uses.Rows[0]["relation"]; got != "Refreshes semantic model" {
		t.Fatalf("refresh pipeline relationship label = %q, want %q", got, "Refreshes semantic model")
	}
}

func TestAssetLineageProjectsCompiledPipelineChain(t *testing.T) {
	connection := projectview.DevelopAssetView{ID: "connection:finance_files", Type: "connection", Title: "Finance files"}
	source := projectview.DevelopAssetView{ID: "source:finance.financials", Type: "source", Title: "Financials"}
	model := projectview.DevelopAssetView{ID: "model:financial_performance", Type: "model", Title: "Financial performance"}
	semantic := projectview.DevelopAssetView{ID: "semantic-model:finance", Type: "semantic_model", Title: "CFO Finance Model"}
	dashboard := projectview.DevelopAssetView{ID: "dashboard:cfo-command-center", Type: "dashboard", Title: "CFO Command Center"}
	assets := []projectview.DevelopAssetView{connection, source, model, semantic, dashboard}
	for _, pipelineType := range []string{"pipeline", "refresh_pipeline"} {
		t.Run(pipelineType, func(t *testing.T) {
			pipeline := projectview.DevelopAssetView{ID: "pipeline:finance-refresh", Type: pipelineType, Title: "Finance refresh"}
			allAssets := append(append([]projectview.DevelopAssetView{}, assets...), pipeline)
			edges := []projectview.DevelopEdgeView{
				{ID: "source-connection", FromAssetID: source.ID, ToAssetID: connection.ID, Type: "uses_connection"},
				{ID: "model-source", FromAssetID: model.ID, ToAssetID: source.ID, Type: "reads_source"},
				{ID: "semantic-model", FromAssetID: semantic.ID, ToAssetID: model.ID, Type: "uses_model"},
				{ID: "pipeline-semantic", FromAssetID: pipeline.ID, ToAssetID: semantic.ID, Type: "refreshes"},
				{ID: "dashboard-semantic", FromAssetID: dashboard.ID, ToAssetID: semantic.ID, Type: "uses_semantic_model"},
			}

			lineage := assetLineage("project:test", pipeline, allAssets, edges)
			seen := map[string]bool{}
			for _, node := range lineage.Graph.Nodes {
				seen[node.ID] = true
			}
			for _, asset := range []projectview.DevelopAssetView{connection, source, model, semantic, pipeline} {
				if !seen[asset.ID] {
					t.Fatalf("compiled %s pipeline lineage omitted %q: %#v", pipelineType, asset.ID, lineage.Graph.Nodes)
				}
			}
			wantRanks := map[string]int64{connection.ID: -4, source.ID: -3, model.ID: -2, semantic.ID: -1, pipeline.ID: 0}
			for _, node := range lineage.Graph.Nodes {
				if want, ok := wantRanks[node.ID]; ok && node.Rank != want {
					t.Fatalf("compiled %s pipeline node %q rank = %d, want %d", pipelineType, node.ID, node.Rank, want)
				}
			}
			if seen[dashboard.ID] {
				t.Fatalf("compiled %s pipeline lineage included downstream dashboard: %#v", pipelineType, lineage.Graph.Nodes)
			}
			wantEdges := map[string]bool{
				connection.ID + "->" + source.ID: true,
				source.ID + "->" + model.ID:      true,
				model.ID + "->" + semantic.ID:    true,
				semantic.ID + "->" + pipeline.ID: true,
			}
			for _, edge := range lineage.Graph.Edges {
				delete(wantEdges, edge.Source+"->"+edge.Target)
			}
			if len(wantEdges) != 0 {
				t.Fatalf("compiled %s pipeline lineage missing edges %v: %#v", pipelineType, wantEdges, lineage.Graph.Edges)
			}
		})
	}
}

func TestRefreshHistoryUsesAvailableTimestampsAndReadablePrincipals(t *testing.T) {
	run := AssetRefreshRun{
		ID: "run:finance", Status: "succeeded", CreatedAt: "2026-08-24T13:00:00Z", FinishedAt: "2026-08-24T13:00:05Z",
		ParentRunID: "run:pipeline", PrincipalID: "123e4567-e89b-12d3-a456-426614174000",
	}
	table := assetRefreshesTable(AssetRefreshState{Runs: []AssetRefreshRun{run}})
	row := table.Rows[0]
	started, _ := time.Parse(time.RFC3339, run.CreatedAt)
	if row["started"] != started.Local().Format("02 Jan 2006, 15:04 MST") || row["duration"] != "5s" || row["trigger"] != "Pipeline" || row["triggered_by"] != "Unknown actor" {
		t.Fatalf("refresh row = %#v, want readable successful history", row)
	}
	if got := row["status"].(recordTableBadge).Label; got != "succeeded" {
		t.Fatalf("refresh status = %q, want succeeded", got)
	}
	if got := assetRefreshStatus(AssetRefreshState{LatestSuccessful: AssetRefreshRun{Status: "succeeded", FinishedAt: "2026-08-24T13:00:05Z"}}); got != "succeeded" {
		t.Fatalf("refresh status without latest run = %q, want succeeded", got)
	}
	if got := assetRefreshStatus(AssetRefreshState{Runs: []AssetRefreshRun{{Status: "succeeded", FinishedAt: "2026-08-24T13:00:05Z"}}}); got != "succeeded" {
		t.Fatalf("refresh status from successful history = %q, want succeeded", got)
	}
}

func TestPrincipalDisplayLabelDoesNotInferServiceAccountFromUUID(t *testing.T) {
	if got := principalDisplayLabel("123e4567-e89b-12d3-a456-426614174000"); got != "Unknown actor" {
		t.Fatalf("UUID principal label = %q, want neutral unknown actor label", got)
	}
	if got := principalDisplayLabel("service:nightly-refresh"); got != "Nightly Refresh service account" {
		t.Fatalf("explicit service principal label = %q, want service account label", got)
	}
}

func TestAssetVersionsTableDoesNotRenderZeroChangeCounters(t *testing.T) {
	table := assetVersionsTable(AssetVersionsState{Versions: []AssetVersionState{
		{ServingStateID: "state:2", ContentHash: "sha256:2", PayloadJSON: `{"fields":["order_id"]}`},
		{ServingStateID: "state:1", ContentHash: "sha256:1", PayloadJSON: `{"fields":["order_id"]}`},
	}})
	if got := table.Rows[0]["diff_stat"]; got != "—" {
		t.Fatalf("zero-change diff stat = %#v, want em dash without +0/-0 counters", got)
	}
}

func TestAssetVersionsTableDeduplicatesPublicationsAndMarksOneCurrentVersion(t *testing.T) {
	table := assetVersionsTable(AssetVersionsState{
		CurrentContentHash: "sha256:same",
		Versions: []AssetVersionState{
			{ServingStateID: "state:current", ContentHash: "sha256:same", Status: "active", CreatedAt: "2026-08-24T12:00:00Z", ActivatedAt: "2026-08-24T12:01:00Z"},
			{ServingStateID: "state:old", ContentHash: "sha256:same", Status: "active", CreatedAt: "2026-08-23T12:00:00Z", ActivatedAt: "2026-08-23T12:01:00Z"},
			{ServingStateID: "state:current", ContentHash: "sha256:same", Status: "active", CreatedAt: "2026-08-24T12:00:00Z", ActivatedAt: "2026-08-24T12:02:00Z"},
		},
	})
	if len(table.Rows) != 2 {
		t.Fatalf("version rows = %#v, want one row per serving state", table.Rows)
	}
	currentCount := 0
	for _, row := range table.Rows {
		if badge, ok := row["status"].(recordTableBadge); ok && badge.Label == "current" {
			currentCount++
		}
	}
	if currentCount != 1 || table.Rows[0]["versionId"] != "state:current" {
		t.Fatalf("version rows = %#v, want latest serving state as the sole current version", table.Rows)
	}
}
