package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCanonicalGeographicLayersLowerPointAndChoroplethLabels(t *testing.T) {
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{
		{Name: "latitude"}, {Name: "longitude"}, {Name: "state"}, {Name: "city"}, {Name: "revenue"},
	}}
	label := "city"

	point := document.DashboardPointGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{
			DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "points"},
			Kind:                            "point",
		},
		Kind:     "point",
		Latitude: "latitude", Longitude: "longitude", Label: &label,
	}
	choropleth := document.DashboardChoroplethGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{
			DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "states"},
			Kind:                            "choropleth",
		},
		Kind:          "choropleth",
		GeometryAsset: "brazil_states", Join: "state", Label: &label,
	}

	pointLayer, err := canonicalPointGeographicLayer(&point, query)
	if err != nil {
		t.Fatalf("lower point layer: %v", err)
	}
	pointValue, ok := pointLayer.Value.(*visualizationir.VisualizationPointLayer)
	if !ok || pointValue.Label == nil || pointValue.Label.Field != "city" {
		t.Fatalf("point label = %#v, want primary.city", pointValue)
	}

	choroplethLayer, err := canonicalChoroplethGeographicLayer(&choropleth, query)
	if err != nil {
		t.Fatalf("lower choropleth layer: %v", err)
	}
	choroplethValue, ok := choroplethLayer.Value.(*visualizationir.VisualizationChoroplethLayer)
	if !ok || choroplethValue.Label == nil || choroplethValue.Label.Field != "city" {
		t.Fatalf("choropleth label = %#v, want primary.city", choroplethValue)
	}
}

func TestCanonicalGeographicLayerLabelReferencesUseActionablePath(t *testing.T) {
	label := "unknown"
	layer := document.DashboardPointGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{
			DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "points"},
			Kind:                            "point",
		},
		Kind:     "point",
		Latitude: "latitude", Longitude: "longitude", Label: &label,
	}
	presentation := document.GeographicDashboardPresentation{
		Layers: &[]document.DashboardGeographicLayer{{Value: &layer}},
	}
	_, err := canonicalGeographicLayers(&presentation, LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Name: "latitude"}, {Name: "longitude"}}})
	if err == nil || !strings.Contains(err.Error(), "presentation.layers[0]: label:") || !strings.Contains(err.Error(), `reference "unknown" is not a compiled result field`) {
		t.Fatalf("invalid point label error = %v, want presentation.layers[0]: label: unknown field path", err)
	}
}

func TestCanonicalMapLineHonorsWidthAndDefaults(t *testing.T) {
	width := 7.5
	got, err := canonicalMapLine(&document.DashboardMapLineStyle{Width: &width})
	if err != nil {
		t.Fatalf("lower line style: %v", err)
	}
	if got.Width != width {
		t.Fatalf("line style = %#v, want width %v", got, width)
	}

	got, err = canonicalMapLine(nil)
	if err != nil {
		t.Fatalf("lower default line style: %v", err)
	}
	if got.Width != 3 {
		t.Fatalf("default line style = %#v, want width 3", got)
	}
	zeroWidth := 0.0
	if got, err = canonicalMapLine(&document.DashboardMapLineStyle{Width: &zeroWidth}); err != nil || got.Width != 0 {
		t.Fatalf("zero-width line style = %#v, %v, want width 0", got, err)
	}

	negativeWidth := -1.0
	if _, err := canonicalMapLine(&document.DashboardMapLineStyle{Width: &negativeWidth}); err == nil || !strings.Contains(err.Error(), "line style has invalid width") {
		t.Fatalf("negative line width error = %v, want invalid-width diagnostic", err)
	}
}
