package http

import (
	"encoding/json"
	"reflect"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
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

func TestNullCheckCommandsKeepEmptyOperandArraysForBrowserRoundTrip(t *testing.T) {
	for _, operator := range []string{"is_null", "is_not_null"} {
		t.Run(operator, func(t *testing.T) {
			spec := defaultExplorationSpec()
			spec.ModelID = "semantic:sales"
			spec.Filters = []exploration.ExplorationFilter{{
				Field: "orders.status",
				Expression: exploration.ExplorationFilterExpression{Value: &exploration.NullCheckExplorationFilterExpression{
					ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "null_check"},
					Kind:                            "null_check", Operator: operator,
				}},
			}}
			command := dataExploreCommandWithCanonicalSpec(projectsignals.DataExploreCommand{Spec: spec})
			encoded, err := json.Marshal(command)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Filters []struct {
					Values json.RawMessage `json:"values"`
				} `json:"filters"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if len(wire.Filters) != 1 || string(wire.Filters[0].Values) != "[]" {
				t.Fatalf("null-check browser operands must remain an empty array: %s", encoded)
			}
			var restored projectsignals.DataExploreCommand
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			refreshed := dataExploreCommandRefreshSpec(restored)
			actual, err := json.Marshal(refreshed.Spec.Filters)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(spec.Filters)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Fatalf("null-check changed after command round trip: %s, want %s", actual, expected)
			}
		})
	}
}
