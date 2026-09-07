package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerProjectionResult(columns []projectsignals.DataPreviewColumnSignal, rows []map[string]any, requestSeq int64) projectsignals.DataExploreResultSignal {
	return projectsignals.DataExploreResultSignal{Columns: columns, Rows: rows, RequestSeq: requestSeq, Warnings: []string{}}
}

func explorerProjectionColumn(key, label, typ string) projectsignals.DataPreviewColumnSignal {
	return projectsignals.DataPreviewColumnSignal{Key: key, Label: label, Type: projectsignals.Pointer(typ)}
}

func explorerProjectionField(id, kind, label, typ string) projectsignals.DataExploreFieldSignal {
	return projectsignals.DataExploreFieldSignal{ID: id, Kind: kind, Label: label, Type: projectsignals.Pointer(typ), DatasetID: "orders", Compatible: true}
}

func TestProjectDataExplorerViewsInfersKPIAndSharesRevision(t *testing.T) {
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("revenue", "Revenue", "decimal")}, []map[string]any{{"revenue": "42.50"}}, 17)
	projection := ProjectDataExplorerViews(spec, result, []projectsignals.DataExploreFieldSignal{explorerProjectionField("revenue", "metric", "Revenue", "decimal")})
	if projection.RecommendedView != dataExplorerKPIViewID || projection.DefaultView != dataExplorerTableViewID {
		t.Fatalf("view selection = %#v, want KPI recommendation and table default", projection)
	}
	if _, ok := projection.Views[dataExplorerKPIViewID]; !ok {
		t.Fatalf("views = %#v, want KPI view", projection.Views)
	}
	if got := projection.Views[dataExplorerKPIViewID].DataRevision; got != 17 {
		t.Fatalf("KPI data revision = %d, want request revision 17", got)
	}
	if got := projection.Views[dataExplorerTableViewID].DataRevision; got != 17 {
		t.Fatalf("table data revision = %d, want request revision 17", got)
	}
}

func TestProjectDataExplorerViewsInferenceBounds(t *testing.T) {
	base := func(dimensions []exploration.ExplorationDimensionRef, rows []map[string]any) DataExplorerVisualizationProjection {
		spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: dimensions, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
		fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
		columns := []projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("revenue", "Revenue", "decimal")}
		for _, dimension := range dimensions {
			kind, typ := "dimension", "string"
			if dimension.Field == "created_at" {
				typ = "timestamp"
			}
			fields = append(fields, explorerProjectionField(dimension.Field, kind, dimension.Field, typ))
			columns = append(columns, explorerProjectionColumn(dimension.Field, dimension.Field, typ))
		}
		return ProjectDataExplorerViews(spec, explorerProjectionResult(columns, rows, 1), fields)
	}

	t.Run("donut requires seven or fewer nonnegative categories", func(t *testing.T) {
		rows := []map[string]any{{"status": "paid", "revenue": "2.00"}, {"status": "open", "revenue": "1.00"}}
		projection := base([]exploration.ExplorationDimensionRef{{Field: "status"}}, rows)
		if projection.RecommendedView != dataExplorerDonutViewID {
			t.Fatalf("recommended = %q, want donut; warnings=%#v views=%#v", projection.RecommendedView, projection.Warnings, projection.Views)
		}
	})
	t.Run("more than seven categories falls back to bar", func(t *testing.T) {
		rows := make([]map[string]any, 8)
		for index := range rows {
			rows[index] = map[string]any{"status": string(rune('a' + index)), "revenue": "1.00"}
		}
		projection := base([]exploration.ExplorationDimensionRef{{Field: "status"}}, rows)
		if projection.RecommendedView != dataExplorerBarViewID {
			t.Fatalf("recommended = %q, want bar; warnings=%#v views=%#v", projection.RecommendedView, projection.Warnings, projection.Views)
		}
	})
	t.Run("two dimensions become series, three dimensions table", func(t *testing.T) {
		rows := []map[string]any{{"status": "paid", "channel": "web", "revenue": "1.00"}}
		projection := base([]exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}}, rows)
		if projection.RecommendedView != dataExplorerBarViewID {
			t.Fatalf("two dimensions recommended = %q, want bar series; warnings=%#v views=%#v", projection.RecommendedView, projection.Warnings, projection.Views)
		}
		projection = base([]exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}, {Field: "region"}}, append(rows, map[string]any{"region": "us", "revenue": "1.00"}))
		if projection.RecommendedView != dataExplorerTableViewID {
			t.Fatalf("three dimensions recommended = %q, want table", projection.RecommendedView)
		}
	})
	t.Run("temporal dimensions use line even for negative metrics", func(t *testing.T) {
		projection := base([]exploration.ExplorationDimensionRef{{Field: "created_at"}}, []map[string]any{{"created_at": "2026-01-01", "revenue": "-2.00"}})
		if projection.RecommendedView != dataExplorerLineViewID {
			t.Fatalf("recommended = %q, want line; warnings=%#v views=%#v", projection.RecommendedView, projection.Warnings, projection.Views)
		}
	})
}

func TestProjectDataExplorerViewsInfersTemporalSeriesWithMultipleMetrics(t *testing.T) {
	fields := []projectsignals.DataExploreFieldSignal{
		explorerProjectionField("created_at", "dimension", "Created at", "timestamp"),
		explorerProjectionField("channel", "dimension", "Channel", "string"),
		explorerProjectionField("revenue", "metric", "Revenue", "decimal"),
		explorerProjectionField("cost", "metric", "Cost", "decimal"),
	}
	columns := []projectsignals.DataPreviewColumnSignal{
		explorerProjectionColumn("created_at", "Created at", "timestamp"),
		explorerProjectionColumn("channel", "Channel", "string"),
		explorerProjectionColumn("revenue", "Revenue", "decimal"),
		explorerProjectionColumn("cost", "Cost", "decimal"),
	}
	rows := []map[string]any{{"created_at": "2026-01-01", "channel": "web", "revenue": "2.00", "cost": "1.00"}}
	for _, dimensions := range [][]exploration.ExplorationDimensionRef{
		{{Field: "created_at"}, {Field: "channel"}},
		{{Field: "channel"}, {Field: "created_at"}},
	} {
		spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: dimensions, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}, {Field: "cost"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
		projection := ProjectDataExplorerViews(spec, explorerProjectionResult(columns, rows, 1), fields)
		if projection.RecommendedView != dataExplorerLineViewID {
			t.Fatalf("dimensions=%#v recommended=%q warnings=%#v, want temporal line", dimensions, projection.RecommendedView, projection.Warnings)
		}
		chart, ok := projection.Views[dataExplorerLineViewID]
		if !ok {
			t.Fatalf("dimensions=%#v views=%#v, want line envelope", dimensions, projection.Views)
		}
		value, ok := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
		if !ok || value.X.Field != "created_at" || value.Series == nil || value.Series.Field != "channel" || len(value.Y) != 2 {
			t.Fatalf("dimensions=%#v chart=%#v, want temporal X/category series and two metrics", dimensions, chart.Spec.Value)
		}
	}
}

func TestProjectDataExplorerViewsExplicitOverrideAndSafePivotFallback(t *testing.T) {
	dataset := projectsignals.Optional("orders")
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100, Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine}}}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}, []map[string]any{{"status": "paid", "revenue": "1.00"}}, 4)
	fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
	projection := ProjectDataExplorerViews(spec, result, fields)
	if projection.RecommendedView != dataExplorerLineViewID {
		t.Fatalf("explicit recommendation = %q, want line", projection.RecommendedView)
	}

	spec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.PointExplorationVisualization{Kind: "point", Mark: "point", X: exploration.ExplorationVisualizationFieldRef{Field: "status"}, Y: exploration.ExplorationVisualizationFieldRef{Field: "revenue"}}}
	projection = ProjectDataExplorerViews(spec, result, fields)
	if projection.RecommendedView != dataExplorerTableViewID || len(projection.Warnings) == 0 || !strings.Contains(projection.Warnings[len(projection.Warnings)-1], "unsupported authored visualization") {
		t.Fatalf("unsupported override = %#v, want table warning", projection)
	}

	spec.Visualization = nil
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}}
	spec.Metrics = []exploration.ExplorationMetricRef{{Field: "revenue"}}
	spec.Pivot = &exploration.ExplorationPivotConfig{Rows: []exploration.ExplorationDimensionRef{{Field: "status"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}}
	projection = ProjectDataExplorerViews(spec, result, fields)
	if projection.RecommendedView != dataExplorerTableViewID {
		t.Fatalf("pivot recommendation = %q, want table fallback", projection.RecommendedView)
	}
	for key := range projection.Views {
		if key == dataExplorerPivotViewID {
			t.Fatalf("unsafe long-form result emitted pivot view: %#v", projection.Views[key])
		}
	}
	if len(projection.Warnings) == 0 || !strings.Contains(projection.Warnings[len(projection.Warnings)-1], "safe wide pivot") {
		t.Fatalf("pivot warnings = %#v, want safe-shape warning", projection.Warnings)
	}
}

func TestProjectDataExplorerViewsEmitsSelectInteractionsForDrillLineage(t *testing.T) {
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}, []map[string]any{{"status": "paid", "revenue": "1.00"}}, 3)
	projection := ProjectDataExplorerViews(spec, result, []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")})
	chart, ok := projection.Views[dataExplorerDonutViewID]
	if !ok {
		t.Fatalf("views = %#v, want donut", projection.Views)
	}
	base, err := chart.Spec.Base()
	if err != nil || base == nil || len(base.Interactions) != 1 || base.Interactions[0].Kind != "select" {
		t.Fatalf("interactions = %#v, want one select interaction", base)
	}
	mapping := base.Interactions[0].Mappings[0]
	if mapping.TargetFieldID != "status" || projectsignals.ValueOrZero(mapping.TargetDatasetID) != "orders" {
		t.Fatalf("interaction mapping = %#v, want semantic status/orders target", mapping)
	}
}

func TestProjectDataExplorerViewsAuthoredSeriesAndTablePresentation(t *testing.T) {
	label := "Orders by channel"
	series := exploration.ExplorationVisualizationFieldRef{Field: "channel"}
	y := []exploration.ExplorationVisualizationFieldRef{{Field: "revenue"}}
	displayColumns := []exploration.ExplorationTableColumn{{Field: "revenue", Label: projectsignals.Pointer("Gross revenue"), Width: projectsignals.Pointer(int32(180)), Format: &exploration.VisualizationFormat{Value: &exploration.CurrencyVisualizationFormat{Kind: "currency", Currency: "USD"}}}}
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100, Table: &exploration.ExplorationTableDisplayConfig{Columns: &displayColumns, Density: projectsignals.Pointer(exploration.ExplorationTableDensityCompact), Striped: projectsignals.Pointer(true), ShowHeader: projectsignals.Pointer(false)}, Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{ExplorationVisualizationConfigBase: exploration.ExplorationVisualizationConfigBase{Title: &label}, Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine, Series: &series, Y: &y}}}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("channel", "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}, []map[string]any{{"status": "paid", "channel": "web", "revenue": "1.00"}}, 7)
	fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("channel", "dimension", "Channel", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
	projection := ProjectDataExplorerViews(spec, result, fields)
	chart, ok := projection.Views[dataExplorerLineViewID]
	if !ok {
		t.Fatalf("views = %#v, want authored line chart", projection.Views)
	}
	chartSpec, ok := chart.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || chartSpec.Series == nil || chartSpec.Series.Field != "channel" {
		t.Fatalf("authored series = %#v, want channel series", chart.Spec.Value)
	}
	table, ok := projection.Views[dataExplorerTableViewID]
	if !ok {
		t.Fatalf("views = %#v, want table", projection.Views)
	}
	tableSpec, ok := table.Spec.Value.(*visualizationir.TableVisualizationSpec)
	if !ok || len(tableSpec.Columns) != 1 || tableSpec.Columns[0].Label != "Gross revenue" || tableSpec.Columns[0].Width == nil || *tableSpec.Columns[0].Width != 180 {
		t.Fatalf("table columns = %#v, want authored label/width", table.Spec.Value)
	}
	if got := tableSpec.Presentation.RowHeight; got != 28 || !tableSpec.Presentation.Striped || tableSpec.Presentation.ShowHeader {
		t.Fatalf("table presentation = %#v, want compact/striped=true/showHeader=false", tableSpec.Presentation)
	}
	base, err := table.Spec.Base()
	if err != nil || len(base.Datasets) != 1 || len(base.Datasets[0].Fields) != 1 || base.Datasets[0].Fields[0].Format == nil {
		t.Fatalf("table schema format = %#v, want currency format", base)
	}
	tableState, ok := table.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || tableState.ChunkSize < tableState.AvailableRows {
		t.Fatalf("table window chunk size/state = %#v, want chunkSize >= availableRows", table.DataState.Value)
	}
}

func TestProjectDataExplorerViewsProjectsCanonicalSortIntoWindowState(t *testing.T) {
	channelAlias := "sales_channel"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "channel", Alias: &channelAlias}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{},
		Sort: []exploration.ExplorationSort{{Field: channelAlias, Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 100,
	}
	fields := []projectsignals.DataExploreFieldSignal{
		explorerProjectionField("channel", "dimension", "Channel", "string"),
		explorerProjectionField("revenue", "metric", "Revenue", "decimal"),
	}
	columns := []projectsignals.DataPreviewColumnSignal{explorerProjectionColumn(channelAlias, "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}
	result := explorerProjectionResult(columns, []map[string]any{{channelAlias: "web", "revenue": "1.00"}}, 31)
	projection := ProjectDataExplorerViews(spec, result, fields)
	table, ok := projection.Views[dataExplorerTableViewID]
	if !ok {
		t.Fatalf("views=%#v, want table", projection.Views)
	}
	state, ok := table.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || len(state.Sort) != 1 || state.Sort[0].Field.Field != channelAlias || state.Sort[0].Direction != visualizationir.VisualizationSortDirectionDescending {
		t.Fatalf("table window sort=%#v, want alias %q descending", table.DataState.Value, channelAlias)
	}
	if block := state.Blocks["a"]; len(block.Sort) != 1 || block.Sort[0].Field.Field != channelAlias || block.Sort[0].Direction != visualizationir.VisualizationSortDirectionDescending {
		t.Fatalf("table block sort=%#v, want alias %q descending", block.Sort, channelAlias)
	}

	statusAlias := "status_label"
	channelPivotAlias := "channel_label"
	pivotSort := []exploration.ExplorationSort{{Field: statusAlias, Direction: exploration.ExplorationSortDirectionDesc}}
	pivotSpec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		Pivot: &exploration.ExplorationPivotConfig{
			Rows:    []exploration.ExplorationDimensionRef{{Field: "status", Alias: &statusAlias}},
			Columns: []exploration.ExplorationDimensionRef{{Field: "channel", Alias: &channelPivotAlias}},
			Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Sort: &pivotSort,
		},
	}
	pivotFields := []projectsignals.DataExploreFieldSignal{
		explorerProjectionField("status", "dimension", "Status", "string"),
		explorerProjectionField("channel", "dimension", "Channel", "string"),
		explorerProjectionField("revenue", "metric", "Revenue", "decimal"),
	}
	pivotColumns := []projectsignals.DataPreviewColumnSignal{
		explorerProjectionColumn(statusAlias, "Status", "string"), explorerProjectionColumn(channelPivotAlias, "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal"),
	}
	pivotResult := explorerProjectionResult(pivotColumns, []map[string]any{{statusAlias: "paid", channelPivotAlias: "web", "revenue": "1.00"}}, 32)
	pivotProjection := ProjectDataExplorerViews(pivotSpec, pivotResult, pivotFields)
	pivot, ok := pivotProjection.Views[dataExplorerPivotViewID]
	if !ok {
		t.Fatalf("pivot views=%#v, want pivot", pivotProjection.Views)
	}
	pivotState, ok := pivot.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || len(pivotState.Sort) != 1 || pivotState.Sort[0].Field.Field != statusAlias || pivotState.Sort[0].Direction != visualizationir.VisualizationSortDirectionDescending {
		t.Fatalf("pivot window sort=%#v, want alias %q descending", pivot.DataState.Value, statusAlias)
	}
	if block := pivotState.Blocks["a"]; len(block.Sort) != 1 || block.Sort[0].Field.Field != statusAlias || block.Sort[0].Direction != visualizationir.VisualizationSortDirectionDescending {
		t.Fatalf("pivot block sort=%#v, want alias %q descending", block.Sort, statusAlias)
	}
}

func TestProjectDataExplorerViewsAuthoredMetricOverridesRemainValidAtMultiMetricGrain(t *testing.T) {
	fields := []projectsignals.DataExploreFieldSignal{
		explorerProjectionField("status", "dimension", "Status", "string"),
		explorerProjectionField("channel", "dimension", "Channel", "string"),
		explorerProjectionField("revenue", "metric", "Revenue", "decimal"),
		explorerProjectionField("cost", "metric", "Cost", "decimal"),
	}
	columns := []projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("channel", "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal"), explorerProjectionColumn("cost", "Cost", "decimal")}
	rows := []map[string]any{{"status": "paid", "channel": "web", "revenue": "2.00", "cost": "1.00"}}
	base := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}, {Field: "cost"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	metricKPI := exploration.ExplorationVisualizationFieldRef{Field: "revenue"}
	kpiSpec := base
	kpiSpec.Dimensions = []exploration.ExplorationDimensionRef{}
	kpiSpec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.KPIExplorationVisualization{Kind: "kpi", Value: metricKPI}}
	scalarColumns := []projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("revenue", "Revenue", "decimal"), explorerProjectionColumn("cost", "Cost", "decimal")}
	scalarRows := []map[string]any{{"revenue": "2.00", "cost": "1.00"}}
	projection := ProjectDataExplorerViews(kpiSpec, explorerProjectionResult(scalarColumns, scalarRows, 1), fields)
	if projection.RecommendedView != dataExplorerKPIViewID {
		t.Fatalf("authored KPI with multi-metric result = %#v, want KPI", projection)
	}
	malformedKPI := explorerProjectionResult(scalarColumns, []map[string]any{{"revenue": "2.00", "cost": "1.00"}, {"revenue": "3.00", "cost": "1.50"}}, 11)
	projection = ProjectDataExplorerViews(kpiSpec, malformedKPI, fields)
	if projection.RecommendedView != dataExplorerTableViewID || !strings.Contains(strings.Join(projection.Warnings, " "), "exactly one scalar row") {
		t.Fatalf("authored KPI with multiple scalar rows = %#v, want table fallback", projection)
	}
	groupedKPI := base
	groupedKPI.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.KPIExplorationVisualization{Kind: "kpi", Value: metricKPI}}
	projection = ProjectDataExplorerViews(groupedKPI, explorerProjectionResult(columns, rows, 2), fields)
	if projection.RecommendedView != dataExplorerTableViewID || !strings.Contains(strings.Join(projection.Warnings, " "), "scalar result") {
		t.Fatalf("authored KPI with grouped result = %#v, want table fallback", projection)
	}
	category := exploration.ExplorationVisualizationFieldRef{Field: "status"}
	value := exploration.ExplorationVisualizationFieldRef{Field: "cost"}
	proportionalSpec := base
	proportionalSpec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "status"}}
	proportionalSpec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.ProportionalExplorationVisualization{Kind: "proportional", Mark: exploration.VisualizationProportionalMarkDonut, Category: category, Value: value}}
	projection = ProjectDataExplorerViews(proportionalSpec, explorerProjectionResult(columns, rows, 2), fields)
	if projection.RecommendedView != dataExplorerDonutViewID {
		t.Fatalf("authored proportional with multi-metric/dimension result = %#v, want donut", projection)
	}
	unsafeProportional := base
	unsafeProportional.Visualization = proportionalSpec.Visualization
	projection = ProjectDataExplorerViews(unsafeProportional, explorerProjectionResult(columns, rows, 3), fields)
	if projection.RecommendedView != dataExplorerTableViewID || !strings.Contains(strings.Join(projection.Warnings, " "), "exactly one selected categorical dimension") {
		t.Fatalf("authored proportional with grouped result = %#v, want table fallback", projection)
	}
}

func TestProjectDataExplorerViewsRejectsAuthoredCartesianXImplicitSeriesCollision(t *testing.T) {
	fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("channel", "dimension", "Channel", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
	columns := []projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("channel", "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}
	x := exploration.ExplorationVisualizationFieldRef{Field: "channel"}
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}, {Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100, Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine, X: &x}}}
	projection := ProjectDataExplorerViews(spec, explorerProjectionResult(columns, []map[string]any{{"status": "paid", "channel": "web", "revenue": "1.00"}}, 3), fields)
	if projection.RecommendedView != dataExplorerTableViewID || !strings.Contains(strings.Join(projection.Warnings, " "), "x and series") {
		t.Fatalf("implicit series collision = %#v, want table fallback", projection)
	}
}

func TestProjectDataExplorerViewsSafePivotShapeAndFallbacks(t *testing.T) {
	rows := []map[string]any{{"status": "paid", "channel": "web", "revenue": "1.00"}, {"status": "paid", "channel": "mobile", "revenue": "2.00"}, {"status": "open", "channel": "web", "revenue": "3.00"}}
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10, Pivot: &exploration.ExplorationPivotConfig{Rows: []exploration.ExplorationDimensionRef{{Field: "status"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}}}
	fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("channel", "dimension", "Channel", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("channel", "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}, rows, 9)
	projection := ProjectDataExplorerViews(spec, result, fields)
	if projection.RecommendedView != dataExplorerPivotViewID {
		t.Fatalf("recommended = %q, warnings=%#v, want safe pivot", projection.RecommendedView, projection.Warnings)
	}
	envelope, ok := projection.Views[dataExplorerPivotViewID]
	if !ok {
		t.Fatalf("views = %#v, want pivot", projection.Views)
	}
	state, ok := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || len(state.Schema.Fields) != 3 || state.Schema.Fields[1].Grid == nil || state.Schema.Fields[1].Grid.ColumnValue == nil {
		t.Fatalf("pivot state schema = %#v, want row plus dynamic grid metadata", envelope.DataState.Value)
	}
	if got := envelope.DataRevision; got != 9 {
		t.Fatalf("pivot data revision = %d, want 9", got)
	}

	truncated := result
	truncated.Truncated = true
	projection = ProjectDataExplorerViews(spec, truncated, fields)
	if _, ok := projection.Views[dataExplorerPivotViewID]; ok || !strings.Contains(strings.Join(projection.Warnings, " "), "truncated") {
		t.Fatalf("truncated pivot = %#v, want table fallback warning", projection)
	}

	totals := spec
	totals.Pivot = &exploration.ExplorationPivotConfig{Rows: spec.Pivot.Rows, Columns: spec.Pivot.Columns, Metrics: spec.Pivot.Metrics, Totals: &exploration.ExplorationPivotTotals{Grand: projectsignals.Pointer(true)}}
	projection = ProjectDataExplorerViews(totals, result, fields)
	if _, ok := projection.Views[dataExplorerPivotViewID]; ok || !strings.Contains(strings.Join(projection.Warnings, " "), "totals") {
		t.Fatalf("totals pivot = %#v, want table fallback warning", projection)
	}

	tooWideRows := make([]map[string]any, 0, dataExplorerPivotMaxColumns+1)
	for index := 0; index <= dataExplorerPivotMaxColumns; index++ {
		tooWideRows = append(tooWideRows, map[string]any{"status": "paid", "channel": string(rune('a' + index)), "revenue": "1.00"})
	}
	projection = ProjectDataExplorerViews(spec, resultWithRows(result, tooWideRows), fields)
	if _, ok := projection.Views[dataExplorerPivotViewID]; ok || !strings.Contains(strings.Join(projection.Warnings, " "), "exceeds") {
		t.Fatalf("wide pivot = %#v, want bounded table fallback", projection)
	}
}

func resultWithRows(result projectsignals.DataExploreResultSignal, rows []map[string]any) projectsignals.DataExploreResultSignal {
	result.Rows = rows
	return result
}

type recordingPivotTotalsExecutor struct {
	queries []dataquery.Query
	results []dataquery.Result
	errs    []error
}

func (e *recordingPivotTotalsExecutor) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	e.queries = append(e.queries, query)
	index := len(e.queries) - 1
	if index >= len(e.results) {
		return dataquery.Result{}, nil
	}
	if index < len(e.errs) && e.errs[index] != nil {
		return e.results[index], e.errs[index]
	}
	return e.results[index], nil
}

func TestDataExplorerPivotTotalsAreExactAndGoverned(t *testing.T) {
	dataset := "orders"
	lower := "2026-01-01"
	upper := "2026-02-01"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic-model:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.unrelated"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{{Field: "orders.unrelated", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 10,
		Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainDay, Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{Kind: "date", Value: lower}}, Inclusive: true}, Upper: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{Kind: "date", Value: upper}}, Inclusive: false}}}},
		Pivot: &exploration.ExplorationPivotConfig{
			Rows: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "orders.channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
			Sort:   &[]exploration.ExplorationSort{{Field: "orders.status", Direction: exploration.ExplorationSortDirectionAsc}},
			Totals: &exploration.ExplorationPivotTotals{Rows: projectsignals.Pointer(true), Columns: projectsignals.Pointer(true), Grand: projectsignals.Pointer(true)},
		},
	}
	fields := []projectsignals.DataExploreFieldSignal{
		explorerProjectionField("orders.status", "dimension", "Status", "string"),
		explorerProjectionField("orders.channel", "dimension", "Channel", "string"),
		explorerProjectionField("orders.unrelated", "dimension", "Unrelated", "string"),
		explorerProjectionField("orders.created_at", "dimension", "Created at", "date"),
		explorerProjectionField("revenue", "metric", "Revenue", "decimal"),
	}
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order": {Type: "primary", Fields: []string{"status", "channel", "created_at", "revenue"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status":     {Field: "orders.status", Type: "string"},
					"channel":    {Field: "orders.channel", Type: "string"},
					"unrelated":  {Field: "orders.unrelated", Type: "string"},
					"created_at": {Field: "orders.created_at", Type: "date", Datatype: semanticmodel.DataTypeDate},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	executor := &recordingPivotTotalsExecutor{results: []dataquery.Result{
		{Columns: []dataquery.Column{{Name: "status"}, {Name: "channel"}, {Name: "revenue"}}, Rows: []dataquery.Row{{"status": "paid", "channel": "web", "revenue": "1.00"}, {"status": "paid", "channel": "mobile", "revenue": "2.00"}, {"status": "open", "channel": "web", "revenue": "3.00"}}},
		{Columns: []dataquery.Column{{Name: "status"}, {Name: "revenue"}}, Rows: []dataquery.Row{{"status": "paid", "revenue": "99.00"}, {"status": "open", "revenue": "4.00"}}},
		{Columns: []dataquery.Column{{Name: "channel"}, {Name: "revenue"}}, Rows: []dataquery.Row{{"channel": "web", "revenue": "88.00"}, {"channel": "mobile", "revenue": "15.00"}}},
		{Columns: []dataquery.Column{{Name: "revenue"}}, Rows: []dataquery.Row{{"revenue": "103.00"}}},
	}}
	command, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{RequestSeq: 12, Spec: spec}, fields, model)
	if result.Error != nil {
		t.Fatalf("pivot result error = %v", *result.Error)
	}
	if len(executor.queries) != 4 {
		t.Fatalf("query count = %d, want main plus row/column/grand totals", len(executor.queries))
	}
	for index, query := range executor.queries {
		if query.Target != "orders" || query.ProjectID != "project:test" || query.Surface != dataquery.SurfaceDataExplorer || query.Operation != dataquery.OperationSemanticExplore || query.ObjectID != "semantic-model:sales:orders" {
			t.Fatalf("query %d governance metadata/target = %#v", index, query)
		}
		if query.Time.Field != "" || query.Time.Grain != "" {
			t.Fatalf("query %d time grouping = %#v, want pivot axes only", index, query.Time)
		}
		if len(query.Filters) != 2 || len(query.Filters) != len(executor.queries[0].Filters) {
			t.Fatalf("query %d filters = %#v, want same two time-range predicates", index, query.Filters)
		}
	}
	if len(executor.queries[0].Fields) != 2 || len(executor.queries[1].Fields) != 1 || len(executor.queries[2].Fields) != 1 || len(executor.queries[3].Fields) != 0 {
		t.Fatalf("total query dimensions = [%d %d %d %d], want [2 1 1 0]", len(executor.queries[0].Fields), len(executor.queries[1].Fields), len(executor.queries[2].Fields), len(executor.queries[3].Fields))
	}
	if len(executor.queries[0].Sort) != 1 || executor.queries[0].Sort[0].Field != "orders.status" || len(executor.queries[1].Sort) != 0 || len(executor.queries[2].Sort) != 0 || len(executor.queries[3].Sort) != 0 {
		t.Fatalf("pivot sorts = %#v %#v %#v %#v, want only pivot sort on main query", executor.queries[0].Sort, executor.queries[1].Sort, executor.queries[2].Sort, executor.queries[3].Sort)
	}
	if result.DurationMS != 0 {
		t.Fatalf("duration = %d, want zero fixture duration", result.DurationMS)
	}
	if result.PivotTotals == nil || result.PivotTotals.Status != "complete" || len(result.PivotTotals.Rows) != 2 || result.PivotTotals.Rows[0].Values["revenue"] != "99.00" {
		t.Fatalf("pivot totals = %#v, want exact independent row total 99.00", result.PivotTotals)
	}
	projection := ProjectDataExplorerViews(command.Spec, result, fields)
	if projection.RecommendedView != dataExplorerPivotViewID {
		t.Fatalf("recommended = %q, warnings=%#v, want pivot with exact totals", projection.RecommendedView, projection.Warnings)
	}
	envelope := projection.Views[dataExplorerPivotViewID]
	state := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if len(state.Schema.Fields) != 5 || len(state.Blocks) != 1 || len(state.Blocks["a"].Rows) != 3 {
		t.Fatalf("pivot exact-total shape = schema=%d blocks=%d rows=%d, want 5/1/3", len(state.Schema.Fields), len(state.Blocks), len(state.Blocks["a"].Rows))
	}
	if state.ChunkSize < state.AvailableRows {
		t.Fatalf("pivot window chunk size = %d, availableRows=%d, want chunkSize >= availableRows", state.ChunkSize, state.AvailableRows)
	}
	rowTotalColumn := ""
	for _, field := range state.Schema.Fields {
		if strings.HasPrefix(field.ID, "pivot_total_") {
			rowTotalColumn = field.ID
		}
	}
	if rowTotalColumn == "" {
		t.Fatalf("pivot schema = %#v, want exact row total column", state.Schema.Fields)
	}
	rowTotalIndex := -1
	for index, column := range state.Schema.Fields {
		if column.ID == rowTotalColumn {
			rowTotalIndex = index
		}
	}
	if rowTotalIndex < 0 || state.Blocks["a"].Rows[0][rowTotalIndex] != "99.00" {
		t.Fatalf("pivot row total value = %#v, want governed 99.00", state.Blocks["a"].Rows[0])
	}
	pivotBase, err := envelope.Spec.Base()
	if err != nil || len(pivotBase.Interactions) != 0 {
		t.Fatalf("pivot total interactions = %#v err=%v, want disabled for synthetic total row", pivotBase.Interactions, err)
	}

	truncatedSpec := spec
	truncatedSpec.Limit = 2
	truncatedRows := append([]map[string]any(nil), result.Rows...)
	for index := 0; index < 8; index++ {
		truncatedRows = append(truncatedRows, map[string]any{"status": "extra", "channel": string(rune('a' + index)), "revenue": "1.00"})
	}
	truncatedExecutor := &recordingPivotTotalsExecutor{results: []dataquery.Result{{Columns: []dataquery.Column{{Name: "status"}, {Name: "channel"}, {Name: "revenue"}}, Rows: rowsToDataQueryRows(truncatedRows)}}}
	_, truncatedResult := dataExplorerSemanticResult(t.Context(), truncatedExecutor, "project:test", projectsignals.DataExploreCommand{RequestSeq: 13, Spec: truncatedSpec}, fields, model)
	if !truncatedResult.Truncated || truncatedResult.PivotTotals != nil || len(truncatedExecutor.queries) != 1 {
		t.Fatalf("truncated main result = truncated=%t totals=%#v queries=%d, want no totals work", truncatedResult.Truncated, truncatedResult.PivotTotals, len(truncatedExecutor.queries))
	}

	windowSpec := spec
	windowPivot := *spec.Pivot
	windowPivot.Window = &exploration.ExplorationPivotWindow{Limit: 2}
	windowSpec.Pivot = &windowPivot
	windowExecutor := &recordingPivotTotalsExecutor{results: []dataquery.Result{{Columns: []dataquery.Column{{Name: "status"}, {Name: "channel"}, {Name: "revenue"}}, Rows: rowsToDataQueryRows(result.Rows)}}}
	windowCommand, windowResult := dataExplorerSemanticResult(t.Context(), windowExecutor, "project:test", projectsignals.DataExploreCommand{RequestSeq: 14, Spec: windowSpec}, fields, model)
	if windowResult.Error != nil || !windowResult.Truncated || len(windowResult.Rows) != 2 || len(windowExecutor.queries) != 1 || windowExecutor.queries[0].Limit != 3 {
		t.Fatalf("pivot window result = error=%v truncated=%t rows=%d queryLimit=%d queries=%d, want bounded main query (3) and no totals", windowResult.Error, windowResult.Truncated, len(windowResult.Rows), windowExecutor.queries[0].Limit, len(windowExecutor.queries))
	}
	windowProjection := ProjectDataExplorerViews(windowCommand.Spec, windowResult, fields)
	windowTable, ok := windowProjection.Views[dataExplorerTableViewID]
	if !ok {
		t.Fatalf("pivot window projection = %#v, want safe table fallback", windowProjection)
	}
	windowBase, err := windowTable.Spec.Base()
	if err != nil || windowBase.DataBudget.MaxRows != 2 {
		t.Fatalf("pivot window visualization budget = base=%#v err=%v, want maxRows=2", windowBase, err)
	}
}

func rowsToDataQueryRows(rows []map[string]any) []dataquery.Row {
	result := make([]dataquery.Row, 0, len(rows))
	for _, row := range rows {
		result = append(result, dataquery.Row(row))
	}
	return result
}

func TestDataExplorerPivotTotalsFailuresFallBackWithoutSynthesis(t *testing.T) {
	pivot := &exploration.ExplorationPivotConfig{Rows: []exploration.ExplorationDimensionRef{{Field: "status"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Totals: &exploration.ExplorationPivotTotals{Rows: projectsignals.Pointer(true)}}
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10, Pivot: pivot}
	fields := []projectsignals.DataExploreFieldSignal{explorerProjectionField("status", "dimension", "Status", "string"), explorerProjectionField("channel", "dimension", "Channel", "string"), explorerProjectionField("revenue", "metric", "Revenue", "decimal")}
	result := explorerProjectionResult([]projectsignals.DataPreviewColumnSignal{explorerProjectionColumn("status", "Status", "string"), explorerProjectionColumn("channel", "Channel", "string"), explorerProjectionColumn("revenue", "Revenue", "decimal")}, []map[string]any{{"status": "paid", "channel": "web", "revenue": "1.00"}}, 1)
	for _, payload := range []*projectsignals.DataExplorePivotTotalsSignal{
		{Status: "incomplete", Rows: []projectsignals.DataExplorePivotTotalSignal{}, Columns: []projectsignals.DataExplorePivotTotalSignal{}, Grand: []projectsignals.DataExplorePivotTotalSignal{}, Warnings: []string{"truncated"}},
		{Status: "complete", Rows: []projectsignals.DataExplorePivotTotalSignal{{Key: map[string]any{"status": "paid"}, Values: map[string]any{}}}, Columns: []projectsignals.DataExplorePivotTotalSignal{}, Grand: []projectsignals.DataExplorePivotTotalSignal{}, Warnings: []string{}},
	} {
		result.PivotTotals = payload
		projection := ProjectDataExplorerViews(spec, result, fields)
		if projection.RecommendedView != dataExplorerTableViewID || !strings.Contains(strings.Join(projection.Warnings, " "), "exact governed") {
			t.Fatalf("payload %#v projected as %#v, want table fallback", payload, projection)
		}
	}
}

func TestDataExplorerPivotTotalsQueryFailureAndDisabledAvoidExtraWork(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10, Pivot: &exploration.ExplorationPivotConfig{Rows: []exploration.ExplorationDimensionRef{{Field: "status"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Totals: &exploration.ExplorationPivotTotals{Rows: projectsignals.Pointer(true)}}}
	baseQuery := dataquery.Query{ProjectID: "project:test", Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationSemanticExplore, Target: "orders", ObjectID: "semantic:sales:orders", Limit: 11, Time: dataquery.Time{Field: "created_at", Grain: "day"}, Filters: []dataquery.Filter{{Field: "status", Operator: "equals", Values: []any{"paid"}}}}
	executor := &recordingPivotTotalsExecutor{results: []dataquery.Result{{}}, errs: []error{errors.New("permission denied")}}
	payload, _, warnings := dataExplorerExecutePivotTotals(t.Context(), executor, spec, baseQuery)
	if len(executor.queries) != 1 || payload == nil || payload.Status != "error" || len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "permission denied") {
		t.Fatalf("failed total query = payload=%#v warnings=%#v queries=%d, want error payload and one query", payload, warnings, len(executor.queries))
	}
	if got := executor.queries[0].Target; got != "orders" {
		t.Fatalf("failed total query target = %q, want governed target", got)
	}

	disabled := spec
	disabled.Pivot = &exploration.ExplorationPivotConfig{Rows: spec.Pivot.Rows, Columns: spec.Pivot.Columns, Metrics: spec.Pivot.Metrics}
	noTotalsExecutor := &recordingPivotTotalsExecutor{}
	payload, _, warnings = dataExplorerExecutePivotTotals(t.Context(), noTotalsExecutor, disabled, baseQuery)
	if payload != nil || len(warnings) != 0 || len(noTotalsExecutor.queries) != 0 {
		t.Fatalf("disabled totals = payload=%#v warnings=%#v queries=%d, want no payload/work", payload, warnings, len(noTotalsExecutor.queries))
	}

	severitySpec := spec
	severitySpec.Pivot = &exploration.ExplorationPivotConfig{Rows: spec.Pivot.Rows, Columns: spec.Pivot.Columns, Metrics: spec.Pivot.Metrics, Totals: &exploration.ExplorationPivotTotals{Rows: projectsignals.Pointer(true), Columns: projectsignals.Pointer(true)}}
	incompleteRows := make([]dataquery.Row, baseQuery.Limit)
	for index := range incompleteRows {
		incompleteRows[index] = dataquery.Row{"channel": string(rune('a' + index)), "revenue": "1.00"}
	}
	severityExecutor := &recordingPivotTotalsExecutor{results: []dataquery.Result{{}, {Rows: incompleteRows}}, errs: []error{errors.New("totals unavailable"), nil}}
	payload, _, _ = dataExplorerExecutePivotTotals(t.Context(), severityExecutor, severitySpec, baseQuery)
	if payload == nil || payload.Status != "error" {
		t.Fatalf("error after incomplete/complete total = %#v, want error severity preserved", payload)
	}
}

func TestDataExplorerServingHeadersCannotSetFreshnessProvenance(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	datasetID := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	explore := testExplorationCommand(spec)
	command := projectsignals.DataExplorerCommand{Action: projectsignals.Optional("run"), Mode: projectsignals.Optional("explore"), RequestSeq: 5, Explore: &explore}
	request := httptest.NewRequest(http.MethodPost, "/explore/command", nil)
	request.Header.Set("X-Serving-Snapshot", "spoofed-snapshot")
	request.Header.Set("X-Serving-State", "spoofed-state")
	_, explorer, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, command)
	if !ok {
		t.Fatal("exploration command failed")
	}
	if freshness := explorer.Explore.Result.Freshness; freshness == nil || freshness.Status != "unknown" || projectsignals.ValueOrZero(freshness.Source) != "unknown" {
		t.Fatalf("freshness = %#v, want server-owned unknown provenance", freshness)
	}
}
