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
			"tags": {},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"rating_count": {Fact: "ratings", Aggregation: "count", Empty: "zero"},
			"tag_count":    {Fact: "tags", Aggregation: "count", Empty: "zero"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"tags_per_rating": {Expression: "safe_divide(${tag_count}, ${rating_count})"},
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
				Query: dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "tags_per_rating"}}},
			},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview"}},
	}

	if _, err := ValidateAndNormalizeDashboard(dashboardDefinition, map[string]*semanticmodel.Model{"model": model}); err != nil {
		t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
	}
}
