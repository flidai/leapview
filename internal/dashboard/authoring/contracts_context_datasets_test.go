package authoring

import (
	"strings"
	"testing"
)

func TestValidateVisualAcceptsFilteredContextDatasetsAndMetadataBindings(t *testing.T) {
	t.Parallel()

	visual := Visual{
		Type: "line",
		Query: VisualQuery{
			Dimensions: []FieldRef{{Field: "orders.month", Alias: "month"}},
			Measures:   []FieldRef{{Field: "revenue", Alias: "revenue"}},
		},
		Datasets: map[string]VisualQuery{
			"context": {
				Dimensions: []FieldRef{{Field: "orders.region", Alias: "region"}},
				Measures:   []FieldRef{{Field: "target_revenue", Alias: "target"}},
				Limit:      1,
			},
		},
		Metadata: VisualMetadataBindings{
			Title:       &VisualTextBinding{Dataset: "context", Field: "region", Reducer: "first", Prefix: "Revenue — ", Fallback: "Revenue"},
			Description: &VisualTextBinding{Dataset: "context", Field: "target", Reducer: "mean", Prefix: "Target: ", Fallback: "Target unavailable"},
		},
		Presentation: VisualPresentation{
			ReferenceLines: []VisualReferenceLine{{
				ID: "target", Axis: "primary_y",
				Value: VisualReferenceValue{Dataset: "context", Field: "target", Reducer: "mean"},
			}},
		},
	}

	if err := validateVisualPresentation("revenue", visual); err != nil {
		t.Fatalf("validateVisualPresentation() error = %v", err)
	}
}

func TestValidateVisualRejectsUnsafeOrAmbiguousContextBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		visual Visual
		want   string
	}{
		{
			name: "reserved dataset",
			visual: Visual{Type: "line", Datasets: map[string]VisualQuery{
				"primary": {Measures: []FieldRef{{Field: "target", Alias: "target"}}},
			}},
			want: `dataset id "primary" is reserved`,
		},
		{
			name: "empty context query",
			visual: Visual{Type: "line", Datasets: map[string]VisualQuery{
				"context": {},
			}},
			want: `dataset "context" requires dimensions, time, or measures`,
		},
		{
			name: "map data-bound metadata",
			visual: Visual{Type: "map", Metadata: VisualMetadataBindings{
				Title: &VisualTextBinding{Field: "region", Reducer: "first", Fallback: "Map"},
			}},
			want: "does not support context datasets or data-bound metadata",
		},
		{
			name: "unknown metadata dataset",
			visual: Visual{Type: "line", Metadata: VisualMetadataBindings{
				Title: &VisualTextBinding{Dataset: "deleted", Field: "region", Reducer: "first"},
			}},
			want: `metadata title references unknown dataset "deleted"`,
		},
		{
			name: "unsupported reducer",
			visual: Visual{Type: "line", Metadata: VisualMetadataBindings{
				Title: &VisualTextBinding{Dataset: "primary", Field: "label", Reducer: "concatenate"},
			}},
			want: `metadata title has unsupported reducer "concatenate"`,
		},
		{
			name: "reference dataset without field",
			visual: Visual{Type: "line", Presentation: VisualPresentation{
				ReferenceLines: []VisualReferenceLine{{
					ID: "target", Axis: "primary_y", Value: VisualReferenceValue{Dataset: "context"},
				}},
			}},
			want: "reference value dataset requires field",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVisualPresentation("revenue", test.visual)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
