package explorationadapter

import (
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestConvertCartesianPreservesSelectionsAndScopesFilters(t *testing.T) {
	statusAlias := "status_label"
	metricAlias := "gross_revenue"
	timeAlias := "month"
	lower := "2026-01-01"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []exploration.ExplorationDimensionRef{{Field: "status", Alias: &statusAlias}},
		Metrics:       []exploration.ExplorationMetricRef{{Field: "revenue", Alias: &metricAlias}},
		Filters: []exploration.ExplorationFilter{{
			Field: "status",
			Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{
				Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}},
			}},
		}},
		Time: &exploration.ExplorationTimeSelection{
			Field: "created_at", Grain: exploration.ExplorationTimeGrainMonth, Alias: &timeAlias,
			Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{Kind: "date", Value: lower}}, Inclusive: true}}}},
		Sort:          []exploration.ExplorationSort{{Field: "revenue", Direction: exploration.ExplorationSortDirectionDesc}},
		Limit:         250,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{ExplorationVisualizationConfigBase: exploration.ExplorationVisualizationConfigBase{Title: stringPointer("Monthly revenue")}, Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine}},
	}
	result, err := Convert(spec, Options{VisualID: "component_1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Visual.Type != document.DashboardVisualTypeLine || result.Visual.Title == nil || *result.Visual.Title != "Monthly revenue" {
		t.Fatalf("visual identity = %#v", result.Visual)
	}
	aggregate, ok := result.Visual.Query.Value.(*document.AggregateDashboardQuery)
	if !ok {
		t.Fatalf("query = %T, want aggregate", result.Visual.Query.Value)
	}
	if len(aggregate.Dimensions) != 2 || dimensionOutput(aggregate.Dimensions[0]) != statusAlias || dimensionOutput(aggregate.Dimensions[1]) != timeAlias {
		t.Fatalf("dimensions = %#v, want status and time aliases", aggregate.Dimensions)
	}
	if len(aggregate.Metrics) != 1 || metricOutput(aggregate.Metrics[0]) != metricAlias || aggregate.Limit == nil || *aggregate.Limit != 250 {
		t.Fatalf("metrics/limit = %#v/%v", aggregate.Metrics, aggregate.Limit)
	}
	if aggregate.Sort == nil || len(*aggregate.Sort) != 1 || (*aggregate.Sort)[0].Field != metricAlias || (*aggregate.Sort)[0].Direction != document.DashboardSortDirectionDesc {
		t.Fatalf("sort = %#v, want alias descending", aggregate.Sort)
	}
	if len(result.Filters) != 2 {
		t.Fatalf("filters = %#v, want field and time filters", result.Filters)
	}
	for _, filter := range result.Filters {
		if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != "component_1" {
			t.Fatalf("filter %q targets = %#v, want component scope", filter.ID, filter.Targets)
		}
	}
	if got, _ := result.Filters[0].Default.Type(); got != "set" {
		t.Fatalf("set filter expression type = %q", got)
	}
	if got, _ := result.Filters[1].Default.Type(); got != "range" {
		t.Fatalf("time expression type = %q", got)
	}
}

func TestConvertPivotPreservesPivotSemantics(t *testing.T) {
	rowAlias := "region_label"
	metricAlias := "amount"
	offset := int32(4)
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Limit:         100,
		Pivot: &exploration.ExplorationPivotConfig{
			Rows:    []exploration.ExplorationDimensionRef{{Field: "region", Alias: &rowAlias}},
			Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}},
			Metrics: []exploration.ExplorationMetricRef{{Field: "revenue", Alias: &metricAlias}},
			Sort:    &[]exploration.ExplorationSort{{Field: "region", Direction: exploration.ExplorationSortDirectionAsc}},
			Totals:  &exploration.ExplorationPivotTotals{Grand: boolPointer(true)},
			Window:  &exploration.ExplorationPivotWindow{Offset: &offset, Limit: 40},
		},
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.PivotExplorationVisualization{Kind: "pivot", Rows: []exploration.ExplorationVisualizationFieldRef{{Field: "region"}}, Columns: []exploration.ExplorationVisualizationFieldRef{{Field: "channel"}}, Metrics: []exploration.ExplorationVisualizationFieldRef{{Field: "revenue"}}}},
	}
	result, err := Convert(spec, Options{VisualID: "component_2"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Visual.Type != document.DashboardVisualTypePivot {
		t.Fatalf("visual type = %q, want pivot", result.Visual.Type)
	}
	pivot, ok := result.Visual.Query.Value.(*document.PivotDashboardQuery)
	if !ok {
		t.Fatalf("query = %T, want pivot", result.Visual.Query.Value)
	}
	if len(pivot.Rows) != 1 || dimensionOutput(pivot.Rows[0]) != rowAlias || len(pivot.Columns) != 1 || dimensionOutput(pivot.Columns[0]) != "channel" || metricOutput(pivot.Metrics[0]) != metricAlias {
		t.Fatalf("pivot selections = %#v", pivot)
	}
	if pivot.Sort == nil || (*pivot.Sort)[0].Field != rowAlias || (*pivot.Sort)[0].Direction != document.DashboardSortDirectionAsc {
		t.Fatalf("pivot sort = %#v", pivot.Sort)
	}
	if pivot.Totals == nil || pivot.Totals.Grand == nil || !*pivot.Totals.Grand || pivot.Window == nil || pivot.Window.Offset == nil || *pivot.Window.Offset != offset || pivot.Window.Limit != 40 {
		t.Fatalf("pivot window/totals = %#v", pivot)
	}
}

func TestConvertRejectsUnboundPhysicalFieldsAndUnrepresentableFormats(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.region"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 10}
	if _, err := Convert(spec, Options{}); err == nil || !strings.Contains(err.Error(), "semantic dimension binding") {
		t.Fatalf("unbound physical field error = %v", err)
	}
	if _, err := Convert(spec, Options{Bindings: map[string]string{"orders.region": "region", "revenue": "revenue"}}); err == nil || !strings.Contains(err.Error(), "root lineage") {
		t.Fatalf("missing metric root evidence error = %v", err)
	}
	result, err := Convert(spec, Options{Bindings: map[string]string{"orders.region": "region", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}})
	if err != nil || result.Visual.Query.Value == nil {
		t.Fatalf("bound physical field conversion = %#v (%v)", result, err)
	}
	format := exploration.ExplorationVisualizationFormat{Value: &exploration.ExplorationNumberVisualizationFormat{Kind: "number"}}
	ref := exploration.ExplorationVisualizationFieldRef{Field: "region", Format: &format}
	spec.DatasetID = nil
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "region"}}
	spec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkBar, X: &ref}}
	if _, err := Convert(spec, Options{}); err == nil || !strings.Contains(err.Error(), "format cannot be represented") {
		t.Fatalf("format error = %v", err)
	}
}

func TestConvertPivotAppliesTimeToExistingRowAndDateRangeFilter(t *testing.T) {
	rowAlias := "month"
	lower := "2026-01-01"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Limit:         100,
		Pivot: &exploration.ExplorationPivotConfig{
			Rows:    []exploration.ExplorationDimensionRef{{Field: "created_at", Alias: stringPointer("created_at_raw")}},
			Columns: []exploration.ExplorationDimensionRef{{Field: "channel"}},
			Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		},
		Time: &exploration.ExplorationTimeSelection{
			Field: "created_at", Grain: exploration.ExplorationTimeGrainMonth, Alias: &rowAlias,
			Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{
				Kind:  "absolute",
				Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{Kind: "date", Value: lower}}, Inclusive: true},
			}},
		},
	}
	result, err := Convert(spec, Options{VisualID: "component_time"})
	if err != nil {
		t.Fatal(err)
	}
	pivot, ok := result.Visual.Query.Value.(*document.PivotDashboardQuery)
	if !ok || len(pivot.Rows) != 1 || pivot.Rows[0].Reference == nil || pivot.Rows[0].Reference.Alias == nil || *pivot.Rows[0].Reference.Alias != rowAlias || pivot.Rows[0].Reference.Grain == nil || *pivot.Rows[0].Reference.Grain != document.DashboardTimeGrainMonth {
		t.Fatalf("pivot time selection = %#v, want existing row decorated with month grain/alias", result.Visual.Query.Value)
	}
	if len(result.Filters) != 1 || result.Filters[0].ID != "exploration_filter_component_time_1" {
		t.Fatalf("scoped time filter = %#v", result.Filters)
	}
	if _, ok := result.Filters[0].Control.Value.(*document.DateRangeDashboardFilterControl); !ok {
		t.Fatalf("time filter control = %T, want date range", result.Filters[0].Control.Value)
	}
}

func TestConvertPivotDoesNotDuplicateTimeAlreadyInColumns(t *testing.T) {
	monthAlias := "purchase_month"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", Limit: 100,
		Pivot: &exploration.ExplorationPivotConfig{
			Rows: []exploration.ExplorationDimensionRef{{Field: "region"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "purchaseDate"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		},
		Time: &exploration.ExplorationTimeSelection{Field: "purchaseDate", Grain: exploration.ExplorationTimeGrainMonth, Alias: &monthAlias},
	}
	result, err := Convert(spec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pivot, ok := result.Visual.Query.Value.(*document.PivotDashboardQuery)
	if !ok || len(pivot.Rows) != 1 || len(pivot.Columns) != 1 || dimensionOutput(pivot.Columns[0]) != monthAlias || pivot.Columns[0].Reference == nil || pivot.Columns[0].Reference.Grain == nil || *pivot.Columns[0].Reference.Grain != document.DashboardTimeGrainMonth {
		t.Fatalf("pivot time column = %#v, want one decorated column and no duplicate row", result.Visual.Query.Value)
	}
}

func TestConvertPreservesTableDisplayConfigForDefaultAndPivotTables(t *testing.T) {
	striped := true
	showHeader := false
	density := exploration.ExplorationTableDensityCompact
	table := &exploration.ExplorationTableDisplayConfig{Density: &density, Striped: &striped, ShowHeader: &showHeader}
	base := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales",
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "region"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Limit:      20, Table: table,
	}
	for name, spec := range map[string]exploration.ExplorationSpec{
		"default": base,
		"pivot": func() exploration.ExplorationSpec {
			pivot := base
			pivot.Dimensions = nil
			pivot.Metrics = nil
			pivot.Pivot = &exploration.ExplorationPivotConfig{
				Rows:    []exploration.ExplorationDimensionRef{{Field: "region"}},
				Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
			}
			return pivot
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Convert(spec, Options{})
			if err != nil {
				t.Fatal(err)
			}
			presentation, ok := result.Visual.Presentation.Value.(*document.TableDashboardPresentation)
			if !ok || presentation.RowHeight != 24 || !presentation.Striped || presentation.ShowHeader {
				t.Fatalf("table presentation = %#v, want compact striped no-header", result.Visual.Presentation.Value)
			}
		})
	}
}

func TestConvertCartesianUsesFirstMetricForXWithoutDimensions(t *testing.T) {
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", Limit: 20,
		Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{
			Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine,
			X: &exploration.ExplorationVisualizationFieldRef{Field: "revenue"},
		}},
	}
	if _, err := Convert(spec, Options{}); err != nil {
		t.Fatalf("metric-only Cartesian x should follow dashboard default: %v", err)
	}
}

func TestConvertTranslatesComparisonOperatorAndPreservesPointChannels(t *testing.T) {
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		Dimensions:    []exploration.ExplorationDimensionRef{{Field: "order_id"}, {Field: "status"}},
		Metrics:       []exploration.ExplorationMetricRef{{Field: "delivery_days"}, {Field: "revenue"}, {Field: "review_score"}},
		Filters: []exploration.ExplorationFilter{{Field: "revenue", Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
			Kind: "comparison", Operator: "greater_than", Value: exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{Kind: "decimal", Value: "10"}},
		}}}},
		Limit: 500,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.PointExplorationVisualization{
			Kind: "point", Mark: "point",
			X:        exploration.ExplorationVisualizationFieldRef{Field: "review_score"},
			Y:        exploration.ExplorationVisualizationFieldRef{Field: "revenue"},
			Size:     &exploration.ExplorationVisualizationFieldRef{Field: "delivery_days"},
			Color:    &exploration.ExplorationVisualizationFieldRef{Field: "status"},
			Identity: &[]exploration.ExplorationVisualizationFieldRef{{Field: "order_id"}},
		}},
	}
	result, err := Convert(spec, Options{VisualID: "component_point"})
	if err != nil {
		t.Fatal(err)
	}
	point, ok := result.Visual.Presentation.Value.(*document.PointDashboardPresentation)
	if !ok || point.X != "review_score" || point.Y != "revenue" || point.Size == nil || *point.Size != "delivery_days" || point.Color == nil || *point.Color != "status" || len(point.Identity) != 1 || point.Identity[0] != "order_id" {
		t.Fatalf("point channels = %#v", result.Visual.Presentation.Value)
	}
	if result.Filters[0].Default == nil {
		t.Fatal("comparison filter default is nil")
	}
	comparison, ok := result.Filters[0].Default.Value.(*document.ComparisonDashboardFilterExpression)
	if !ok || comparison.Operator != document.DashboardFilterOperatorGreaterThan {
		t.Fatalf("comparison operator = %#v, want greaterThan", result.Filters[0].Default.Value)
	}
}

func TestConvertMonthlyVisualCompilesAgainstSemanticModel(t *testing.T) {
	monthAlias := "purchase_month"
	stateAlias := "customer_state"
	metricAlias := "gross_revenue"
	lower := "2026-01-01T00:00:00Z"
	dataset := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		DatasetID:     &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{
			{Field: "orders.ordered_at", Alias: &monthAlias},
			{Field: "customers.state", Alias: &stateAlias},
		},
		Metrics: []exploration.ExplorationMetricRef{{Field: "revenue", Alias: &metricAlias}},
		Filters: []exploration.ExplorationFilter{{Field: "customers.state", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{
			Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}},
		}}}},
		Time: &exploration.ExplorationTimeSelection{
			Field: "orders.ordered_at", Grain: exploration.ExplorationTimeGrainMonth, Alias: &monthAlias,
			Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &exploration.ExplorationTimeBound{
				Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: lower}}, Inclusive: true,
			}}},
		},
		Sort:          []exploration.ExplorationSort{{Field: "purchase_month", Direction: exploration.ExplorationSortDirectionAsc}},
		Limit:         250,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine, Series: &exploration.ExplorationVisualizationFieldRef{Field: "customers.state"}}},
	}
	result, err := Convert(spec, Options{VisualID: "sales_visual", Bindings: map[string]string{"customers.state": "customerState", "orders.ordered_at": "purchaseDate", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range result.Filters {
		if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != "sales_visual" {
			t.Fatalf("converted filter %q targets = %#v, want only sales_visual (not unrelated other_visual)", filter.ID, filter.Targets)
		}
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":    {Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				"customer_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				"status":      {Type: "string", Datatype: semanticmodel.DataTypeString},
				"ordered_at":  {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
				"revenue":     {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			}},
			"customers": {ModelName: "customers", GrainEntity: "customer", Entities: map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger},
				"state":       {Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Datasets:      map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customerState": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "customers.state", Path: []string{"orders_customers"}}}},
			"purchaseDate":  {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}},
	}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales", Filters: result.Filters, Visuals: map[string]document.DashboardVisual{"sales_visual": result.Visual, "other_visual": result.Visual},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{
				{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "sales_visual"}},
				{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "other_card", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "other_visual"}},
			}}},
		},
	}
	if _, err := compiler.CompileDocument(doc, map[string]*semanticmodel.Model{"sales": model}); err != nil {
		t.Fatalf("converted dashboard failed canonical compilation: %v", err)
	}
}

func TestConvertRejectsChannelOrderAndConflictingDatasetQualifier(t *testing.T) {
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", Limit: 20,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "region"}, {Field: "status"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{
			Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine,
			X: &exploration.ExplorationVisualizationFieldRef{Field: "status"},
		}},
	}
	if _, err := Convert(spec, Options{}); err == nil || !strings.Contains(err.Error(), "first dimension") {
		t.Fatalf("out-of-order Cartesian channel error = %v", err)
	}
	dataset := "orders"
	physical := "customers.region"
	spec.DatasetID = &dataset
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: physical}}
	spec.Visualization = nil
	if _, err := Convert(spec, Options{Bindings: map[string]string{physical: "region", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}}); err != nil {
		t.Fatalf("verified joined dimension should be accepted: %v", err)
	}
	filterDataset := "customers"
	spec.Filters = []exploration.ExplorationFilter{{Field: "orders.status", DatasetID: &filterDataset, Expression: exploration.ExplorationFilterExpression{Value: &exploration.UnfilteredExplorationFilterExpression{Kind: "unfiltered"}}}}
	if _, err := Convert(spec, Options{VisualID: "visual", Bindings: map[string]string{physical: "region", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}}); err == nil || !strings.Contains(err.Error(), "conflicts with dataset") {
		t.Fatalf("conflicting filter dataset qualifier error = %v", err)
	}
}

func TestConvertScopesFilterIDsByComponentAndDetachesPointers(t *testing.T) {
	alias := "state_label"
	smooth := true
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", Limit: 20,
		Dimensions:    []exploration.ExplorationDimensionRef{{Field: "state", Alias: &alias}},
		Metrics:       []exploration.ExplorationMetricRef{{Field: "revenue"}},
		Filters:       []exploration.ExplorationFilter{{Field: "state", Expression: exploration.ExplorationFilterExpression{Value: &exploration.UnfilteredExplorationFilterExpression{Kind: "unfiltered"}}}},
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine, Smooth: &smooth}},
	}
	first, err := Convert(spec, Options{VisualID: "tile_a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Convert(spec, Options{VisualID: "tile_b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Filters[0].ID == second.Filters[0].ID || !strings.Contains(first.Filters[0].ID, "tile_a") || !strings.Contains(second.Filters[0].ID, "tile_b") {
		t.Fatalf("component-scoped filter IDs = %q/%q", first.Filters[0].ID, second.Filters[0].ID)
	}
	alias = "mutated"
	smooth = false
	aggregate := first.Visual.Query.Value.(*document.AggregateDashboardQuery)
	if dimensionOutput(aggregate.Dimensions[0]) != "state_label" || first.Visual.Presentation.Value.(*document.CartesianDashboardPresentation).Smooth == nil || !*first.Visual.Presentation.Value.(*document.CartesianDashboardPresentation).Smooth {
		t.Fatalf("converted output retained input aliases = %#v", first.Visual)
	}
}

func stringPointer(value string) *string { return &value }
func boolPointer(value bool) *bool       { return &value }
