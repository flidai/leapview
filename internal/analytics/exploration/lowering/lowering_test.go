package lowering

import (
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestQueryLowersSemanticSpecWithoutBrowserDependencies(t *testing.T) {
	alias := "Status"
	dataset := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status", Alias: &alias}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters:    []exploration.ExplorationFilter{{Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "shipped"}}}}}}},
		Sort:       []exploration.ExplorationSort{{Field: "orders.status", Direction: exploration.ExplorationSortDirectionAsc}},
		Limit:      100,
	}
	query, err := Query(spec)
	if err != nil {
		t.Fatal(err)
	}
	if query.ModelID != spec.ModelID || query.Target != dataset || len(query.Fields) != 1 || query.Fields[0].Alias != alias || len(query.Metrics) != 1 || len(query.Filters) != 1 || query.Limit != 101 {
		t.Fatalf("lowered query = %#v", query)
	}
	if query.Filters[0].Values[0] != "shipped" || query.Sort[0].Direction != "asc" {
		t.Fatalf("lowered filter/sort = %#v %#v", query.Filters, query.Sort)
	}
}

func TestQueryRejectsPivotFailClosed(t *testing.T) {
	spec := basicSpec()
	spec.Pivot = &exploration.ExplorationPivotConfig{}
	if _, err := Query(spec); err == nil {
		t.Fatal("pivot query error = nil")
	}
}

func TestQueryForModelClearsMultiRootMetricTarget(t *testing.T) {
	spec := basicSpec()
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate"}}}
	query, err := QueryForModel(spec, model)
	if err != nil {
		t.Fatal(err)
	}
	if query.Target != "" {
		t.Fatalf("multi-root target = %q, want empty", query.Target)
	}
	model.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders"}
	query, err = QueryForModel(spec, model)
	if err != nil {
		t.Fatal(err)
	}
	if query.Target != "orders" {
		t.Fatalf("single-root target = %q, want orders", query.Target)
	}
}

func TestQueryRejectsUnsupportedRelativeFilter(t *testing.T) {
	spec := basicSpec()
	spec.Filters = []exploration.ExplorationFilter{{Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.RelativePeriodExplorationFilterExpression{Kind: "relative_period"}}}}
	if _, err := Query(spec); err == nil {
		t.Fatal("relative filter error = nil")
	}
}

func TestQueryHandlesMaximumValidatedSelectionCounts(t *testing.T) {
	spec := basicSpec()
	spec.Dimensions = make([]exploration.ExplorationDimensionRef, 100)
	for index := range spec.Dimensions {
		spec.Dimensions[index] = exploration.ExplorationDimensionRef{Field: fmt.Sprintf("orders.dimension_%d", index)}
	}
	spec.Metrics = make([]exploration.ExplorationMetricRef, 100)
	for index := range spec.Metrics {
		spec.Metrics[index] = exploration.ExplorationMetricRef{Field: fmt.Sprintf("metric_%d", index)}
	}
	spec.Filters = make([]exploration.ExplorationFilter, 100)
	for index := range spec.Filters {
		spec.Filters[index] = exploration.ExplorationFilter{
			Field: fmt.Sprintf("orders.dimension_%d", index),
			Expression: exploration.ExplorationFilterExpression{Value: &exploration.RangeExplorationFilterExpression{
				Kind: "range", Lower: &exploration.ExplorationFilterBound{
					Inclusive: true,
					Value:     exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{Kind: "integer", Value: "0"}},
				}, Upper: &exploration.ExplorationFilterBound{
					Value: exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{Kind: "integer", Value: "100"}},
				},
			}},
		}
	}

	query, err := Query(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Fields) != len(spec.Dimensions) || len(query.Metrics) != len(spec.Metrics) || len(query.Filters) != 2*len(spec.Filters) {
		t.Fatalf("lowered maximum-shape query counts = fields %d, metrics %d, filters %d; want %d, %d, %d", len(query.Fields), len(query.Metrics), len(query.Filters), len(spec.Dimensions), len(spec.Metrics), 2*len(spec.Filters))
	}
	if first, last := query.Filters[0], query.Filters[len(query.Filters)-1]; first.Field != "orders.dimension_0" || first.Operator != "greater_than_or_equal" || first.Values[0] != int64(0) || last.Field != "orders.dimension_99" || last.Operator != "less_than" || last.Values[0] != int64(100) {
		t.Fatalf("lowered range boundary filters = %#v, %#v", first, last)
	}
}

func basicSpec() exploration.ExplorationSpec {
	dataset := "orders"
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
}
