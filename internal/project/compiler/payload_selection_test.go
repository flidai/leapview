package compiler

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestVisualPayloadIncludesPointSelectionContract(t *testing.T) {
	dashboardDefinition := &dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
		"source": {Type: "bar", Title: "Source", Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "activity_date", Alias: "label"}}, Measures: []dashboardauthoring.FieldRef{{Field: "event_count", Alias: "value"}}, Limit: 100,
		}, Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
			Toggle: true,
			Mappings: []dashboardauthoring.SelectionMapping{{
				Field: "activity_date",
				Grain: "month",
				Value: "label",
				Label: "label",
			}},
			Targets: []string{"tags_per_rating"},
		}}},
		"tags_per_rating": {Type: "bar", Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "tag", Alias: "tag"}}, Measures: []dashboardauthoring.FieldRef{{Field: "event_count", Alias: "value"}}, Limit: 100,
		}},
	})}

	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(definitions["source"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"interactions"`, `"activity_date"`, `"month"`, `"tags_per_rating"`} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Fatalf("visual payload = %s, want %s", payload, want)
		}
	}
	revision, err := visualizationir.ComputeSpecRevision(definitions["source"].Spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := definitions["source"].SpecRevision; got != revision.String() {
		t.Fatalf("completed interaction graph revision = %q, want %q", got, revision)
	}
}

func TestTablePayloadIncludesFactLocalRowSelectionContract(t *testing.T) {
	dashboardDefinition := &dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
		"source": {Title: "Source", Query: dashboardauthoring.TableQuery{Table: "ratings", Fields: []string{"ratings.rating_bucket"}}, Columns: []dashboard.TableColumn{{Key: "rating_bucket", Label: "Rating"}}, Interaction: dashboardauthoring.Interaction{RowSelection: dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{
				Field: "ratings.rating_bucket",
				Fact:  "ratings",
				Value: "rating_bucket",
			}},
			Targets: []string{"tags_per_rating"},
		}}},
		"tags_per_rating": {Title: "Tags", Query: dashboardauthoring.TableQuery{Table: "ratings", Fields: []string{"ratings.rating_bucket"}}, Columns: []dashboard.TableColumn{{Key: "rating_bucket", Label: "Rating"}}},
	})}

	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(definitions["source"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"interactions"`, `"ratings.rating_bucket"`, `"ratings"`, `"tags_per_rating"`} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Fatalf("table payload = %s, want %s", payload, want)
		}
	}
}

func TestGeographicVisualCompilesTiledCoordinateLayers(t *testing.T) {
	dashboardDefinition := &dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
		"detail": {
			Type: "bar", Query: dashboardauthoring.VisualQuery{Table: "orders", Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.state", Alias: "state"}}, Measures: []dashboardauthoring.FieldRef{{Field: "orders.revenue", Alias: "revenue"}}},
		},
		"locations": {
			Type: "map", Title: "Locations", Query: dashboardauthoring.VisualQuery{
				Table: "orders",
				Dimensions: []dashboardauthoring.FieldRef{
					{Field: "orders.state", Alias: "state"},
					{Field: "orders.latitude", Alias: "latitude"},
					{Field: "orders.longitude", Alias: "longitude"},
				},
				Measures: []dashboardauthoring.FieldRef{{Field: "orders.revenue", Alias: "revenue"}},
			},
			Geo: dashboardauthoring.VisualGeo{
				Basemap: "streets", Theme: "auto", LabelDensity: "normal",
				Camera:   dashboardauthoring.VisualGeoCamera{Mode: "fit_data", Padding: 32, MinimumZoom: 2, MaximumZoom: 14},
				Controls: dashboardauthoring.VisualGeoControls{Zoom: true, Reset: true, Compass: true},
				Layers: []dashboardauthoring.VisualGeoLayer{
					{ID: "stores", Kind: "point", Latitude: "latitude", Longitude: "longitude", Value: "revenue", Label: "state", Size: dashboardauthoring.VisualGeoSizeScale{MinimumRadius: 5, MaximumRadius: 28}, Cluster: dashboardauthoring.VisualGeoCluster{Enabled: true, Radius: 48, MaximumZoom: 10, ShowCount: true}},
					{ID: "demand", Kind: "heat", Latitude: "latitude", Longitude: "longitude", Value: "revenue"},
					{ID: "density", Kind: "density", Latitude: "latitude", Longitude: "longitude"},
				}},
			Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
				Toggle: true,
				Mappings: []dashboardauthoring.SelectionMapping{
					{Field: "orders.state", Fact: "orders", Value: "state", Label: "state"},
					{Field: "orders.latitude", Fact: "orders", Value: "latitude", Label: "revenue"},
				},
				Targets: []string{"detail", "summary"},
			}, SpatialSelection: dashboardauthoring.SpatialSelectionInteraction{
				Gestures:  []string{"box", "lasso", "radius"},
				Latitude:  dashboardauthoring.SpatialSelectionMapping{Source: "latitude", Field: "orders.latitude", Fact: "orders"},
				Longitude: dashboardauthoring.SpatialSelectionMapping{Source: "longitude", Field: "orders.longitude", Fact: "orders"},
				Targets:   []string{"detail", "summary"},
			}},
		},
		"summary": {
			Type: "bar", Query: dashboardauthoring.VisualQuery{Table: "orders", Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.state", Alias: "state"}}, Measures: []dashboardauthoring.FieldRef{{Field: "orders.revenue", Alias: "revenue"}}},
		},
	})}

	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition)
	if err != nil {
		t.Fatal(err)
	}
	definition := definitions["locations"]
	if definition.RendererID != "maplibre" {
		t.Fatalf("renderer = %q, want maplibre", definition.RendererID)
	}
	spec, ok := definition.Spec.Value.(*visualizationir.GeographicVisualizationSpec)
	if !ok {
		t.Fatalf("geographic spec = %#v", definition.Spec.Value)
	}
	if got, want := spec.DataBudget.MaxRows, int64(0); got != want {
		t.Fatalf("geographic data budget = %d, want %d", got, want)
	}
	if definition.Query.Kind != visualizationdefinition.QuerySpatial || definition.Query.Spatial == nil {
		t.Fatalf("geographic query binding = %#v, want explicit spatial binding", definition.Query)
	}
	if definition.Query.Spatial.Tiles == nil {
		t.Fatal("coordinate map did not compile tiled delivery")
	}
	tiles := definition.Query.Spatial.Tiles
	if tiles.MinimumZoom != 0 || tiles.MaximumZoom != 18 || tiles.RawMinimumZoom != 5 || tiles.FeatureCap != 5000 || tiles.MaximumBytes != 512*1024 || tiles.MetatileSize != 4 || tiles.CellRadius != 48 {
		t.Fatalf("tile policy = %#v", tiles)
	}
	if got, want := spec.Presentation.Legend, visualizationir.VisualizationLegendPositionHidden; got != want {
		t.Fatalf("geographic legend = %q, want %q", got, want)
	}
	if spec.Presentation.Basemap == nil || spec.Presentation.Basemap.ID != "leapview-streets" || spec.Presentation.Basemap.ArchiveDigest == "" {
		t.Fatalf("geographic basemap = %#v, want content-addressed streets asset", spec.Presentation.Basemap)
	}
	if spec.Presentation.Camera.Mode != visualizationir.VisualizationMapCameraModeFitData || !spec.Presentation.Controls.Reset {
		t.Fatalf("geographic presentation = %#v", spec.Presentation)
	}
	if got, want := len(spec.Layers), 3; got != want {
		t.Fatalf("layers = %d, want %d", got, want)
	}
	for index, want := range []string{
		"point",
		"heat",
		"density",
	} {
		got, err := spec.Layers[index].Kind()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("layer %d kind = %q, want %q", index, got, want)
		}
	}
	point, ok := spec.Layers[0].Value.(*visualizationir.VisualizationPointLayer)
	if !ok || point.Latitude.Field != "latitude" || point.Longitude.Field != "longitude" || !point.Cluster.Enabled || point.Size.MaximumRadius != 28 {
		t.Fatalf("point layer = %#v", spec.Layers[0].Value)
	}
	if got, want := len(spec.Interactions), 1; got != want {
		t.Fatalf("geographic interactions = %d, want %d", got, want)
	}
	interaction := spec.Interactions[0]
	if interaction.Mode != visualizationir.VisualizationSelectionModeMultiple || !interaction.RequiresStableIdentity || len(interaction.Mappings) != 2 {
		t.Fatalf("geographic interaction = %#v", interaction)
	}
	if got := interaction.Targets; len(got) != 3 ||
		got[0].VisualID != "detail" || got[0].Effect != visualizationir.VisualizationInteractionEffectFilter ||
		got[1].VisualID != "locations" || got[1].Effect != visualizationir.VisualizationInteractionEffectNone ||
		got[2].VisualID != "summary" || got[2].Effect != visualizationir.VisualizationInteractionEffectFilter {
		t.Fatalf("geographic targets = %#v", got)
	}
	if got, want := len(spec.SpatialInteractions), 1; got != want {
		t.Fatalf("geographic spatial interactions = %d, want %d", got, want)
	}
	spatial := spec.SpatialInteractions[0]
	if spatial.ID != "spatial_selection" || spatial.Latitude.Source.Field != "latitude" || spatial.Longitude.TargetFieldID != "orders.longitude" || spatial.Longitude.TargetFactID == nil || *spatial.Longitude.TargetFactID != "orders" {
		t.Fatalf("geographic spatial interaction = %#v", spatial)
	}
	if got, want := spatial.Gestures, []visualizationir.VisualizationSpatialSelectionGesture{"box", "lasso", "radius"}; !slices.Equal(got, want) {
		t.Fatalf("geographic spatial gestures = %#v, want %#v", got, want)
	}
	roles := map[string]visualizationir.VisualizationFieldRole{}
	for _, field := range spec.Datasets[0].Fields {
		roles[field.ID] = field.Role
	}
	if roles["state"] != visualizationir.VisualizationFieldRoleIdentity || roles["latitude"] != visualizationir.VisualizationFieldRoleIdentity || roles["revenue"] != visualizationir.VisualizationFieldRoleMeasure {
		t.Fatalf("geographic roles = %#v", roles)
	}
}

func TestGeographicVisualRejectsMixedTiledAndInlineDataLayers(t *testing.T) {
	dashboardDefinition := &dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"locations": {
		Type: "map",
		Query: dashboardauthoring.VisualQuery{Table: "orders", Dimensions: []dashboardauthoring.FieldRef{
			{Field: "orders.state", Alias: "state"},
			{Field: "orders.latitude", Alias: "latitude"},
			{Field: "orders.longitude", Alias: "longitude"},
		}},
		Geo: dashboardauthoring.VisualGeo{Layers: []dashboardauthoring.VisualGeoLayer{
			{ID: "states", Kind: "choropleth", GeometryAsset: "brazil_states", Join: "state"},
			{ID: "stores", Kind: "point", Latitude: "latitude", Longitude: "longitude"},
		}},
	}})}

	_, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition)
	if err == nil || !strings.Contains(err.Error(), "cannot mix tiled point/heat/density layers") {
		t.Fatalf("mixed geographic layers error = %v", err)
	}
}

func TestGeographicVisualRejectsAuthoredRowBudgetsWhenTiled(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*dashboardauthoring.Visual)
		want   string
	}{
		{name: "query limit", mutate: func(visual *dashboardauthoring.Visual) { visual.Query.Limit = 20_000 }, want: "must not set query.limit"},
		{name: "data budget", mutate: func(visual *dashboardauthoring.Visual) { visual.DataBudget.MaxRows = 20_000 }, want: "must not set data_budget.max_rows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			visual := dashboardauthoring.Visual{Type: "map", Query: dashboardauthoring.VisualQuery{Table: "orders", Dimensions: []dashboardauthoring.FieldRef{
				{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"},
			}}, Geo: dashboardauthoring.VisualGeo{Layers: []dashboardauthoring.VisualGeoLayer{{ID: "stores", Kind: "point", Latitude: "latitude", Longitude: "longitude"}}}}
			test.mutate(&visual)
			_, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"locations": visual})})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("row budget error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGeographicVisualCanExplicitlyDisableTheDefaultBasemap(t *testing.T) {
	dashboardDefinition := &dashboardauthoring.Dashboard{SemanticModel: "model", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"locations": {
		Type: "map", Query: dashboardauthoring.VisualQuery{
			Table: "orders", Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.latitude", Alias: "latitude"}, {Field: "orders.longitude", Alias: "longitude"}}, Measures: []dashboardauthoring.FieldRef{{Field: "orders.revenue", Alias: "revenue"}},
		},
		Geo: dashboardauthoring.VisualGeo{Basemap: "blank", Layers: []dashboardauthoring.VisualGeoLayer{{ID: "stores", Kind: "point", Latitude: "latitude", Longitude: "longitude"}}},
	}})}

	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition)
	if err != nil {
		t.Fatal(err)
	}
	spec := definitions["locations"].Spec.Value.(*visualizationir.GeographicVisualizationSpec)
	if spec.Presentation.Basemap != nil {
		t.Fatalf("geographic basemap = %#v, want none", spec.Presentation.Basemap)
	}

	dashboardDefinition.Visuals["locations"] = func() dashboardauthoring.AuthoringVisualization {
		visual := *dashboardDefinition.Visuals["locations"].Chart
		visual.Geo.Basemap = "unknown"
		return dashboardauthoring.ChartVisualization(visual)
	}()
	if _, err := dashboardcompiler.CompileVisualizationDefinitions(dashboardDefinition); err == nil || !strings.Contains(err.Error(), `geographic basemap: unknown map style asset "unknown"`) {
		t.Fatalf("unknown basemap error = %v", err)
	}
}
