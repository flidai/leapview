package authoring

import (
	"strings"
	"testing"
)

func TestValidateGeographicVisualUsesClosedAliasBoundLayers(t *testing.T) {
	base := Visual{
		Type: "map",
		Query: VisualQuery{
			Dimensions: []FieldRef{{Field: "orders.state", Alias: "state"}, {Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}},
			Measures:   []FieldRef{{Field: "revenue", Alias: "revenue"}},
		},
		Geo: VisualGeo{Layers: []VisualGeoLayer{
			{ID: "states", Kind: "choropleth", GeometryAsset: "brazil_states", Join: "state", Value: "revenue", Tooltip: []string{"state", "revenue"}, Color: VisualGeoColorScale{Kind: "sequential", Palette: "blue"}},
			{ID: "orders", Kind: "point", Latitude: "latitude", Longitude: "longitude", Value: "revenue", Label: "state", Size: VisualGeoSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: VisualGeoCluster{Enabled: true, Radius: 48, MaximumZoom: 10, ShowCount: true}},
		}},
	}
	if err := validateGeographicVisual("map", base); err != nil {
		t.Fatalf("valid geographic visual: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Visual)
		want   string
	}{
		{name: "unknown alias", mutate: func(visual *Visual) { visual.Geo.Layers[1].Latitude = "missing" }, want: `references unknown query alias "missing"`},
		{name: "duplicate layer", mutate: func(visual *Visual) { visual.Geo.Layers[1].ID = "states" }, want: `duplicate geographic layer "states"`},
		{name: "mixed coordinate and geometry", mutate: func(visual *Visual) { visual.Geo.Layers[1].GeometryAsset = "brazil_states" }, want: "does not accept geometry_asset or join"},
		{name: "point radius order", mutate: func(visual *Visual) { visual.Geo.Layers[1].Size.MinimumRadius = 30 }, want: "minimum_radius must not exceed maximum_radius"},
		{name: "cluster on choropleth", mutate: func(visual *Visual) { visual.Geo.Layers[0].Cluster.Enabled = true }, want: "clustering is only supported for point layers"},
		{name: "unknown tooltip", mutate: func(visual *Visual) { visual.Geo.Layers[0].Tooltip = []string{"missing"} }, want: `tooltip references unknown query alias "missing"`},
		{name: "unknown kind", mutate: func(visual *Visual) { visual.Geo.Layers[1].Kind = "tiles" }, want: `unsupported kind "tiles"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			visual := base
			visual.Geo.Layers = append([]VisualGeoLayer(nil), base.Geo.Layers...)
			tt.mutate(&visual)
			err := validateGeographicVisual("map", visual)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateGeographicPointSelectionUsesStableQueryAliases(t *testing.T) {
	base := Visual{
		Type: "map",
		Query: VisualQuery{
			Dimensions: []FieldRef{
				{Field: "orders.customer_id", Alias: "customer_id"},
				{Field: "orders.latitude", Alias: "latitude"},
				{Field: "orders.longitude", Alias: "longitude"},
			},
			Time:     QueryTime{Field: "orders.created_at", Grain: "day", Alias: "created_day"},
			Measures: []FieldRef{{Field: "revenue", Alias: "revenue"}},
		},
		Geo: VisualGeo{Layers: []VisualGeoLayer{{ID: "customers", Kind: "point", Latitude: "latitude", Longitude: "longitude", Value: "revenue"}}},
		Interaction: Interaction{PointSelection: SelectionInteraction{Mappings: []SelectionMapping{
			{Field: "orders.customer_id", Fact: "orders", Value: "customer_id", Label: "revenue"},
			{Field: "orders.created_at", Fact: "orders", Grain: "day", Value: "created_day"},
		}, Targets: []string{"detail"}}},
	}
	if err := ValidateVisualPointSelectionMappingKeys("map", base); err != nil {
		t.Fatalf("valid geographic interaction: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Visual)
		want   string
	}{
		{name: "unknown value alias", mutate: func(visual *Visual) { visual.Interaction.PointSelection.Mappings[0].Value = "missing" }, want: `references unknown value query alias "missing"`},
		{name: "measure identity", mutate: func(visual *Visual) { visual.Interaction.PointSelection.Mappings[0].Value = "revenue" }, want: `value query alias "revenue" must reference a dimension or time field`},
		{name: "unknown label alias", mutate: func(visual *Visual) { visual.Interaction.PointSelection.Mappings[0].Label = "missing" }, want: `references unknown label query alias "missing"`},
		{name: "heat only", mutate: func(visual *Visual) {
			visual.Geo.Layers = []VisualGeoLayer{{ID: "heat", Kind: "heat", Latitude: "latitude", Longitude: "longitude", Value: "revenue"}}
		}, want: "requires at least one point or choropleth layer"},
		{name: "density only", mutate: func(visual *Visual) {
			visual.Geo.Layers = []VisualGeoLayer{{ID: "density", Kind: "density", Latitude: "latitude", Longitude: "longitude"}}
		}, want: "requires at least one point or choropleth layer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			visual := base
			visual.Geo.Layers = append([]VisualGeoLayer(nil), base.Geo.Layers...)
			visual.Interaction.PointSelection.Mappings = append([]SelectionMapping(nil), base.Interaction.PointSelection.Mappings...)
			tt.mutate(&visual)
			err := ValidateVisualPointSelectionMappingKeys("map", visual)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateGeographicSpatialSelectionUsesTypedCoordinateMappings(t *testing.T) {
	base := Visual{
		Type: "map",
		Query: VisualQuery{Dimensions: []FieldRef{
			{Field: "customers.latitude", Alias: "latitude"},
			{Field: "customers.longitude", Alias: "longitude"},
		}},
		Geo: VisualGeo{Layers: []VisualGeoLayer{{ID: "density", Kind: "density", Latitude: "latitude", Longitude: "longitude"}}},
		Interaction: Interaction{SpatialSelection: SpatialSelectionInteraction{
			Gestures:  []string{"box", "lasso", "radius"},
			Latitude:  SpatialSelectionMapping{Source: "latitude", Field: "customers.latitude", Fact: "orders"},
			Longitude: SpatialSelectionMapping{Source: "longitude", Field: "customers.longitude", Fact: "orders"},
			Targets:   []string{"detail"},
		}},
	}
	if err := validateSpatialSelectionInteraction("map", base); err != nil {
		t.Fatalf("valid spatial interaction: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Visual)
		want   string
	}{
		{name: "unknown gesture", mutate: func(visual *Visual) { visual.Interaction.SpatialSelection.Gestures = []string{"pan"} }, want: `unsupported gesture "pan"`},
		{name: "duplicate gesture", mutate: func(visual *Visual) { visual.Interaction.SpatialSelection.Gestures = []string{"box", "box"} }, want: `duplicate gesture "box"`},
		{name: "unknown source", mutate: func(visual *Visual) { visual.Interaction.SpatialSelection.Latitude.Source = "missing" }, want: `unknown stable query alias "missing"`},
		{name: "layer mismatch", mutate: func(visual *Visual) { visual.Geo.Layers[0].Latitude = "other" }, want: "must match one coordinate layer"},
		{name: "missing fact", mutate: func(visual *Visual) { visual.Interaction.SpatialSelection.Latitude.Fact = "" }, want: "requires fact"},
		{name: "missing targets", mutate: func(visual *Visual) { visual.Interaction.SpatialSelection.Targets = nil }, want: "requires targets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			visual := base
			visual.Geo.Layers = append([]VisualGeoLayer(nil), base.Geo.Layers...)
			visual.Interaction.SpatialSelection.Gestures = append([]string(nil), base.Interaction.SpatialSelection.Gestures...)
			visual.Interaction.SpatialSelection.Targets = append([]string(nil), base.Interaction.SpatialSelection.Targets...)
			tt.mutate(&visual)
			err := validateSpatialSelectionInteraction("map", visual)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
