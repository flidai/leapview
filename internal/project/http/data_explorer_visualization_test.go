package http

import (
	"testing"
	"time"

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

func TestProjectDataExplorerViewsProjectsSalesOrdersTemporalAndDecimalScalars(t *testing.T) {
	dataset := "sales_orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic-model:sales",
		DatasetID:     &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{
			{Field: "sales_orders.category"},
			{Field: "sales_customers.customer_id"},
			{Field: "sales_orders.order_id"},
			{Field: "sales_orders.purchase_date"},
			{Field: "sales_orders.purchase_month"},
			{Field: "sales_orders.purchase_timestamp"},
			{Field: "sales_orders.revenue"},
			{Field: "sales_orders.status"},
		},
		Metrics: []exploration.ExplorationMetricRef{},
		Time: &exploration.ExplorationTimeSelection{
			Field: "sales_orders.purchase_date",
			Grain: exploration.ExplorationTimeGrainMonth,
		},
		Sort:  []exploration.ExplorationSort{{Field: "sales_orders.purchase_date", Direction: exploration.ExplorationSortDirectionDesc}},
		Limit: 100,
	}
	fields := []projectsignals.DataExploreFieldSignal{
		{ID: "sales_orders.category", Kind: "dimension", Label: "Category", DatasetID: "sales_orders", Type: projectsignals.Optional("string"), Compatible: true},
		{ID: "sales_customers.customer_id", Kind: "dimension", Label: "Customer ID", DatasetID: "sales_customers", Type: projectsignals.Optional("string"), Compatible: true},
		{ID: "sales_orders.order_id", Kind: "dimension", Label: "Order ID", DatasetID: "sales_orders", Type: projectsignals.Optional("string"), Compatible: true},
		{ID: "sales_orders.purchase_date", Kind: "dimension", Label: "Purchase date", DatasetID: "sales_orders", Type: projectsignals.Optional("date"), Compatible: true},
		{ID: "sales_orders.purchase_month", Kind: "dimension", Label: "Purchase month", DatasetID: "sales_orders", Type: projectsignals.Optional("string"), Compatible: true},
		{ID: "sales_orders.purchase_timestamp", Kind: "dimension", Label: "Purchase timestamp", DatasetID: "sales_orders", Type: projectsignals.Optional("timestamp"), Compatible: true},
		{ID: "sales_orders.revenue", Kind: "dimension", Label: "Revenue", DatasetID: "sales_orders", Type: projectsignals.Optional("number"), Compatible: true},
		{ID: "sales_orders.status", Kind: "dimension", Label: "Status", DatasetID: "sales_orders", Type: projectsignals.Optional("string"), Compatible: true},
	}
	result := projectsignals.DataExploreResultSignal{
		RequestSeq: 18,
		Truncated:  true,
		Columns: []projectsignals.DataPreviewColumnSignal{
			{Key: "category", Label: "Category"},
			{Key: "customer_id", Label: "Customer ID"},
			{Key: "order_id", Label: "Order ID"},
			{Key: "purchase_date", Label: "Purchase date"},
			{Key: "purchase_month", Label: "Purchase month"},
			{Key: "purchase_timestamp", Label: "Purchase timestamp"},
			{Key: "revenue", Label: "Revenue"},
			{Key: "status", Label: "Status"},
		},
		Rows: []map[string]any{{
			"category": "uncategorized", "customer_id": "4c2ec60c29d10c34bd49cb88aa85cfc4", "order_id": "a2ac6dad85cf8af5b0afb510a240fe8c",
			"purchase_date": time.Date(2018, 10, 1, 0, 0, 0, 0, time.UTC), "purchase_month": "2018-10", "purchase_timestamp": time.Date(2018, 10, 3, 18, 55, 29, 0, time.UTC),
			"revenue": "197.55", "status": "canceled",
		}},
	}

	views, recommended, warnings := ProjectDataExplorerViews(spec, result, fields)
	if recommended != dataExplorerTableViewID {
		t.Fatalf("recommended view = %q, want %q", recommended, dataExplorerTableViewID)
	}
	table, ok := views[dataExplorerTableViewID]
	if !ok {
		t.Fatalf("table view missing for the default Sales Orders query: warnings=%#v", warnings)
	}
	if err := visualizationir.ValidateEnvelope(table); err != nil {
		t.Fatalf("table view did not validate: %v", err)
	}
	state, ok := table.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok {
		t.Fatalf("table state = %T, want windowed state", table.DataState.Value)
	}
	block, ok := state.Blocks["a"]
	if !ok || len(block.Rows) != 1 {
		t.Fatalf("table state block = %#v, want one projected row", state.Blocks)
	}
	for index, field := range state.Schema.Fields {
		switch field.ID {
		case "purchase_date":
			if got, want := block.Rows[0][index], "2018-10-01T00:00:00Z"; got != want {
				t.Errorf("purchase_date projection = %#v, want RFC3339Nano string %q", got, want)
			}
		case "purchase_timestamp":
			if got, want := block.Rows[0][index], "2018-10-03T18:55:29Z"; got != want {
				t.Errorf("purchase_timestamp projection = %#v, want RFC3339Nano string %q", got, want)
			}
		}
	}
	for _, field := range state.Schema.Fields {
		if field.ID == "revenue" {
			if field.DataType != visualizationir.VisualizationDataTypeDecimal {
				t.Fatalf("revenue data type = %q, want decimal for exact decimal-string results", field.DataType)
			}
			return
		}
	}
	t.Fatal("table schema has no revenue field")
}

func TestProjectDataExplorerViewsUsesObservedGenericNumberTransport(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    any
		wantType visualizationir.VisualizationDataType
	}{
		{name: "decimal string transport", value: "42.125", wantType: visualizationir.VisualizationDataTypeDecimal},
		{name: "float transport", value: float64(42.125), wantType: visualizationir.VisualizationDataTypeFloat},
		{name: "integer transport", value: int64(42), wantType: visualizationir.VisualizationDataTypeInteger},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := explorerVisualizationTestSpec()
			spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.amount"}}
			spec.Metrics = []exploration.ExplorationMetricRef{}
			fields := []projectsignals.DataExploreFieldSignal{{
				ID: "orders.amount", Kind: "dimension", Label: "Amount", DatasetID: "orders", Type: projectsignals.Optional("number"), Compatible: true,
			}}
			result := projectsignals.DataExploreResultSignal{
				RequestSeq: 19,
				Columns:    []projectsignals.DataPreviewColumnSignal{{Key: "amount", Label: "Amount"}},
				Rows:       []map[string]any{{"amount": test.value}},
			}
			views, _, warnings := ProjectDataExplorerViews(spec, result, fields)
			table, ok := views[dataExplorerTableViewID]
			if !ok {
				t.Fatalf("table view missing: warnings=%#v", warnings)
			}
			state, ok := table.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
			if !ok {
				t.Fatalf("table state = %T, want windowed state", table.DataState.Value)
			}
			if got := state.Schema.Fields[0].DataType; got != test.wantType {
				t.Fatalf("amount data type = %q, want %q", got, test.wantType)
			}
		})
	}

	spec := explorerVisualizationTestSpec()
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.amount"}}
	spec.Metrics = []exploration.ExplorationMetricRef{}
	fields := []projectsignals.DataExploreFieldSignal{{
		ID: "orders.amount", Kind: "dimension", Label: "Amount", DatasetID: "orders", Type: projectsignals.Optional("number"), Compatible: true,
	}}
	invalidDecimal := projectsignals.DataExploreResultSignal{
		RequestSeq: 20,
		Columns:    []projectsignals.DataPreviewColumnSignal{{Key: "amount", Label: "Amount"}},
		Rows:       []map[string]any{{"amount": "1e2"}},
	}
	views, _, warnings := ProjectDataExplorerViews(spec, invalidDecimal, fields)
	if _, ok := views[dataExplorerTableViewID]; ok || len(warnings) == 0 {
		t.Fatalf("non-canonical generic number transport should fail shared validation: views=%#v warnings=%#v", views, warnings)
	}
}

func TestProjectDataExplorerViewsBoundsRowsAndMarksTruncation(t *testing.T) {
	spec := explorerVisualizationTestSpec()
	spec.Limit = 1
	result := explorerVisualizationTestResult()
	result.RequestSeq = 12
	result.Rows = append(result.Rows, map[string]any{"status": "returned", "revenue": float64(8)})
	views, _, warnings := ProjectDataExplorerViews(spec, result, explorerVisualizationTestFields())
	if len(warnings) != 0 {
		t.Fatalf("bounded frame should use completeness metadata without a redundant warning: %v", warnings)
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
