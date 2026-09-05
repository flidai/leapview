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
