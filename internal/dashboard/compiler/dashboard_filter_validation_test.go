package compiler

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestValidateDashboardPreservesDatasetOnLocalFilterForMultiDatasetTarget(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "model",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"ratings": {Model: "ratings"},
			"tags":    {Model: "tags"},
		},
		Tables: map[string]semanticmodel.Table{
			"ratings": {
				ModelName:   "ratings",
				GrainEntity: "rating_bucket",
				Entities: map[string]semanticmodel.EntityDefinition{
					"rating_bucket": {Type: "primary", Fields: []string{"rating_bucket"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"rating_bucket": {Field: "ratings.rating_bucket", Type: "number", Datatype: semanticmodel.DataTypeFloat},
				},
			},
			"tags": {
				ModelName:   "tags",
				GrainEntity: "tag_id",
				Entities: map[string]semanticmodel.EntityDefinition{
					"tag_id": {Type: "primary", Fields: []string{"tag_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"tag_id": {Field: "tags.tag_id", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"rating_count":    {Type: "aggregate", Dataset: "ratings", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "ratings.rating_bucket"}, Empty: "zero"},
			"tag_count":       {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}, Empty: "zero"},
			"tags_per_rating": {Type: "ratio", Numerator: "tag_count", Denominator: "rating_count"},
		},
	}
	dashboardDefinition := &dashboardauthoring.Dashboard{
		ID: "dashboard", Title: "Dashboard", SemanticModel: "model",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"rating_bucket": {
				Label: "Rating", Field: "ratings.rating_bucket", Dataset: "ratings",
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
				}},
			},
		},
		FilterBindings: map[string]dashboardfilter.Binding{"rating_bucket": {Filter: "rating_bucket"}},
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"target": {
				Type:  "kpi",
				Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "tags_per_rating"}}},
			},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview"}},
	}

	if _, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model}); err != nil {
		t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
	}
}
