package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestCanonicalSpatialBindingCarriesClusterPolicySeparatelyFromTransportRadius(t *testing.T) {
	radius := int32(96)
	maximumZoom := int32(12)
	minimumPoints := int32(3)
	showCount := true
	presentation := tiledPointPresentation(&document.DashboardMapCluster{Radius: &radius, MaximumZoom: &maximumZoom, MinimumPoints: &minimumPoints, ShowCount: &showCount})
	binding, err := canonicalSpatialBinding(tiledAggregateBinding(), presentation, document.DashboardQuery{})
	if err != nil {
		t.Fatalf("canonicalSpatialBinding: %v", err)
	}
	if binding.Spatial == nil || binding.Spatial.Tiles == nil || binding.Spatial.Tiles.Cluster == nil {
		t.Fatal("compiled tiled binding omitted cluster policy")
	}
	if got := binding.Spatial.Tiles.Cluster.Radius; got != radius {
		t.Fatalf("cluster radius = %d, want %d", got, radius)
	}
	if got := binding.Spatial.Tiles.CellRadius; got != 32 {
		t.Fatalf("transport cell radius = %d, want independent default 32", got)
	}
	if !binding.Spatial.Tiles.Cluster.ShowCount || binding.Spatial.Tiles.Cluster.MinimumPoints != minimumPoints {
		t.Fatalf("cluster policy = %#v", binding.Spatial.Tiles.Cluster)
	}
}

func TestCanonicalSpatialBindingRejectsIncompatiblePointClusterPoliciesWithPaths(t *testing.T) {
	firstRadius, secondRadius := int32(40), int32(48)
	first := tiledPoint(&document.DashboardMapCluster{Radius: &firstRadius})
	second := tiledPoint(&document.DashboardMapCluster{Radius: &secondRadius})
	layers := []document.DashboardGeographicLayer{{Value: first}, {Value: second}}
	presentation := &document.GeographicDashboardPresentation{Layers: &layers}
	_, err := canonicalSpatialBinding(tiledAggregateBinding(), presentation, document.DashboardQuery{})
	if err == nil || !strings.Contains(err.Error(), "presentation.layers[1].cluster") || !strings.Contains(err.Error(), "presentation.layers[0].cluster") {
		t.Fatalf("error = %v, want both cluster paths", err)
	}
}

func TestCanonicalSpatialBindingRejectsClusterAtTiledTerminalZoom(t *testing.T) {
	maximumZoom := int32(18)
	_, err := canonicalSpatialBinding(tiledAggregateBinding(), tiledPointPresentation(&document.DashboardMapCluster{MaximumZoom: &maximumZoom}), document.DashboardQuery{})
	if err == nil || !strings.Contains(err.Error(), "presentation.layers[0].cluster.maximumZoom") || !strings.Contains(err.Error(), "below tiled terminal zoom 18") {
		t.Fatalf("error = %v, want terminal cluster zoom path", err)
	}
}

func TestCanonicalSpatialBindingValidatesClusterRadiusBounds(t *testing.T) {
	for _, radius := range []int32{0, 1, 512, 513} {
		binding, err := canonicalSpatialBinding(tiledAggregateBinding(), tiledPointPresentation(&document.DashboardMapCluster{Radius: &radius}), document.DashboardQuery{})
		if radius < 1 || radius > 512 {
			if err == nil || !strings.Contains(err.Error(), "presentation.layers[0].cluster") || !strings.Contains(err.Error(), "radius must be between 1 and 512") {
				t.Errorf("radius %d: error = %v, want actionable cluster radius path", radius, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("radius %d: %v", radius, err)
		}
		if err := binding.Validate(); err != nil {
			t.Errorf("radius %d: compiled binding invalid: %v", radius, err)
		}
	}
}

func TestCanonicalSpatialBindingRejectsClusteredPointWithHeatSharingTiledSource(t *testing.T) {
	layers := []document.DashboardGeographicLayer{{Value: tiledPoint(nil)}, {Value: tiledHeat()}}
	_, err := canonicalSpatialBinding(tiledAggregateBinding(), &document.GeographicDashboardPresentation{Layers: &layers}, document.DashboardQuery{})
	if err == nil || !strings.Contains(err.Error(), "presentation.layers[0].cluster") || !strings.Contains(err.Error(), "presentation.layers[1].heat") {
		t.Fatalf("error = %v, want point cluster and heat paths", err)
	}
}

func TestCanonicalSpatialBindingAllowsNonClusteredPointWithHeatSharingTiledSource(t *testing.T) {
	disabled := false
	layers := []document.DashboardGeographicLayer{{Value: tiledPoint(&document.DashboardMapCluster{Enabled: &disabled})}, {Value: tiledHeat()}}
	if _, err := canonicalSpatialBinding(tiledAggregateBinding(), &document.GeographicDashboardPresentation{Layers: &layers}, document.DashboardQuery{}); err != nil {
		t.Fatalf("non-clustered point and heat should share a tiled source: %v", err)
	}
}

func tiledAggregateBinding() visualizationdefinition.QueryBinding {
	return visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultGeographicFeatures,
		ModelID: "sales", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{
			TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{
				{FieldID: "orders.id", Alias: "id"}, {FieldID: "orders.latitude", Alias: "latitude"}, {FieldID: "orders.longitude", Alias: "longitude"},
			},
		},
	}
}

func tiledPointPresentation(cluster *document.DashboardMapCluster) *document.GeographicDashboardPresentation {
	layers := []document.DashboardGeographicLayer{{Value: tiledPoint(cluster)}}
	return &document.GeographicDashboardPresentation{Layers: &layers}
}

func tiledPoint(cluster *document.DashboardMapCluster) *document.DashboardPointGeographicLayer {
	return &document.DashboardPointGeographicLayer{DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "points"}, Kind: "point"}, Kind: "point", Latitude: "latitude", Longitude: "longitude", Cluster: cluster}
}

func tiledHeat() *document.DashboardHeatGeographicLayer {
	return &document.DashboardHeatGeographicLayer{DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "heat"}, Kind: "heat"}, Kind: "heat", Latitude: "latitude", Longitude: "longitude"}
}
