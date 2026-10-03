package explorehandoff

import (
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
)

func TestSpecForVisualPreservesStandardAggregateQuery(t *testing.T) {
	series := visualizationdefinition.FieldBinding{FieldID: "orders.channel", Alias: "channel"}
	definition := visualizationdefinition.Definition{
		ID: "revenue",
		Query: visualizationdefinition.QueryBinding{
			Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales", DatasetID: "primary",
			Aggregate: &visualizationdefinition.AggregateQueryBinding{
				TableID:    "orders",
				Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "orders.region", Alias: "region"}, {FieldID: "orders.purchase_month", Alias: "purchase_month", Grain: "month"}},
				Series:     &series,
				Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}},
				Time:       &visualizationdefinition.TimeBinding{FieldID: "ordered_at", Alias: "ordered_at", Grain: "month"},
				Sort:       []visualizationdefinition.Sort{{FieldID: "revenue", Direction: "desc"}},
				Limit:      80,
			},
		},
	}

	spec, ok := SpecForVisual(definition, exploreHandoffModel())
	if !ok {
		t.Fatal("SpecForVisual() reported a representable aggregate as unsupported")
	}
	if spec.ModelID != "semantic:sales" || spec.DatasetID == nil || *spec.DatasetID != "orders" || spec.Limit != 80 {
		t.Fatalf("identity/limit = (%q, %v, %d)", spec.ModelID, spec.DatasetID, spec.Limit)
	}
	if len(spec.Dimensions) != 3 || spec.Dimensions[0].Field != "orders.region" || spec.Dimensions[1].Field != "orders.purchase_month" || spec.Dimensions[2].Field != "orders.channel" {
		t.Fatalf("dimensions = %#v", spec.Dimensions)
	}
	if spec.Dimensions[1].Grain == nil || *spec.Dimensions[1].Grain != exploration.ExplorationTimeGrainMonth {
		t.Fatalf("dimension grain = %#v, want month", spec.Dimensions[1].Grain)
	}
	if len(spec.Metrics) != 1 || spec.Metrics[0].Field != "revenue" {
		t.Fatalf("metrics = %#v", spec.Metrics)
	}
	if spec.Time == nil || spec.Time.Field != "orders.ordered_at" || spec.Time.Grain != exploration.ExplorationTimeGrainMonth {
		t.Fatalf("time selection = %#v", spec.Time)
	}
	if len(spec.Sort) != 1 || spec.Sort[0].Field != "revenue" || spec.Sort[0].Direction != exploration.ExplorationSortDirectionDesc {
		t.Fatalf("sort = %#v", spec.Sort)
	}
	if len(spec.Filters) != 0 {
		t.Fatalf("filters = %#v, want empty", spec.Filters)
	}
}

func TestSpecForVisualFailsClosedForUnsupportedQueries(t *testing.T) {
	base := visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}, Limit: 100},
	}}
	tests := map[string]func(*visualizationdefinition.Definition){
		"detail query": func(value *visualizationdefinition.Definition) {
			value.Query.Kind = visualizationdefinition.QueryDetail
			value.Query.Aggregate = nil
			value.Query.Detail = &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "orders.id", Alias: "id"}}, Limit: 100}
		},
		"histogram": func(value *visualizationdefinition.Definition) {
			value.Query.Aggregate.Histogram = &visualizationdefinition.HistogramQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: "orders.revenue", Alias: "revenue"}, Bins: 10}
		},
		"secondary query": func(value *visualizationdefinition.Definition) {
			value.SecondaryQueries = map[string]visualizationdefinition.QueryBinding{"context": {Kind: visualizationdefinition.QueryAggregate}}
		},
		"over-limit query": func(value *visualizationdefinition.Definition) {
			value.Query.Aggregate.Limit = 1001
		},
		"missing semantic dataset": func(value *visualizationdefinition.Definition) {
			value.Query.Aggregate.TableID = ""
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			value.Query.Aggregate = &*base.Query.Aggregate
			mutate(&value)
			if _, ok := SpecForVisual(value, exploreHandoffModel()); ok {
				t.Fatal("SpecForVisual() accepted an unsupported query")
			}
		})
	}
}

func TestSpecForCompiledExecutiveSalesFailsClosedWhenPhysicalProjectionLosesCalendar(t *testing.T) {
	project, err := projectcompiler.LoadSourceRoot("../../../dashboards")
	if err != nil {
		t.Fatalf("compile dashboards fixture: %v", err)
	}
	report, ok := project.Manifest.DashboardDefinitions["dashboard:executive-sales"]
	if !ok {
		t.Fatal("compiled project omitted Executive Sales dashboard")
	}
	visual, ok := report.Visualizations["revenue_by_month"]
	if !ok {
		t.Fatal("compiled Executive Sales omitted revenue_by_month visual")
	}
	if visual.Query.DatasetID != "primary" {
		t.Fatalf("compiled visual slot = %q, want primary", visual.Query.DatasetID)
	}
	if visual.Query.Aggregate == nil || visual.Query.Aggregate.TableID != "sales_orders" {
		t.Fatalf("compiled aggregate dataset = %#v, want sales_orders", visual.Query.Aggregate)
	}
	model := project.Manifest.SemanticModels[report.SemanticModel]
	if model == nil {
		t.Fatalf("compiled semantic model %q is missing", report.SemanticModel)
	}
	if _, eligible := SpecForVisual(visual, model); eligible {
		t.Fatal("ISO-calendar semantic month dimension was mapped to a physical field")
	}

	categoryVisual, ok := report.Visualizations["category_revenue"]
	if !ok {
		t.Fatal("compiled Executive Sales omitted category_revenue visual")
	}
	spec, eligible := SpecForVisual(categoryVisual, model)
	if !eligible {
		t.Fatal("compiled non-temporal aggregate visual was reported as unsupported")
	}
	if spec.DatasetID == nil || *spec.DatasetID != "sales_orders" {
		t.Fatalf("Explorer dataset = %v, want compiled semantic dataset sales_orders", spec.DatasetID)
	}
	if len(spec.Dimensions) != 1 || spec.Dimensions[0].Field != "sales_orders.category" {
		t.Fatalf("compiled category dimension = %#v", spec.Dimensions)
	}
	if len(spec.Sort) != 1 || spec.Sort[0].Field != "revenue" {
		t.Fatalf("compiled sort field = %#v, want selected metric", spec.Sort)
	}
}

func TestSpecForVisualRejectsHiddenMetricsAndNonPhysicalTimeSemantics(t *testing.T) {
	definition := visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "ordered_at", Alias: "ordered_at", Grain: "week"}},
			Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}, Limit: 80,
		},
	}}

	model := exploreHandoffModel()
	model.Dimensions["ordered_at"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, Grains: []string{"day", "week"},
		Timezone: "UTC", Calendar: "gregorian", WeekStart: "sunday",
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	if _, eligible := SpecForVisual(definition, model); !eligible {
		t.Fatal("physical-equivalent semantic time defaults were incorrectly rejected")
	}

	model.Dimensions["ordered_at"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, Grains: []string{"day", "week"},
		Timezone: "America/Los_Angeles", Calendar: "gregorian", WeekStart: "sunday",
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	if _, eligible := SpecForVisual(definition, model); eligible {
		t.Fatal("non-UTC semantic timestamp was handed off as a physical dimension")
	}

	model.Dimensions["ordered_at"] = semanticmodel.SemanticDimension{
		Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, Grains: []string{"day", "week"},
		Timezone: "UTC", Calendar: "iso8601", WeekStart: "monday",
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}},
	}
	if _, eligible := SpecForVisual(definition, model); eligible {
		t.Fatal("ISO calendar / Monday-week semantic timestamp was handed off as a physical dimension")
	}

	model.Dimensions = map[string]semanticmodel.SemanticDimension{}
	model.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Hidden: true}
	definition.Query.Aggregate.Dimensions = nil
	if _, eligible := SpecForVisual(definition, model); eligible {
		t.Fatal("hidden metric was handed off although Explorer does not project it")
	}
}

func exploreHandoffModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
			"region": {}, "purchase_month": {}, "channel": {}, "ordered_at": {},
		}}},
		Dimensions: map[string]semanticmodel.SemanticDimension{},
		Metrics:    map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders"}},
	}
}

func TestHasActiveStateFailsClosedForUnprojectedFiltersAndInteractions(t *testing.T) {
	if !HasActiveState(dashboard.Filters{}, true) {
		t.Fatal("missing filter state with bindings was treated as unfiltered")
	}
	if HasActiveState(dashboard.Filters{CompiledState: &dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{}}}, true) {
		t.Fatal("empty compiled filter state was treated as active")
	}
	active := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{
		"region": {Expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionSet}},
	}}
	if !HasActiveState(dashboard.Filters{CompiledState: &active}, false) {
		t.Fatal("active filter expression was treated as unfiltered")
	}
	if !HasActiveState(dashboard.Filters{SpatialSelections: []dashboard.SpatialInteractionSelection{{VisualID: "map"}}}, false) {
		t.Fatal("spatial interaction was treated as unfiltered")
	}
}

func TestRouteHrefSuppressesScopedCandidateRoutes(t *testing.T) {
	if href, ok := RouteHref("/candidates/candidate-1/projects/project-1", "dash", "overview", "revenue", "client", "stream"); ok || href != "" {
		t.Fatalf("candidate RouteHref() = (%q, %t), want no handoff", href, ok)
	}
}
