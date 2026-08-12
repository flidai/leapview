package ui

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	uiactions "github.com/flidai/leapview/internal/platform/web/actions"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

func BenchmarkDashboardJSONAttributeBridge(b *testing.B) {
	benchmarkDashboardBridge(b)
}

func BenchmarkDashboardDatastarLitBridge(b *testing.B) {
	benchmarkDashboardBridge(b)
}

func benchmarkDashboardBridge(b *testing.B) {
	report, model, catalog := benchmarkDashboardFixture()
	activePage := report.Pages[0]
	if err := workspacecompiler.ValidateDashboard(&report, map[string]*semanticmodel.Model{model.Name: model}); err != nil {
		b.Fatal(err)
	}
	definitions, err := workspacecompiler.CompileVisualizationDefinitions(&report, model)
	if err != nil {
		b.Fatal(err)
	}
	compiled, err := workspacecompiler.CompileDashboardDefinition(&report, definitions)
	if err != nil {
		b.Fatal(err)
	}
	htmlBytes := 0

	b.ReportAllocs()
	for b.Loop() {
		signals := BootstrapSignals("client", "benchmark-stream", catalog, compiled, model, definitions, report.Pages, activePage, dashboard.Filters{})
		node := benchmarkDashboardDocument(catalog, report, model, activePage, signals)
		var out strings.Builder
		if err := node.Render(&out); err != nil {
			b.Fatal(err)
		}
		htmlBytes = out.Len()
	}

	b.ReportMetric(float64(htmlBytes), "html_bytes/op")
}

func benchmarkDashboardDocument(catalog catalog.Catalog, report reportdef.Dashboard, model *semanticmodel.Model, activePage dashboard.Page, _ map[string]any) g.Node {
	dashboardUpdatesURL := updatesURL(catalog.Workspace.ID, report.ID, activePage.ID)
	body := benchmarkDatastarLitDashboardRoot(catalog, report, model)
	mainAttrs := []g.Node{
		h.ID("dashboard"),
		h.Class(webpage.RootClass),
	}
	return webpage.Render(webpage.Layout{
		Presentation: webpage.Presentation{ProductName: "LeapView", FaviconPath: "/static/favicon.svg"},
		Scripts:      []string{"/static/app-shell.js"},
	}, webpage.Spec{
		Title: "LeapView", Scripts: []string{"/static/dashboard-page.js", "/static/url-sync.js"},
		MainAttrs:  mainAttrs,
		UpdatesURL: dashboardUpdatesURL,
		Content:    body,
	})
}

func benchmarkDatastarLitDashboardRoot(catalog catalog.Catalog, report reportdef.Dashboard, model *semanticmodel.Model) g.Node {
	attrs := append([]g.Node{g.Attr("slot", "page")}, benchmarkDashboardCommandAttrs(catalog, report, model)...)
	return g.El("lv-app-shell",
		g.El("lv-dashboard-page", attrs...),
	)
}

func benchmarkDashboardCommandAttrs(catalog catalog.Catalog, report reportdef.Dashboard, model *semanticmodel.Model) []g.Node {
	return []g.Node{
		g.Attr("data-on:lv-filter-command", "$filterCommand = evt.detail; "+uiactions.EventPost("/workspaces/"+catalog.Workspace.ID+"/commands/filter", "runtime", "filterCommand")),
		g.Attr("data-on:lv-filter-options-request", "$filterOptionRequest = evt.detail; "+uiactions.EventPost("/workspaces/"+catalog.Workspace.ID+"/commands/filter-options", "runtime", "filterOptionRequest")),
		g.Attr("data-on:lv-selection-clear", "$interactionSelections = []; "+uiactions.EventPost("/workspaces/"+catalog.Workspace.ID+"/commands/clear-selection", "runtime")),
		g.Attr("data-on:lv-interaction-select", "$interactionCommand = evt.detail; "+uiactions.EventPost("/workspaces/"+catalog.Workspace.ID+"/commands/select", "runtime", "interactionCommand")),
		g.Attr("data-on:lv-visualization-window-request", "$visualWindowCommand = evt.detail; "+uiactions.EventPost("/workspaces/"+catalog.Workspace.ID+"/commands/visual-window", "runtime", "visualWindowCommand")),
	}
}

func benchmarkDashboardFixture() (reportdef.Dashboard, *semanticmodel.Model, catalog.Catalog) {
	zebra := true
	filterDefinitions := map[string]dashboardfilter.Definition{}
	filterBindings := map[string]dashboardfilter.Binding{}
	for _, id := range []string{"state", "category", "status", "channel"} {
		filterDefinitions[id] = dashboardfilter.Definition{
			Label: strings.ToUpper(id[:1]) + id[1:], Field: "orders." + id,
			Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}},
			Options:    dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceDistinct, Limit: 50},
		}
		filterBindings[id] = dashboardfilter.Binding{
			Filter:  id,
			Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
			URL:     dashboardfilter.URLPolicy{Param: id, Encoding: dashboardfilter.URLEncodingTypedV1},
		}
	}
	visuals := map[string]reportdef.Visual{}
	components := []dashboard.PageVisual{}
	for i := range 8 {
		id := "visual_" + string(rune('a'+i))
		visuals[id] = reportdef.Visual{
			Title: "Benchmark Visual " + string(rune('A'+i)),
			Type:  "bar",
			Query: reportdef.VisualQuery{
				Dimensions: fieldRefs("orders.status"),
				Measures:   fieldRefs("order_count"),
			},
		}
		components = append(components, dashboard.PageVisual{ID: id, Kind: "visual", Visual: id, X: float64((i % 4) * 300), Y: float64((i / 4) * 180), Width: 280, Height: 160})
	}
	for i, filterID := range []string{"state", "category", "status", "channel"} {
		components = append(components, dashboard.PageVisual{
			ID: filterID + "_slicer", Kind: "slicer",
			Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: filterID},
			X:       float64(i * 220), Y: 390, Width: 200, Height: 120,
		})
	}
	tables := map[string]reportdef.TableVisual{}
	for i := 0; i < 4; i++ {
		id := "table_" + string(rune('a'+i))
		tables[id] = reportdef.TableVisual{
			Title: "Benchmark Table " + string(rune('A'+i)),
			Query: reportdef.TableQuery{Table: "orders", Fields: []string{"orders.order_id", "orders.status", "orders.state", "orders.category"}},
			Style: dashboard.TableStyle{Density: "compact", Grid: "full", Zebra: &zebra},
			Columns: []dashboard.TableColumn{
				{Key: "order_id", Label: "Order", Width: 180, Format: "text"},
				{Key: "status", Label: "Status", Width: 140, Format: "text"},
				{Key: "state", Label: "State", Width: 100, Format: "text"},
				{Key: "category", Label: "Category", Width: 180, Format: "text"},
			},
		}
		components = append(components, dashboard.PageVisual{ID: id, Kind: "visual", Visual: id, X: float64(i * 300), Y: 540, Width: 280, Height: 220})
	}
	report := reportdef.Dashboard{
		ID:                "benchmark-dashboard",
		Title:             "Benchmark Dashboard",
		SemanticModel:     "benchmark",
		FilterDefinitions: filterDefinitions,
		Visuals:           reportdef.MergeVisualizations(reportdef.ChartVisualizations(visuals), reportdef.TabularVisualizations("table", tables)),
		Pages: []dashboard.Page{{
			ID:             "overview",
			Title:          "Overview",
			Canvas:         dashboard.PageCanvas{Width: 1366, Height: 940},
			Grid:           dashboard.PageGrid{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16},
			FilterBindings: filterBindings,
			Visuals:        components,
		}},
	}
	model := &semanticmodel.Model{
		Name:  "benchmark",
		Title: "Benchmark Semantic Model",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Grain:      "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Expr: "order_id", Type: "string"},
					"status":   {Expr: "status", Type: "string"},
					"state":    {Expr: "state", Type: "string"},
					"category": {Expr: "category", Type: "string"},
					"channel":  {Expr: "channel", Type: "string"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero", Label: "Orders"}},
	}
	catalog := catalog.Catalog{Workspace: catalog.Workspace{ID: "benchmark", Title: "Benchmark Workspace"}}
	return report, model, catalog
}
