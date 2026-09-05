package lowering

import (
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

func basicSpec() exploration.ExplorationSpec {
	dataset := "orders"
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
}
