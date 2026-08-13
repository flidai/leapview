package analyticsruntime

import (
	"testing"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestRequiredWorkspaceExtensionsIncludesSpatialOnlyForTiledMaps(t *testing.T) {
	definition := &dashboarddefinition.Workspace{Dashboards: map[string]dashboarddefinition.Definition{
		"orders": {Visualizations: map[string]visualizationdefinition.Definition{
			"map": {Query: visualizationdefinition.QueryBinding{Spatial: &visualizationdefinition.SpatialQueryBinding{Tiles: &visualizationdefinition.SpatialTileBinding{}}}},
		}},
	}}
	got := requiredWorkspaceExtensions(definition)
	if len(got) != 1 || got[0] != "spatial" {
		t.Fatalf("required extensions = %#v, want spatial", got)
	}
	definition.Dashboards["orders"] = dashboarddefinition.Definition{Visualizations: map[string]visualizationdefinition.Definition{
		"map": {Query: visualizationdefinition.QueryBinding{Spatial: &visualizationdefinition.SpatialQueryBinding{Viewport: &visualizationdefinition.SpatialViewportBinding{}}}},
	}}
	if got := requiredWorkspaceExtensions(definition); len(got) != 0 {
		t.Fatalf("legacy viewport required extensions = %#v, want none", got)
	}
}
