package compiler

import (
	"fmt"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestValidateDashboardUsesSemanticSelectionResolver(t *testing.T) {
	tests := []struct {
		name    string
		mapping dashboardauthoring.SelectionMapping
		wantErr string
	}{
		{name: "conformed", mapping: dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "label"}},
		{name: "physical requires dataset", mapping: dashboardauthoring.SelectionMapping{Field: "ratings.release_decade", Value: "label"}, wantErr: `physical field "ratings.release_decade" requires dataset`},
		{name: "semantic forbids dataset", mapping: dashboardauthoring.SelectionMapping{Field: "release_decade", Dataset: "ratings", Value: "label"}, wantErr: `semantic dimension "release_decade" must not specify dataset`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dashboardDefinition, model := compilerSelectionFixture(test.mapping)
			normalized, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model})
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
				}
				dashboardDefinition = normalized
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ValidateAndNormalizeDashboard() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateDashboardRejectsPointSelectionForAggregateRadarPolygons(t *testing.T) {
	for _, visualType := range []string{"radar"} {
		t.Run(visualType, func(t *testing.T) {
			dashboardDefinition, model := compilerSelectionFixture(dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "source"})
			source := *dashboardDefinition.Visuals["source"].Chart
			source.Type = visualType
			source.Query.Dimensions = []dashboardauthoring.FieldRef{
				{Field: "release_decade", Alias: "source"},
				{Field: "release_decade_target", Alias: "target"},
			}
			if visualType == "radar" {
				source.Query.Dimensions = source.Query.Dimensions[:1]
			}
			dashboardDefinition.Visuals["source"] = dashboardauthoring.ChartVisualization(source)

			_, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model})
			want := fmt.Sprintf(`visual "source" type %q shape %q does not support point_selection`, visualType, source.ResultShape())
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateAndNormalizeDashboard() error = %v, want containing %q", err, want)
			}
		})
	}
}

func TestValidateDashboardAllowsSelectionFromHierarchyNodesAndNetworkLinks(t *testing.T) {
	for _, visualType := range []string{"graph", "sankey", "tree", "treemap", "sunburst"} {
		t.Run(visualType, func(t *testing.T) {
			dashboardDefinition, model := compilerSelectionFixture(dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "source"})
			source := *dashboardDefinition.Visuals["source"].Chart
			source.Type = visualType
			source.Query.Dimensions = []dashboardauthoring.FieldRef{
				{Field: "release_decade", Alias: "source"},
				{Field: "release_decade_target", Alias: "target"},
			}
			dashboardDefinition.Visuals["source"] = dashboardauthoring.ChartVisualization(source)

			normalized, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model})
			if err != nil {
				t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
			}
			dashboardDefinition = normalized
			definitions, err := CompileVisualizationDefinitions(dashboardDefinition, model)
			if err != nil {
				t.Fatal(err)
			}
			base, err := visualizationir.SpecificationBase(definitions["source"].Spec)
			if err != nil {
				t.Fatal(err)
			}
			roles := map[string]string{}
			for _, field := range base.Datasets[0].Fields {
				roles[field.ID] = string(field.Role)
			}
			if roles["source"] != "identity" {
				t.Fatalf("compiled field roles = %#v, want source identity", roles)
			}
		})
	}
}

func TestCompileDashboardRequiresHighlightTargetsToExposeMappedFieldsUnlessTheyAreKPIs(t *testing.T) {
	dashboardDefinition, model := compilerSelectionFixture(dashboardauthoring.SelectionMapping{Field: "release_decade", Value: "label"})
	source := *dashboardDefinition.Visuals["source"].Chart
	source.Interaction.PointSelection.Targets = nil
	source.Interaction.PointSelection.HighlightTargets = []string{"target"}
	dashboardDefinition.Visuals["source"] = dashboardauthoring.ChartVisualization(source)

	target := *dashboardDefinition.Visuals["target"].Chart
	target.Query.Dimensions = nil
	dashboardDefinition.Visuals["target"] = dashboardauthoring.ChartVisualization(target)
	if _, err := CompileVisualizationDefinitions(dashboardDefinition, model); err == nil || !strings.Contains(err.Error(), `does not expose mapped field "release_decade"`) {
		t.Fatalf("incompatible highlight target error = %v", err)
	}

	target.Type = "kpi"
	target.Query.Metrics = target.Query.Metrics[:1]
	dashboardDefinition.Visuals["target"] = dashboardauthoring.ChartVisualization(target)
	if _, err := CompileVisualizationDefinitions(dashboardDefinition, model); err != nil {
		t.Fatalf("KPI comparison-context highlight error = %v", err)
	}
}

func TestValidateDashboardResolvesNumericSpatialSelectionCoordinates(t *testing.T) {
	dashboardDefinition, model := compilerSpatialSelectionFixture()
	if _, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model}); err != nil {
		t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
	}

	source := *dashboardDefinition.Visuals["source"].Chart
	source.Interaction.SpatialSelection.Latitude.Field = "ratings.release_decade"
	dashboardDefinition.Visuals["source"] = dashboardauthoring.ChartVisualization(source)
	if _, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model}); err == nil || !strings.Contains(err.Error(), `field "ratings.release_decade" must be numeric`) {
		t.Fatalf("nonnumeric spatial coordinate error = %v", err)
	}
}

func compilerSpatialSelectionFixture() (*dashboardauthoring.Dashboard, *semanticmodel.Model) {
	model := &semanticmodel.Model{
		Name:     "model",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"ratings": {Model: "ratings"}},
		Tables: map[string]semanticmodel.Table{"ratings": {
			ModelName:   "ratings",
			GrainEntity: "release_decade",
			Entities: map[string]semanticmodel.ModelEntitySpec{
				"release_decade": {Type: "primary", Fields: []string{"release_decade"}},
			},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"latitude":       {Field: "ratings.latitude", Type: "number", Datatype: semanticmodel.DataTypeFloat},
				"longitude":      {Field: "ratings.longitude", Type: "number", Datatype: semanticmodel.DataTypeFloat},
				"release_decade": {Field: "ratings.release_decade", Type: "string", Datatype: semanticmodel.DataTypeString},
			},
		}},
		Metrics: map[string]semanticmodel.Metric{"rating_count": {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "ratings.release_decade"}, Empty: "zero"}},
	}
	source := dashboardauthoring.Visual{
		Title: "Source", Type: "map",
		Query: dashboardauthoring.VisualQuery{Dataset: "ratings", Dimensions: []dashboardauthoring.FieldRef{
			{Field: "ratings.latitude", Alias: "latitude"}, {Field: "ratings.longitude", Alias: "longitude"},
		}, Metrics: []dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}}, Limit: 100},
		Geo: dashboardauthoring.VisualGeo{Layers: []dashboardauthoring.VisualGeoLayer{{ID: "density", Kind: "density", Latitude: "latitude", Longitude: "longitude", Value: "value"}}},
		Interaction: dashboardauthoring.Interaction{SpatialSelection: dashboardauthoring.SpatialSelectionInteraction{
			Gestures:  []string{"box", "lasso", "radius"},
			Latitude:  dashboardauthoring.SpatialSelectionMapping{Source: "latitude", Field: "ratings.latitude", Dataset: "ratings"},
			Longitude: dashboardauthoring.SpatialSelectionMapping{Source: "longitude", Field: "ratings.longitude", Dataset: "ratings"},
			Targets:   []string{"target"},
		}},
	}
	target := dashboardauthoring.Visual{Title: "Target", Type: "kpi", Query: dashboardauthoring.VisualQuery{Dataset: "ratings", Metrics: []dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}}, Limit: 1}}
	return &dashboardauthoring.Dashboard{
		ID: "dashboard", Title: "Dashboard", SemanticModel: "model",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"source": source, "target": target}),
		Pages:   []dashboard.Page{{ID: "overview", Title: "Overview"}},
	}, model
}

func compilerSelectionFixture(mapping dashboardauthoring.SelectionMapping) (*dashboardauthoring.Dashboard, *semanticmodel.Model) {
	model := &semanticmodel.Model{
		Name: "model",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"ratings": {Model: "ratings"},
			"tags":    {Model: "tags"},
		},
		Tables: map[string]semanticmodel.Table{
			"ratings": {
				ModelName:   "ratings",
				GrainEntity: "release_decade",
				Entities: map[string]semanticmodel.ModelEntitySpec{
					"release_decade": {Type: "primary", Fields: []string{"release_decade"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"release_decade": {Field: "ratings.release_decade", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"tags": {
				ModelName:   "tags",
				GrainEntity: "release_decade",
				Entities: map[string]semanticmodel.ModelEntitySpec{
					"release_decade": {Type: "primary", Fields: []string{"release_decade"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"release_decade": {Field: "tags.release_decade", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"ratings": {Field: "ratings.release_decade"},
				"tags":    {Field: "tags.release_decade"},
			}},
			"release_decade_target": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"ratings": {Field: "ratings.release_decade"},
				"tags":    {Field: "tags.release_decade"},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"rating_count": {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "ratings.release_decade"}, Empty: "zero"},
			"tag_count":    {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.release_decade"}, Empty: "zero"},
		},
	}
	source := dashboardauthoring.Visual{
		Title: "Source", Type: "bar",
		Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: mapping.Field, Alias: "label"}},
			Metrics:    []dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		},
		Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{mapping}, Targets: []string{"target"},
		}},
	}
	target := dashboardauthoring.Visual{
		Title: "Target", Type: "combo",
		Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "release_decade", Alias: "label"}},
			Metrics: []dashboardauthoring.FieldRef{
				{Field: "rating_count", Alias: "rating_count"},
				{Field: "tag_count", Alias: "tag_count"},
			},
		},
	}
	return &dashboardauthoring.Dashboard{
		ID: "dashboard", Title: "Dashboard", SemanticModel: "model",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"source": source, "target": target}),
		Pages:   []dashboard.Page{{ID: "overview", Title: "Overview"}},
	}, model
}
