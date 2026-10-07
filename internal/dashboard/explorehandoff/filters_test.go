package explorehandoff

import (
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/lowering"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"testing"
)

func TestSpecForStatePreservesResolvedRangeAndTargetScope(t *testing.T) {
	model := exploreHandoffModel()
	table := model.Tables["orders"]
	table.Dimensions["ordered_at"] = semanticmodel.MetricDimension{Type: "date", Datatype: semanticmodel.DataTypeDate}
	model.Tables["orders"] = table
	visual := visualizationdefinition.Definition{ID: "revenue", Query: visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}, Limit: 100}}}
	definition := dashboarddefinition.Definition{Visualizations: map[string]visualizationdefinition.Definition{"revenue": visual}, FilterDefinitions: map[string]dashboardfilter.Definition{"date": {Field: "orders.ordered_at", Dataset: "orders"}}, FilterBindings: map[string]dashboardfilter.Binding{"date": {Key: "date", Filter: "date", Targets: []string{"overview/revenue"}}}}
	state := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{"date": {Expression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRelativePeriod}, ResolvedExpression: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRange, Lower: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDate, Value: "2026-01-01"}, Inclusive: true}, Upper: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDate, Value: "2026-02-01"}, Inclusive: false}}}}}
	filters := dashboard.Filters{CompiledState: &state}
	if _, ok := SpecForState(definition, model, "revenue", "overview", dashboard.Filters{}); ok {
		t.Fatal("missing compiled filter state accepted")
	}
	if _, ok := SpecForState(definition, model, "revenue", "overview", dashboard.Filters{CompiledState: &state, SpatialSelections: []dashboard.SpatialInteractionSelection{{VisualID: "map"}}}); ok {
		t.Fatal("spatial selection must remain unavailable")
	}
	spec, ok := SpecForState(definition, model, "revenue", "overview", filters)
	if !ok || len(spec.Filters) != 2 {
		t.Fatalf("resolved filter handoff = %#v, supported=%v", spec, ok)
	}
	lowered, err := (lowering.Service{}).Filters(spec)
	if err != nil || len(lowered) != 2 || lowered[0].Operator != "greater_than_or_equal" || lowered[1].Operator != "less_than" || lowered[0].Values[0] != "2026-01-01" || lowered[1].Values[0] != "2026-02-01" {
		t.Fatalf("resolved bounds changed: %#v, err=%v", lowered, err)
	}
	for _, filter := range spec.Filters {
		if _, ok := filter.Expression.Value.(*exploration.ComparisonExplorationFilterExpression).Value.Value.(*exploration.DateExplorationFilterValue); !ok {
			t.Fatalf("date lost typed value: %#v", filter)
		}
	}
	spec, ok = SpecForState(definition, model, "revenue", "details", filters)
	if !ok || len(spec.Filters) != 0 {
		t.Fatalf("unrelated-page filter leaked: %#v", spec)
	}
}

func TestExplorerFilterPreservesAdditiveSingleFieldSelections(t *testing.T) {
	predicate := report.QueryFilter{Groups: []report.QueryFilterGroup{{Filters: []report.QueryFilter{{Field: "orders.region", Operator: "equals", Values: []any{"east"}}}}, {Filters: []report.QueryFilter{{Field: "orders.region", Operator: "equals", Values: []any{"west"}}}}}}
	converted, ok := explorerFilter(exploreHandoffModel(), "orders", predicate)
	if !ok {
		t.Fatal("single-field additive selection rejected")
	}
	lowered, err := (lowering.Service{}).Filters(exploration.ExplorationSpec{Filters: []exploration.ExplorationFilter{converted}})
	if err != nil || len(lowered) != 1 || lowered[0].Operator != "in" || len(lowered[0].Values) != 2 {
		t.Fatalf("additive selection changed: %#v, %v", lowered, err)
	}
	predicate.Groups[1].Filters[0].Field = "orders.channel"
	if _, ok := explorerFilter(exploreHandoffModel(), "orders", predicate); ok {
		t.Fatal("multi-field disjunction must not be flattened")
	}
}
