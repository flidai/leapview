package authoring

import (
	"strings"
	"testing"
)

func TestValidateVisualAcceptsGovernedKPIComparison(t *testing.T) {
	t.Parallel()

	visual := Visual{
		Type:  "kpi",
		Query: VisualQuery{Measures: []FieldRef{{Field: "revenue", Alias: "value"}}},
		Datasets: map[string]VisualQuery{
			"comparison": {Measures: []FieldRef{{Field: "prior_revenue", Alias: "value"}}, Limit: 1},
			"goal":       {Measures: []FieldRef{{Field: "revenue_goal", Alias: "value"}}, Limit: 1},
			"trend": {
				Time:     QueryTime{Field: "orders.month", Alias: "period", Grain: "month"},
				Measures: []FieldRef{{Field: "revenue", Alias: "value"}},
				Sort:     []Sort{{Field: "period", Direction: "asc"}},
				Limit:    12,
			},
		},
		KPI: VisualKPI{
			Mode:       "bullet",
			Comparison: &VisualKPIValueBinding{Dataset: "comparison", Field: "value", Reducer: "first", Label: "Previous"},
			Goal:       &VisualKPIValueBinding{Dataset: "goal", Field: "value", Reducer: "first", Label: "Target"},
			Trend:      &VisualKPITrendBinding{Dataset: "trend", Category: "period", Value: "value"},
			Delta:      "relative", FavorableDirection: "increase", MissingComparison: "show_unavailable",
			Ranges: []VisualKPIQualitativeRange{
				{Maximum: floatPointer(80), Label: "Needs attention", Tone: "danger"},
				{Minimum: floatPointer(80), Maximum: floatPointer(100), Label: "On track", Tone: "warning"},
				{Minimum: floatPointer(100), Label: "Above goal", Tone: "success"},
			},
		},
	}
	if err := validateVisualPresentation("revenue", visual); err != nil {
		t.Fatalf("validateVisualPresentation(): %v", err)
	}
}

func TestValidateVisualRejectsAmbiguousKPIComparison(t *testing.T) {
	t.Parallel()

	valid := func() Visual {
		return Visual{
			Type: "kpi",
			Datasets: map[string]VisualQuery{
				"context": {Measures: []FieldRef{{Field: "prior", Alias: "value"}}, Limit: 1},
			},
			KPI: VisualKPI{
				Mode: "compact", Delta: "absolute", FavorableDirection: "increase", MissingComparison: "show_unavailable",
				Comparison: &VisualKPIValueBinding{Dataset: "context", Field: "value", Reducer: "first"},
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Visual)
		want   string
	}{
		{"bullet without goal", func(visual *Visual) { visual.KPI.Mode = "bullet" }, "requires an explicit goal"},
		{"implicit direction", func(visual *Visual) { visual.KPI.FavorableDirection = "" }, "requires favorable_direction"},
		{"unknown dataset", func(visual *Visual) { visual.KPI.Comparison.Dataset = "deleted" }, `references unknown dataset "deleted"`},
		{"unknown field", func(visual *Visual) { visual.KPI.Comparison.Field = "deleted" }, `references unknown field "deleted"`},
		{"invalid reducer", func(visual *Visual) { visual.KPI.Comparison.Reducer = "sum" }, `unsupported reducer "sum"`},
		{"unsorted trend", func(visual *Visual) {
			visual.Datasets["trend"] = VisualQuery{
				Time: QueryTime{Field: "orders.month", Alias: "period", Grain: "month"}, Measures: []FieldRef{{Field: "revenue", Alias: "value"}}, Limit: 12,
			}
			visual.KPI.Trend = &VisualKPITrendBinding{Dataset: "trend", Category: "period", Value: "value"}
		}, "must sort by category field"},
		{"trend requires history", func(visual *Visual) {
			visual.KPI.Trend = &VisualKPITrendBinding{Dataset: "context", Category: "period", Value: "value"}
		}, "trend dataset must have limit greater than one"},
		{"kpi block on chart", func(visual *Visual) { visual.Type = "line" }, "kpi configuration is only valid for type kpi"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visual := valid()
			test.mutate(&visual)
			err := validateVisualPresentation("revenue", visual)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func floatPointer(value float64) *float64 { return &value }
