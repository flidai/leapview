package compiler

import (
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompileKPIOwnsComparisonGoalAndTrendBindings(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	authored := dashboardauthoring.Visual{
		Type:  "kpi",
		Title: "Revenue",
		Query: dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "revenue"}}},
		Datasets: map[string]dashboardauthoring.VisualQuery{
			"comparison": {Measures: []dashboardauthoring.FieldRef{{Field: "prior_revenue", Alias: "value"}}, Limit: 1},
			"goal":       {Measures: []dashboardauthoring.FieldRef{{Field: "target_revenue", Alias: "value"}}, Limit: 1},
			"trend": {
				Time:     dashboardauthoring.QueryTime{Field: "orders.created_at", Grain: "month", Alias: "period"},
				Measures: []dashboardauthoring.FieldRef{{Field: "revenue", Alias: "value"}},
				Sort:     []dashboardauthoring.Sort{{Field: "period", Direction: "asc"}},
				Limit:    12,
			},
		},
		KPI: dashboardauthoring.VisualKPI{
			Mode:               "bullet",
			Comparison:         &dashboardauthoring.VisualKPIValueBinding{Dataset: "comparison", Field: "value", Reducer: "last", Label: "Previous"},
			Goal:               &dashboardauthoring.VisualKPIValueBinding{Dataset: "goal", Field: "value", Label: "Target"},
			Trend:              &dashboardauthoring.VisualKPITrendBinding{Dataset: "trend", Category: "period", Value: "value"},
			Delta:              "relative",
			FavorableDirection: "increase",
			MissingComparison:  "hide",
			Ranges: []dashboardauthoring.VisualKPIQualitativeRange{{
				Minimum: &minimum, Maximum: &maximum, Label: "On track", Tone: "success",
			}},
		},
	}

	specification, err := compileBuiltInVisualizationSpec("revenue", authored, nil)
	if err != nil {
		t.Fatalf("compileBuiltInVisualizationSpec(): %v", err)
	}
	kpi := specification.Value.(*visualizationir.KPIVisualizationSpec)
	if kpi.Comparison == nil || kpi.Comparison.Field.Dataset != "comparison" || kpi.Comparison.Reducer != visualizationir.VisualizationReferenceReducerLast {
		t.Fatalf("comparison = %#v", kpi.Comparison)
	}
	if kpi.Goal == nil || kpi.Goal.Field.Dataset != "goal" || kpi.Goal.Reducer != visualizationir.VisualizationReferenceReducerFirst {
		t.Fatalf("goal = %#v", kpi.Goal)
	}
	if kpi.Trend == nil || kpi.Trend.Category.Dataset != "trend" || kpi.Trend.Category.Field != "period" || kpi.Trend.Value.Field != "value" {
		t.Fatalf("trend = %#v", kpi.Trend)
	}
	if kpi.Presentation.Mode != visualizationir.VisualizationKPIModeBullet ||
		kpi.Presentation.Delta != visualizationir.VisualizationKPIDeltaModeRelative ||
		kpi.Presentation.FavorableDirection != visualizationir.VisualizationKPIDirectionIncrease ||
		kpi.Presentation.MissingComparison != visualizationir.VisualizationKPIMissingComparisonHide {
		t.Fatalf("presentation = %#v", kpi.Presentation)
	}
	if len(kpi.Presentation.Ranges) != 1 || kpi.Presentation.Ranges[0].Label != "On track" {
		t.Fatalf("ranges = %#v", kpi.Presentation.Ranges)
	}
	if kpi.DataBudget.MaxRows != 12 {
		t.Fatalf("data budget = %d, want 12 to admit the trend dataset", kpi.DataBudget.MaxRows)
	}
}

func TestCompileKPIDefaultsRemainExplicit(t *testing.T) {
	t.Parallel()

	authored := dashboardauthoring.Visual{
		Type:  "kpi",
		Query: dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "revenue"}}},
	}
	specification, err := compileBuiltInVisualizationSpec("revenue", authored, nil)
	if err != nil {
		t.Fatal(err)
	}
	presentation := specification.Value.(*visualizationir.KPIVisualizationSpec).Presentation
	if presentation.Mode != visualizationir.VisualizationKPIModeCompact ||
		presentation.Delta != visualizationir.VisualizationKPIDeltaModeAbsolute ||
		presentation.FavorableDirection != visualizationir.VisualizationKPIDirectionNeutral ||
		presentation.MissingComparison != visualizationir.VisualizationKPIMissingComparisonShowUnavailable ||
		presentation.DisplayUnits == nil || *presentation.DisplayUnits != visualizationir.VisualizationDisplayUnitsAuto {
		t.Fatalf("defaults = %#v", presentation)
	}
}

func TestCompileKPIUsesAuthoredDescriptionForAccessibleContext(t *testing.T) {
	t.Parallel()

	authored := dashboardauthoring.Visual{
		Type:        "kpi",
		Title:       "Revenue",
		Description: "Revenue against the governed baseline.",
		Query:       dashboardauthoring.VisualQuery{Measures: []dashboardauthoring.FieldRef{{Field: "revenue"}}},
	}
	specification, err := compileBuiltInVisualizationSpec("revenue", authored, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := specification.Value.(*visualizationir.KPIVisualizationSpec).Accessibility.Description; got != authored.Description {
		t.Fatalf("accessibility description = %q, want %q", got, authored.Description)
	}
}
