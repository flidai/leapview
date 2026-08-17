package compiler

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestValidateDashboardPreservesFactOnLocalFilterForMultiFactTarget(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "model",
		Tables: map[string]semanticmodel.Table{
			"ratings": {Dimensions: map[string]semanticmodel.MetricDimension{
				"rating_bucket": {Type: "number"},
			}},
			"tags": {Dimensions: map[string]semanticmodel.MetricDimension{"tag_id": {Type: "string"}}},
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
				Label: "Rating", Field: "ratings.rating_bucket", Fact: "ratings",
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
