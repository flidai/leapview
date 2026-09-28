package http

import (
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestProjectDataExplorerViewsUsesGovernedResultAndSharedValidation(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	result := explorerVisualizationTestResult()
	views, recommended, warnings := ProjectDataExplorerViews(spec, result, explorerVisualizationTestFields())
	if recommended != "table" {
		t.Fatalf("recommended view = %q, want table", recommended)
	}
	if got := len(warnings); got != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	for _, id := range []string{"table", "chart"} {
		envelope, ok := views[id]
		if !ok {
			t.Fatalf("view %q is missing: %#v", id, views)
		}
		if err := visualizationir.ValidateEnvelope(envelope); err != nil {
			t.Fatalf("view %q did not validate: %v", id, err)
		}
		if envelope.DataRevision != result.RequestSeq {
			t.Errorf("view %q data revision = %d, want current result sequence %d", id, envelope.DataRevision, result.RequestSeq)
		}
		if envelope.SpecRevision == "" {
			t.Errorf("view %q has no specification revision", id)
		}
	}
	if _, ok := views["pivot"]; ok {
		t.Fatal("ordinary aggregate result unexpectedly produced a pivot")
	}
	chart := views["chart"]
	if chart.RendererID != "echarts" || chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec).X.Field != "status" {
		t.Fatalf("chart view = %#v", chart)
	}
}

func TestProjectDataExplorerViewsBoundsRowsAndMarksTruncation(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	spec.Limit = 1
	result := explorerVisualizationTestResult()
	result.RequestSeq = 12
	result.Rows = append(result.Rows, map[string]any{"status": "returned", "revenue": float64(8)})
	views, _, warnings := ProjectDataExplorerViews(spec, result, explorerVisualizationTestFields())
	if len(warnings) == 0 {
		t.Fatal("bounded result did not report truncation")
	}
	tableState, ok := views["table"].DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok {
		t.Fatalf("table state = %T", views["table"].DataState.Value)
	}
	if tableState.AvailableRows != 1 || tableState.Cardinality.Kind != visualizationir.VisualizationCardinalityKindLowerBound {
		t.Fatalf("table state does not retain truncation: %#v", tableState)
	}
	chartState, ok := views["chart"].DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if !ok || len(chartState.Datasets) != 1 || len(chartState.Datasets[0].Rows) != 1 || chartState.Datasets[0].Completeness != visualizationir.VisualizationCompletenessTruncated {
		t.Fatalf("chart state does not retain bounded completeness: %#v", views["chart"].DataState.Value)
	}
	for id, envelope := range views {
		if err := visualizationir.ValidateEnvelope(envelope); err != nil {
			t.Errorf("view %q did not validate after truncation: %v", id, err)
		}
	}
}

func TestProjectDataExplorerViewsRejectsFailedOrUnsafeInputs(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	fields := explorerVisualizationTestFields()
	failed := explorerVisualizationTestResult()
	failed.Error = projectsignals.Pointer("governed query failed")
	views, _, _ := ProjectDataExplorerViews(spec, failed, fields)
	if len(views) != 0 {
		t.Fatalf("failed result produced views: %#v", views)
	}

	unsafeChart := explorerVisualizationTestSpec()
	unsafeChart.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMark("waterfall")}}
	views, _, warnings := ProjectDataExplorerViews(unsafeChart, explorerVisualizationTestResult(), fields)
	if _, ok := views["table"]; !ok {
		t.Fatalf("safe table view missing for unsupported chart config: %#v", views)
	}
	if _, ok := views["chart"]; ok || len(warnings) == 0 {
		t.Fatalf("unsupported chart config was not rejected: views=%#v warnings=%#v", views, warnings)
	}

	badFrame := explorerVisualizationTestResult()
	badFrame.Rows[0]["revenue"] = "not-a-number"
	views, _, _ = ProjectDataExplorerViews(spec, badFrame, fields)
	if len(views) != 0 {
		t.Fatalf("non-canonical metric value produced a view: %#v", views)
	}
}

func TestProjectDataExplorerViewsKeepsSeriesChartsReadable(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: "orders.region"})
	fields := append(explorerVisualizationTestFields(), projectsignals.DataExploreFieldSignal{ID: "orders.region", Kind: "dimension", Label: "Region", DatasetID: "orders", Type: projectsignals.Optional("string"), Compatible: true})
	result := explorerVisualizationTestResult()
	result.Columns = append(result.Columns, projectsignals.DataPreviewColumnSignal{Key: "region", Label: "Region"})
	result.Rows[0]["region"] = "west"
	result.Truncated = true
	views, _, warnings := ProjectDataExplorerViews(spec, result, fields)
	if _, ok := views["chart"]; ok || len(warnings) == 0 {
		t.Fatalf("truncated series chart should be omitted: views=%#v warnings=%#v", views, warnings)
	}

	result.Truncated = false
	result.Rows = []map[string]any{{"status": "delivered", "region": "west", "revenue": float64(42)}, {"status": "pending", "region": "east", "revenue": float64(8)}}
	views, _, warnings = ProjectDataExplorerViews(spec, result, fields)
	chart, ok := views["chart"]
	if !ok || len(warnings) > 0 {
		t.Fatalf("small complete series chart should remain available: views=%#v warnings=%#v", views, warnings)
	}
	if chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec).Presentation.Legend != visualizationir.VisualizationLegendPositionBottom {
		t.Fatal("series chart should identify its colors with a visible legend")
	}

	result.Rows = make([]map[string]any, 9)
	for index := range result.Rows {
		result.Rows[index] = map[string]any{"status": "delivered", "region": string(rune('a' + index)), "revenue": float64(index + 1)}
	}
	views, _, warnings = ProjectDataExplorerViews(spec, result, fields)
	if _, ok := views["chart"]; ok || len(warnings) == 0 {
		t.Fatalf("chart with more than eight series should be omitted: views=%#v warnings=%#v", views, warnings)
	}
}

func TestProjectDataExplorerViewsOmitsTruncatedOrAmbiguousPivot(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: "orders.region"})
	spec.Pivot = &exploration.ExplorationPivotConfig{
		Rows:    []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Columns: []exploration.ExplorationDimensionRef{{Field: "orders.region"}},
		Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
	}
	fields := explorerVisualizationTestFields()
	fields = append(fields, projectsignals.DataExploreFieldSignal{ID: "orders.region", Kind: "dimension", Label: "Region", DatasetID: "orders", Type: projectsignals.Optional("string"), Compatible: true})
	result := explorerVisualizationTestResult()
	result.Columns = append(result.Columns, projectsignals.DataPreviewColumnSignal{Key: "region", Label: "Region"})
	result.Rows[0]["region"] = "west"
	result.Truncated = true
	views, _, warnings := ProjectDataExplorerViews(spec, result, fields)
	if _, ok := views["pivot"]; ok {
		t.Fatalf("truncated result produced a pivot: %#v", views["pivot"])
	}
	if len(warnings) == 0 {
		t.Fatal("omitted truncated pivot did not produce a warning")
	}

	result.Truncated = false
	result.Rows = append(result.Rows, map[string]any{"status": "delivered", "region": "west", "revenue": float64(6)})
	views, _, warnings = ProjectDataExplorerViews(spec, result, fields)
	if _, ok := views["pivot"]; ok {
		t.Fatalf("duplicate pivot cell produced an ambiguous pivot: %#v", views["pivot"])
	}
	if len(warnings) == 0 {
		t.Fatal("omitted duplicate-cell pivot did not produce a warning")
	}
}

func TestProjectDataExplorerViewsBuildsOnlyValidatedBoundedPivot(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	spec.Dimensions = append(spec.Dimensions, exploration.ExplorationDimensionRef{Field: "orders.region"})
	spec.Pivot = &exploration.ExplorationPivotConfig{
		Rows:    []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Columns: []exploration.ExplorationDimensionRef{{Field: "orders.region"}},
		Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
	}
	fields := explorerVisualizationTestFields()
	fields = append(fields, projectsignals.DataExploreFieldSignal{ID: "orders.region", Kind: "dimension", Label: "Region", DatasetID: "orders", Type: projectsignals.Optional("string"), Compatible: true})
	result := explorerVisualizationTestResult()
	result.Columns = append(result.Columns, projectsignals.DataPreviewColumnSignal{Key: "region", Label: "Region"})
	result.Rows = []map[string]any{
		{"status": "delivered", "region": "west", "revenue": float64(42)},
		{"status": "pending", "region": "east", "revenue": float64(8)},
	}
	views, _, warnings := ProjectDataExplorerViews(spec, result, fields)
	pivot, ok := views["pivot"]
	if !ok {
		t.Fatalf("safe pivot view missing: views=%#v warnings=%#v", views, warnings)
	}
	if err := visualizationir.ValidateEnvelope(pivot); err != nil {
		t.Fatalf("pivot envelope did not validate: %v", err)
	}
	state, ok := pivot.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || len(state.Schema.Fields) != 3 || state.AvailableRows != 2 {
		t.Fatalf("unexpected bounded pivot state: %#v", pivot.DataState.Value)
	}
	if pivot.Spec.Value.(*visualizationir.PivotVisualizationSpec).Kind != "pivot" {
		t.Fatalf("pivot spec = %#v", pivot.Spec.Value)
	}
}

func explorerVisualizationTestSpec() exploration.ExplorationSpec {
	dataset := "orders"
	return exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters:    []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10,
	}
}

func explorerVisualizationTestFields() []projectsignals.DataExploreFieldSignal {
	return []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Kind: "dimension", Label: "Status", DatasetID: "orders", Type: projectsignals.Optional("string"), Compatible: true},
		{ID: "revenue", Kind: "metric", Label: "Revenue", DatasetID: "orders", Type: projectsignals.Optional("sum"), Compatible: true},
	}
}

func explorerVisualizationTestResult() projectsignals.DataExploreResultSignal {
	return projectsignals.DataExploreResultSignal{
		RequestSeq: 7,
		Columns:    []projectsignals.DataPreviewColumnSignal{{Key: "status", Label: "Status"}, {Key: "revenue", Label: "Revenue"}},
		Rows:       []map[string]any{{"status": "delivered", "revenue": float64(42)}},
		Warnings:   []string{},
	}
}
