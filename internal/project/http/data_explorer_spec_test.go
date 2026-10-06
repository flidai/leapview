package http

import (
	"reflect"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
)

func TestDefaultExplorationSpecIsCanonical(t *testing.T) {
	spec := defaultExplorationSpec()
	if spec.SchemaVersion != 1 || spec.Limit != 100 || spec.Dimensions == nil || spec.Metrics == nil || spec.Filters == nil || spec.Sort == nil {
		t.Fatalf("default spec = %#v", spec)
	}
}

func TestDataExploreStateRoundTripPreservesCanonicalReferences(t *testing.T) {
	dataset := "orders"
	spec := defaultExplorationSpec()
	spec.ModelID = "semantic:sales"
	spec.DatasetID = &dataset
	spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.status", Alias: stringPointer("status")}}
	spec.Metrics = []exploration.ExplorationMetricRef{{Field: "revenue", Alias: stringPointer("net_revenue")}}
	spec.Sort = []exploration.ExplorationSort{{Field: "revenue", Direction: exploration.ExplorationSortDirectionDesc}}

	state := dataExploreStateFromSpec(spec)
	restored := explorationSpecWithState(spec, state)
	if !reflect.DeepEqual(restored, spec) {
		t.Fatalf("round trip = %#v, want %#v", restored, spec)
	}
}

func TestDataExploreStateRoundTripPreservesTypedAndRichFilters(t *testing.T) {
	dataset := "orders"
	spec := defaultExplorationSpec()
	spec.Filters = []exploration.ExplorationFilter{
		{
			Field: "orders.quantity", DatasetID: &dataset,
			Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
				ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "comparison"}, Kind: "comparison", Operator: "greater_than",
				Value: exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "integer"}, Kind: "integer", Value: "2"}},
			}},
		},
		{
			Field: "orders.amount", DatasetID: &dataset,
			Expression: exploration.ExplorationFilterExpression{Value: &exploration.RangeExplorationFilterExpression{
				ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "range"}, Kind: "range",
				Lower: &exploration.ExplorationFilterBound{Inclusive: true, Value: exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "decimal"}, Kind: "decimal", Value: "1.5"}}},
			}},
		},
	}

	restored := explorationSpecWithState(spec, dataExploreStateFromSpec(spec))
	if !reflect.DeepEqual(restored.Filters, spec.Filters) {
		t.Fatalf("filters = %#v, want %#v", restored.Filters, spec.Filters)
	}
}

func stringPointer(value string) *string { return &value }
