package explorationadapter

import (
	"reflect"
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestFromDashboardDocumentMonthlyRevenueByCustomerState(t *testing.T) {
	month := "purchase_month"
	state := "customer_state"
	revenue := "gross_revenue"
	start := "2026-01-01"
	semantic := "semantic:sales"
	dataset := "orders"
	line := document.DashboardVisualTypeLine
	visual := document.DashboardVisual{
		Type:  document.DashboardVisualTypeLine,
		Title: stringPointer("Monthly revenue"),
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Dimensions: []document.DashboardDimensionSelection{
				{Reference: &document.DashboardDimensionReference{Dimension: state, Alias: &state}},
				{Reference: &document.DashboardDimensionReference{Dimension: month, Alias: &month, Grain: dashboardGrainPointer(document.DashboardTimeGrainMonth)}},
			},
			Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: revenue, Alias: &revenue}}},
			Sort:    &[]document.DashboardSort{{Field: revenue, Direction: document.DashboardSortDirectionDesc}},
			Limit:   int32Pointer(250),
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}},
	}
	filters := []document.DashboardFilter{
		{ID: "status", Dimension: state, Default: &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "set"}, Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "paid"}}}}}, Targets: stringSlicePointer([]string{"sales"})},
		{ID: "period", Dimension: month, Default: &document.DashboardFilterExpression{Value: &document.RangeDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "range"}, Type: "range", Lower: &document.DashboardFilterBound{Value: document.DashboardFilterValue{Value: &document.DateDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "date"}, Type: "date", Value: start}}, Inclusive: true}}}, Targets: stringSlicePointer([]string{"sales"})},
		{ID: "other", Dimension: state, Default: &document.DashboardFilterExpression{Value: &document.UnfilteredDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "unfiltered"}, Type: "unfiltered"}}, Targets: stringSlicePointer([]string{"other"})},
	}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{SemanticModel: semantic, Filters: filters, Visuals: map[string]document.DashboardVisual{"sales": visual}}}
	got, err := FromDashboardDocument(doc, "sales", ReverseOptions{DatasetID: &dataset, Bindings: map[string]string{
		state:   "customers.state",
		month:   "orders.purchase_date",
		revenue: "orders.revenue",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ModelID != semantic || got.DatasetID == nil || *got.DatasetID != dataset || got.Limit != 250 {
		t.Fatalf("identity/limit = %#v", got)
	}
	if len(got.Dimensions) != 2 || got.Dimensions[0].Field != "customers.state" || got.Dimensions[1].Field != "orders.purchase_date" || got.Dimensions[1].Grain == nil || *got.Dimensions[1].Grain != exploration.ExplorationTimeGrainMonth {
		t.Fatalf("dimensions = %#v", got.Dimensions)
	}
	if got.Time == nil || got.Time.Field != "orders.purchase_date" || got.Time.Range == nil {
		t.Fatalf("time = %#v", got.Time)
	}
	if len(got.Filters) != 1 || got.Filters[0].Field != "customers.state" {
		t.Fatalf("filters = %#v", got.Filters)
	}
	if len(got.Metrics) != 1 || got.Metrics[0].Field != "orders.revenue" || got.Metrics[0].Alias == nil || *got.Metrics[0].Alias != revenue {
		t.Fatalf("metrics = %#v", got.Metrics)
	}
	if len(got.Sort) != 1 || got.Sort[0].Field != revenue || got.Sort[0].Direction != exploration.ExplorationSortDirectionDesc {
		t.Fatalf("sort = %#v", got.Sort)
	}
	if got.Visualization == nil {
		t.Fatal("visualization is nil")
	}
	if cart, ok := got.Visualization.Value.(*exploration.CartesianExplorationVisualization); !ok || cart.Mark != exploration.ExplorationVisualizationCartesianMark(line) || cart.X.Field != state {
		t.Fatalf("visualization = %#v", got.Visualization.Value)
	}
}

func TestFromDashboardDocumentRejectsModelMismatch(t *testing.T) {
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeTable,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Metrics: []document.DashboardMetricSelection{{String: stringPointer("revenue")}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 24, ShowHeader: true}},
	}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{SemanticModel: "semantic:sales", Visuals: map[string]document.DashboardVisual{"sales": visual}}}
	if _, err := FromDashboardDocument(doc, "sales", ReverseOptions{ModelID: "semantic:marketing"}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("model mismatch error = %v", err)
	}
}

func TestFromDashboardDocumentIncludesExactComponentFilterTarget(t *testing.T) {
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeTable,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Metrics: []document.DashboardMetricSelection{{String: stringPointer("revenue")}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}},
	}
	target := stringSlicePointer([]string{"overview/sales-card"})
	otherTarget := stringSlicePointer([]string{"overview/other-card"})
	filter := document.DashboardFilter{ID: "region", Dimension: "region", Targets: target, Default: &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "set"}, Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "EMEA"}}}}}}
	other := filter
	other.ID = "other"
	other.Targets = otherTarget
	other.Default = &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "set"}, Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: "APAC"}}}}}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{SemanticModel: "semantic:sales", Filters: []document.DashboardFilter{filter, other}, Visuals: map[string]document.DashboardVisual{"sales": visual}}}
	dataset := "orders"
	got, err := FromDashboardDocument(doc, "sales", ReverseOptions{ModelID: "semantic:sales", DatasetID: &dataset, FilterTarget: "overview/sales-card", Bindings: map[string]string{"region": "orders.region", "revenue": "revenue"}, FilterDatasets: map[string]string{"region": "orders", "other": "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Filters) != 1 || got.Filters[0].Field != "orders.region" {
		t.Fatalf("component-scoped filters = %#v, want only sales-card filter", got.Filters)
	}
	value, ok := got.Filters[0].Expression.Value.(*exploration.SetExplorationFilterExpression)
	if !ok || value.Values[0].Value.(*exploration.StringExplorationFilterValue).Value != "EMEA" {
		t.Fatalf("component filter value = %#v, want EMEA", got.Filters[0].Expression.Value)
	}
}

func TestFromDashboardVisualPreservesPivotAndTypedFilters(t *testing.T) {
	region := "region_label"
	amount := "amount"
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypePivot,
		Query: document.DashboardQuery{Value: &document.PivotDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "pivot"}, Type: "pivot",
			Rows:    []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: region, Alias: &region}}},
			Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: amount, Alias: &amount}}},
			Sort:    &[]document.DashboardSort{{Field: region, Direction: document.DashboardSortDirectionAsc}},
			Totals:  &document.DashboardPivotTotals{Grand: boolPointer(true)},
			Window:  &document.DashboardPivotWindow{Limit: 40},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "table"}, Type: "table", RowHeight: 24, ShowHeader: true}},
	}
	visualFilter := document.DashboardFilter{ID: "amount", Dimension: amount, Default: &document.DashboardFilterExpression{Value: &document.ComparisonDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison", Operator: document.DashboardFilterOperatorGreaterThanOrEqual, Value: document.DashboardFilterValue{Value: &document.DecimalDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "decimal"}, Type: "decimal", Value: "10.25"}}}}}
	got, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales", Filters: []document.DashboardFilter{visualFilter}, Bindings: map[string]string{region: "customers.region", amount: "orders.revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pivot == nil || len(got.Pivot.Rows) != 1 || got.Pivot.Rows[0].Field != "customers.region" || got.Pivot.Totals == nil || got.Pivot.Window == nil || got.Pivot.Window.Limit != 40 {
		t.Fatalf("pivot = %#v", got.Pivot)
	}
	if got.Limit != 40 || len(got.Filters) != 1 || got.Filters[0].Field != "orders.revenue" {
		t.Fatalf("bounds/filter = %d/%#v", got.Limit, got.Filters)
	}
	comparison, ok := got.Filters[0].Expression.Value.(*exploration.ComparisonExplorationFilterExpression)
	if !ok || comparison.Value.Value.(*exploration.DecimalExplorationFilterValue).Value != "10.25" {
		t.Fatalf("comparison = %#v", got.Filters[0].Expression.Value)
	}
}

func TestFromDashboardRejectsUnsupportedQueryAndMissingExpression(t *testing.T) {
	visual := document.DashboardVisual{Type: document.DashboardVisualTypeTable, Query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "records"}, Type: "records"}}, Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table"}}}
	if _, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales"}); err == nil || !strings.Contains(err.Error(), "cannot be represented") {
		t.Fatalf("records query error = %v", err)
	}
	aggregate := document.DashboardVisual{Type: document.DashboardVisualTypeTable, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: stringPointer("revenue")}}}}, Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table"}}}
	if _, err := FromDashboardVisual(aggregate, ReverseOptions{ModelID: "semantic:sales", Filters: []document.DashboardFilter{{Dimension: "status"}}}); err == nil || !strings.Contains(err.Error(), "no default expression") {
		t.Fatalf("missing expression error = %v", err)
	}
}

func TestFromDashboardRejectsDuplicateCanonicalGrains(t *testing.T) {
	day := "day"
	month := "month"
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeTable,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Dimensions: []document.DashboardDimensionSelection{
				{Reference: &document.DashboardDimensionReference{Dimension: "orderDate", Alias: &day, Grain: dashboardGrainPointer(document.DashboardTimeGrainDay)}},
				{Reference: &document.DashboardDimensionReference{Dimension: "orderDate", Alias: &month, Grain: dashboardGrainPointer(document.DashboardTimeGrainMonth)}},
			},
			Metrics: []document.DashboardMetricSelection{{String: stringPointer("revenue")}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table"}},
	}
	if _, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales", Bindings: map[string]string{"orderDate": "orders.ordered_at"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate canonical grain error = %v", err)
	}
}

func TestFromDashboardPreservesTableOrderAndDisplay(t *testing.T) {
	region := "region"
	revenue := "gross_revenue"
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeTable,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: region, Alias: &region}}},
			Metrics:    []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: revenue, Alias: &revenue}}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 36, ShowHeader: false, Striped: true}},
	}
	got, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales", Bindings: map[string]string{region: "customers.region", revenue: "orders.revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Dimensions) != 1 || got.Dimensions[0].Field != "customers.region" || got.Dimensions[0].Alias == nil || *got.Dimensions[0].Alias != region || len(got.Metrics) != 1 || got.Metrics[0].Field != "orders.revenue" || got.Metrics[0].Alias == nil || *got.Metrics[0].Alias != revenue {
		t.Fatalf("selection order/aliases = %#v/%#v", got.Dimensions, got.Metrics)
	}
	if got.Table == nil || got.Table.RowHeight == nil || *got.Table.RowHeight != 36 || got.Table.ShowHeader == nil || *got.Table.ShowHeader || got.Table.Striped == nil || !*got.Table.Striped {
		t.Fatalf("table display = %#v", got.Table)
	}
}

func TestFromDashboardCartesianAliasesAndUnsupportedOptions(t *testing.T) {
	state := "customer_state"
	revenue := "gross_revenue"
	labels := &document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAlways}
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeLine,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
			Dimensions: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: state, Alias: &state}}},
			Metrics:    []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: revenue, Alias: &revenue}}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Labels: labels}},
	}
	if _, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales"}); err == nil || !strings.Contains(err.Error(), "renderer options") {
		t.Fatalf("unsupported Cartesian option error = %v", err)
	}
	visual.Presentation = document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}}
	got, err := FromDashboardVisual(visual, ReverseOptions{ModelID: "semantic:sales", Bindings: map[string]string{state: "customers.state", revenue: "orders.revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	cart, ok := got.Visualization.Value.(*exploration.CartesianExplorationVisualization)
	if !ok || cart.X == nil || cart.X.Field != state || cart.Y == nil || len(*cart.Y) != 1 || (*cart.Y)[0].Field != revenue {
		t.Fatalf("Cartesian output aliases = %#v", got.Visualization.Value)
	}
}

func TestFromDashboardDetachesInputPointers(t *testing.T) {
	model := "semantic:sales"
	dataset := "orders"
	alias := "gross_revenue"
	visual := document.DashboardVisual{
		Type:         document.DashboardVisualTypeTable,
		Title:        stringPointer("Original"),
		Query:        document.DashboardQuery{Value: &document.AggregateDashboardQuery{DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate", Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: &alias}}}}},
		Presentation: document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 24, ShowHeader: true}},
	}
	got, err := FromDashboardVisual(visual, ReverseOptions{ModelID: model, DatasetID: &dataset})
	if err != nil {
		t.Fatal(err)
	}
	*visual.Title = "Mutated"
	*visual.Query.Value.(*document.AggregateDashboardQuery).Metrics[0].Reference.Alias = "changed"
	dataset = "changed"
	visualization, _ := got.Visualization.Value.(*exploration.TableExplorationVisualization)
	title := ""
	if visualization != nil && visualization.Title != nil {
		title = *visualization.Title
	}
	if title == "Mutated" || got.DatasetID == nil || *got.DatasetID != "orders" || got.Metrics[0].Alias == nil || *got.Metrics[0].Alias != "gross_revenue" {
		t.Fatalf("reverse result shares input pointers: %#v", got)
	}
}

func TestForwardThenReverseMonthlyStateKeepsCanonicalSemantics(t *testing.T) {
	monthAlias := "purchase_month"
	stateAlias := "customer_state"
	metricAlias := "gross_revenue"
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
			Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2026-01-01T00:00:00Z"}}, Inclusive: true}}},
		},
		Sort:          []exploration.ExplorationSort{{Field: monthAlias, Direction: exploration.ExplorationSortDirectionAsc}},
		Limit:         250,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine, Series: &exploration.ExplorationVisualizationFieldRef{Field: "customers.state"}}},
	}
	forward, err := Convert(spec, Options{VisualID: "sales_visual", Bindings: map[string]string{"orders.ordered_at": "purchaseDate", "customers.state": "customerState", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{SemanticModel: spec.ModelID, Filters: forward.Filters, Visuals: map[string]document.DashboardVisual{"sales_visual": forward.Visual}}}
	reverse, err := FromDashboardDocument(doc, "sales_visual", ReverseOptions{ModelID: spec.ModelID, DatasetID: &dataset, Bindings: map[string]string{"purchaseDate": "orders.ordered_at", "customerState": "customers.state", "revenue": "revenue", "purchase_month": "orders.ordered_at", "customer_state": "customers.state", "gross_revenue": "revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	if reverse.ModelID != spec.ModelID || reverse.DatasetID == nil || *reverse.DatasetID != dataset || reverse.Limit != spec.Limit {
		t.Fatalf("identity = %#v", reverse)
	}
	if len(reverse.Dimensions) != 2 || reverse.Dimensions[0].Field != spec.Dimensions[0].Field || reverse.Dimensions[0].Alias == nil || *reverse.Dimensions[0].Alias != monthAlias || reverse.Dimensions[1].Field != spec.Dimensions[1].Field || reverse.Dimensions[1].Alias == nil || *reverse.Dimensions[1].Alias != stateAlias {
		t.Fatalf("dimensions = %#v", reverse.Dimensions)
	}
	if reverse.Time == nil || reverse.Time.Field != spec.Time.Field || reverse.Time.Grain != spec.Time.Grain || reverse.Time.Range == nil {
		t.Fatalf("time = %#v", reverse.Time)
	}
	if len(reverse.Filters) != 1 || reverse.Filters[0].Field != "customers.state" || len(reverse.Sort) != 1 || reverse.Sort[0].Field != monthAlias {
		t.Fatalf("filters/sort = %#v/%#v", reverse.Filters, reverse.Sort)
	}
	cart, ok := reverse.Visualization.Value.(*exploration.CartesianExplorationVisualization)
	if !ok || cart.Series == nil || cart.Series.Field != stateAlias || cart.Title == nil || *cart.Title != "Explore" {
		t.Fatalf("display semantics = %#v", reverse.Visualization.Value)
	}
}

func TestReverseMonthlyStateCompilesAfterForwardRoundTrip(t *testing.T) {
	monthAlias := "purchase_month"
	stateAlias := "customer_state"
	metricAlias := "gross_revenue"
	dataset := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.ordered_at", Alias: &monthAlias}, {Field: "customers.state", Alias: &stateAlias}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue", Alias: &metricAlias}},
		Filters:    []exploration.ExplorationFilter{{Field: "customers.state", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}}}}}},
		Time:       &exploration.ExplorationTimeSelection{Field: "orders.ordered_at", Grain: exploration.ExplorationTimeGrainMonth, Alias: &monthAlias, Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{Kind: "absolute", Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2026-01-01T00:00:00Z"}}, Inclusive: true}}}},
		Sort:       []exploration.ExplorationSort{{Field: monthAlias, Direction: exploration.ExplorationSortDirectionAsc}}, Limit: 250,
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.ExplorationVisualizationCartesianMarkLine, Series: &exploration.ExplorationVisualizationFieldRef{Field: "customers.state"}}},
	}
	forwardOptions := Options{VisualID: "sales_visual", Bindings: map[string]string{"orders.ordered_at": "purchaseDate", "customers.state": "customerState", "revenue": "revenue"}, MetricRoots: map[string]string{"revenue": "orders"}}
	forward, err := Convert(spec, forwardOptions)
	if err != nil {
		t.Fatal(err)
	}
	doc := document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"}, Spec: document.DashboardSpec{SemanticModel: spec.ModelID, Filters: forward.Filters, Visuals: map[string]document.DashboardVisual{"sales_visual": forward.Visual}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "sales_card", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: "sales_visual"}}}}}}}
	model := reverseFixtureSemanticModel()
	if _, err := compiler.CompileDocument(doc, map[string]*semanticmodel.Model{spec.ModelID: model}); err != nil {
		t.Fatalf("forward dashboard compilation: %v", err)
	}
	reverse, err := FromDashboardDocument(doc, "sales_visual", ReverseOptions{ModelID: spec.ModelID, DatasetID: &dataset, Bindings: map[string]string{"purchaseDate": "orders.ordered_at", "customerState": "customers.state", "revenue": "revenue", "purchase_month": "orders.ordered_at", "customer_state": "customers.state", "gross_revenue": "revenue"}})
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Time == nil || reverse.Time.Field != spec.Time.Field || reverse.Time.Grain != spec.Time.Grain || len(reverse.Filters) != 1 || len(reverse.Sort) != 1 || reverse.Sort[0].Field != monthAlias {
		t.Fatalf("reverse analytical channels = %#v", reverse)
	}
	forwardAgain, err := Convert(reverse, forwardOptions)
	if err != nil {
		t.Fatalf("reverse conversion: %v", err)
	}
	roundTrip := doc
	roundTrip.Spec.Filters = forwardAgain.Filters
	roundTrip.Spec.Visuals = map[string]document.DashboardVisual{"sales_visual": forwardAgain.Visual}
	if _, err := compiler.CompileDocument(roundTrip, map[string]*semanticmodel.Model{spec.ModelID: model}); err != nil {
		t.Fatalf("reverse dashboard compilation: %v", err)
	}
	originalQuery := forward.Visual.Query.Value.(*document.AggregateDashboardQuery)
	roundTripQuery := forwardAgain.Visual.Query.Value.(*document.AggregateDashboardQuery)
	if !reflect.DeepEqual(roundTripQuery, originalQuery) {
		t.Fatalf("round-trip query changed: %#v vs %#v", roundTripQuery, originalQuery)
	}
	if !reflect.DeepEqual(roundTrip.Spec.Filters, doc.Spec.Filters) {
		t.Fatalf("round-trip filters changed: %#v vs %#v", roundTrip.Spec.Filters, doc.Spec.Filters)
	}
}

func reverseFixtureSemanticModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger}, "customer_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger}, "status": {Type: "string", Datatype: semanticmodel.DataTypeString}, "ordered_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ}, "revenue": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			}},
			"customers": {ModelName: "customers", GrainEntity: "customer", Entities: map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString},
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
}

func dashboardGrainPointer(value document.DashboardTimeGrain) *document.DashboardTimeGrain {
	return &value
}
func int32Pointer(value int32) *int32             { return &value }
func stringSlicePointer(value []string) *[]string { return &value }
