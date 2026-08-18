package authoring

import (
	"strings"
	"testing"
)

func TestValidateVisualPresentationAcceptsCartesianDecisionContext(t *testing.T) {
	t.Parallel()

	minimum := 0.0
	maximum := 100.0
	target := 80.0
	visual := Visual{
		Type: "line",
		Presentation: VisualPresentation{
			Axes: []VisualAxis{
				{ID: "x", Title: "Month", TickDensity: "sparse"},
				{ID: "primary_y", Title: "Revenue", Scale: "linear", Zero: "include", Minimum: &minimum, Maximum: &maximum, Unit: "USD"},
			},
			ReferenceLines: []VisualReferenceLine{{
				ID: "target", Axis: "primary_y", Value: VisualReferenceValue{Number: &target}, Label: "Target", Tone: "success",
			}},
			ReferenceBands: []VisualReferenceBand{{
				ID: "healthy", Axis: "primary_y",
				From:  VisualReferenceValue{Number: numberPointer(70)},
				To:    VisualReferenceValue{Number: numberPointer(90)},
				Label: "Healthy range", Tone: "success",
			}},
			EventAnnotations: []VisualEventAnnotation{{
				ID: "launch", Axis: "x", Value: VisualReferenceValue{Text: "2026-03-01"}, Label: "Launch", Description: "New pricing launched",
			}},
			Tooltip: []string{"month", "revenue"},
		},
	}

	if err := validateVisualPresentation("revenue", visual); err != nil {
		t.Fatalf("validateVisualPresentation() error = %v", err)
	}
}

func TestValidateVisualPresentationRejectsInvalidDecisionContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		visualType   string
		presentation VisualPresentation
		want         string
	}{
		{
			name: "unsupported visual", visualType: "pie",
			presentation: VisualPresentation{Axes: []VisualAxis{{ID: "x"}}},
			want:         "only valid for cartesian",
		},
		{
			name: "duplicate axis", visualType: "line",
			presentation: VisualPresentation{Axes: []VisualAxis{{ID: "x"}, {ID: "x"}}},
			want:         `duplicate axis "x"`,
		},
		{
			name: "invalid log zero policy", visualType: "line",
			presentation: VisualPresentation{Axes: []VisualAxis{{ID: "primary_y", Scale: "log", Zero: "include"}}},
			want:         "log scale cannot include zero",
		},
		{
			name: "inverted domain", visualType: "line",
			presentation: VisualPresentation{Axes: []VisualAxis{{ID: "primary_y", Minimum: numberPointer(10), Maximum: numberPointer(5)}}},
			want:         "minimum must be less than maximum",
		},
		{
			name: "ambiguous value", visualType: "line",
			presentation: VisualPresentation{ReferenceLines: []VisualReferenceLine{{
				ID: "target", Axis: "primary_y", Value: VisualReferenceValue{Number: numberPointer(10), Field: "target"},
			}}},
			want: "requires exactly one",
		},
		{
			name: "duplicate annotation identity", visualType: "line",
			presentation: VisualPresentation{
				ReferenceLines: []VisualReferenceLine{{ID: "target", Axis: "primary_y", Value: VisualReferenceValue{Number: numberPointer(10)}}},
				ReferenceBands: []VisualReferenceBand{{ID: "target", Axis: "primary_y", From: VisualReferenceValue{Number: numberPointer(5)}, To: VisualReferenceValue{Number: numberPointer(15)}}},
			},
			want: `duplicate decision context ID "target"`,
		},
		{
			name: "event on value axis", visualType: "line",
			presentation: VisualPresentation{EventAnnotations: []VisualEventAnnotation{{
				ID: "launch", Axis: "primary_y", Value: VisualReferenceValue{Text: "2026-03-01"},
			}}},
			want: "event annotation axis must be x",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVisualPresentation("visual", Visual{Type: test.visualType, Presentation: test.presentation})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateVisualPresentationAcceptsDeterministicSeriesIntent(t *testing.T) {
	t.Parallel()

	visual := Visual{
		Type: "area",
		Query: VisualQuery{
			Series:  FieldRef{Field: "orders.status", Alias: "status"},
			Metrics: []FieldRef{{Field: "revenue", Alias: "revenue"}},
		},
		Presentation: VisualPresentation{
			Stacking:     "percent",
			SeriesOrder:  []string{"delivered", "processing", "canceled"},
			SeriesColors: map[string]string{"delivered": "success", "processing": "data_3", "canceled": "danger"},
		},
	}

	if err := validateVisualPresentation("revenue", visual); err != nil {
		t.Fatalf("validateVisualPresentation() error = %v", err)
	}
}

func TestValidateVisualPresentationRejectsInvalidSeriesIntent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		visual Visual
		want   string
	}{
		{
			name:   "legacy conflict",
			visual: Visual{Type: "line", Presentation: VisualPresentation{Stacked: true, Stacking: "normal"}},
			want:   "cannot combine presentation.stacked and presentation.stacking",
		},
		{
			name:   "percent without series",
			visual: Visual{Type: "area", Query: VisualQuery{Metrics: []FieldRef{{Field: "revenue"}}}, Presentation: VisualPresentation{Stacking: "percent"}},
			want:   "percent stacking requires a series or multiple metrics",
		},
		{
			name:   "unsupported mark",
			visual: Visual{Type: "scatter", Query: VisualQuery{Series: FieldRef{Field: "status"}}, Presentation: VisualPresentation{Stacking: "normal"}},
			want:   "stacking is unsupported",
		},
		{
			name:   "percent with dual axes",
			visual: Visual{Type: "combo", Query: VisualQuery{Series: FieldRef{Field: "status"}}, Presentation: VisualPresentation{Stacking: "percent", DualAxis: true}},
			want:   "percent stacking cannot use dual axes",
		},
		{
			name:   "duplicate order",
			visual: Visual{Type: "line", Query: VisualQuery{Series: FieldRef{Field: "status"}}, Presentation: VisualPresentation{SeriesOrder: []string{"a", "a"}}},
			want:   `duplicate series order value "a"`,
		},
		{
			name:   "unsupported color",
			visual: Visual{Type: "line", Query: VisualQuery{Series: FieldRef{Field: "status"}}, Presentation: VisualPresentation{SeriesColors: map[string]string{"a": "#ff0000"}}},
			want:   `unsupported color intent "#ff0000"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVisualPresentation("visual", test.visual)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func numberPointer(value float64) *float64 {
	return &value
}
