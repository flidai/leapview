package runtime

import (
	"testing"

	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestSpatialFilterFromSelectionPreservesExactGovernedGeometry(t *testing.T) {
	resolved := reportmodel.ResolvedSpatialSelectionInteraction{
		Latitude:  reportmodel.ResolvedSelectionMapping{Field: "customers.latitude", Dataset: "customers"},
		Longitude: reportmodel.ResolvedSelectionMapping{Field: "customers.longitude", Dataset: "customers"},
	}
	tests := []struct {
		name     string
		geometry visualizationir.VisualizationSpatialSelectionGeometry
		wantKind string
		assert   func(*testing.T, reportdef.SpatialFilter)
	}{
		{name: "box", geometry: visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialBoxSelection{Kind: "box", Bounds: visualizationir.VisualizationSpatialBounds{West: 170, South: -20, East: -170, North: 20}}}, wantKind: "box", assert: func(t *testing.T, filter reportdef.SpatialFilter) {
			if filter.West != 170 || filter.East != -170 {
				t.Fatalf("box = %#v", filter)
			}
		}},
		{name: "lasso", geometry: visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialLassoSelection{Kind: "lasso", Points: []visualizationir.VisualizationSpatialCoordinate{{Longitude: -50, Latitude: -20}, {Longitude: -45, Latitude: -20}, {Longitude: -45, Latitude: -15}}}}, wantKind: "lasso", assert: func(t *testing.T, filter reportdef.SpatialFilter) {
			if len(filter.Points) != 3 {
				t.Fatalf("lasso = %#v", filter)
			}
		}},
		{name: "radius", geometry: visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialRadiusSelection{Kind: "radius", Center: visualizationir.VisualizationSpatialCoordinate{Longitude: -46, Latitude: -23}, RadiusMeters: 25_000}}, wantKind: "radius", assert: func(t *testing.T, filter reportdef.SpatialFilter) {
			if filter.RadiusMeters != 25_000 {
				t.Fatalf("radius = %#v", filter)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter, err := spatialFilterFromSelection(resolved, test.geometry)
			if err != nil {
				t.Fatal(err)
			}
			if filter.Kind != test.wantKind || filter.LatitudeField != "customers.latitude" || filter.LongitudeField != "customers.longitude" || filter.Dataset != "customers" {
				t.Fatalf("filter = %#v", filter)
			}
			test.assert(t, filter)
		})
	}
}
