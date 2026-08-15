package authoring

import (
	"slices"
	"testing"
)

func TestReleasedVisualizationCatalogExcludesCustomRenderers(t *testing.T) {
	for _, visualType := range SupportedVisualizationTypes() {
		if visualType == "custom" {
			t.Fatal("custom renderers must not be part of the released visualization catalog")
		}
	}
	if slices.Contains(SupportedVisualShapes(), "custom") {
		t.Fatal("custom result shapes must not be part of the released visualization contract")
	}
}
