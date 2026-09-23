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

func stringPointer(value string) *string { return &value }
