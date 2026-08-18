package query

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestSpatialPredicateRendersThroughPlanIR(t *testing.T) {
	tests := []struct {
		name  string
		value planir.SpatialPredicate
		parts []string
	}{
		{name: "box", value: planir.SpatialPredicate{Kind: "box", Latitude: "lat", Longitude: "lon", West: -74, South: -34, East: -34, North: 6}, parts: []string{"lat", "lon", "?"}},
		{name: "antimeridian box", value: planir.SpatialPredicate{Kind: "box", Latitude: "lat", Longitude: "lon", West: 170, South: -10, East: -170, North: 10}, parts: []string{"OR", "?"}},
		{name: "lasso", value: planir.SpatialPredicate{Kind: "lasso", Latitude: "lat", Longitude: "lon", Points: []planir.SpatialPoint{{Longitude: -50, Latitude: -20}, {Longitude: -40, Latitude: -20}, {Longitude: -45, Latitude: -10}}}, parts: []string{"MOD(", "CASE WHEN", "NULLIF"}},
		{name: "radius", value: planir.SpatialPredicate{Kind: "radius", Latitude: "lat", Longitude: "lon", Center: planir.SpatialPoint{Longitude: -46.63, Latitude: -23.55}, RadiusMeters: 50000}, parts: []string{"ASIN", "RADIANS", "?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := renderTypedSpatial(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range tt.parts {
				if !strings.Contains(sql, part) {
					t.Fatalf("SQL = %s, want containing %q", sql, part)
				}
			}
			if len(args) == 0 {
				t.Fatalf("SQL predicate has no bound arguments: %s", sql)
			}
		})
	}
}

func TestValidateSpatialFilterRejectsUnsafeGeometry(t *testing.T) {
	tests := []SpatialFilter{
		{Kind: "box", West: -181, South: 0, East: 1, North: 2},
		{Kind: "box", West: 0, South: 2, East: 1, North: 1},
		{Kind: "lasso", Points: []SpatialPoint{{}, {}}},
		{Kind: "lasso", Points: []SpatialPoint{{Longitude: -170, Latitude: 0}, {Longitude: 170, Latitude: 1}, {Longitude: 0, Latitude: 2}}},
		{Kind: "radius", Center: SpatialPoint{}, RadiusMeters: 0},
		{Kind: "radius", Center: SpatialPoint{}, RadiusMeters: 5_000_001},
		{Kind: "polygon"},
	}
	for _, filter := range tests {
		if err := ValidateSpatialFilter(filter); err == nil {
			t.Fatalf("unsafe spatial filter accepted: %#v", filter)
		}
	}
}

func renderTypedSpatial(predicate planir.SpatialPredicate) (string, []any, error) {
	lineage := []planir.PhysicalLineage{{Logical: "lat", Dataset: "points", Field: "lat"}, {Logical: "lon", Dataset: "points", Field: "lon"}}
	scanMeta := planir.NodeMeta{NodeID: "scan", OutputGrain: planir.Grain{Fields: []string{"lat", "lon"}}, AvailableFields: []planir.Field{{Name: "lat", Type: "float"}, {Name: "lon", Type: "float"}}, RootDatasets: []string{"points"}, FilterPhase: planir.FilterPhaseScan, PhysicalLineage: lineage}
	filterMeta := scanMeta
	filterMeta.NodeID = "filter"
	aggMeta := planir.NodeMeta{NodeID: "aggregate", OutputGrain: planir.Grain{Fields: []string{"lat", "lon"}}, AvailableFields: []planir.Field{{Name: "lat", Type: "float"}, {Name: "lon", Type: "float"}}, AvailableMetrics: []planir.Metric{{Name: "rows", Type: "integer"}}, RootDatasets: []string{"points"}, FilterPhase: planir.FilterPhaseAggregate, PhysicalLineage: lineage}
	graph := &planir.Graph{NodeMeta: aggMeta, Roots: []string{"scan"}, Output: "aggregate", Nodes: map[string]planir.Node{
		"scan":      planir.ScanDataset{NodeMeta: scanMeta, Dataset: "points"},
		"filter":    planir.FilterRows{NodeMeta: filterMeta, Input: "scan", Predicate: planir.Predicate{Kind: planir.PredicateSpatial, Spatial: &predicate}, Source: planir.FilterSourceRequest, Fields: []string{"lat", "lon"}},
		"aggregate": planir.AggregateMetrics{NodeMeta: aggMeta, Input: "filter", GroupBy: []string{"lat", "lon"}, Metrics: []planir.MetricSpec{{Name: "rows", Aggregation: "COUNT_STAR"}}},
	}}
	rendered, err := planir.RenderDuckDB(graph)
	return rendered.SQL, rendered.Args, err
}
