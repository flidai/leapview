package authoring

import (
	"strings"
	"testing"
)

func TestScatterRequiresGovernedPointBindings(t *testing.T) {
	t.Parallel()

	valid := Visual{
		Type: "scatter",
		Query: VisualQuery{
			Dimensions: []FieldRef{
				{Field: "orders.id", Alias: "order_id"},
				{Field: "orders.segment", Alias: "segment"},
				{Field: "orders.customer", Alias: "customer"},
			},
			Measures: []FieldRef{
				{Field: "orders.delivery_days", Alias: "delivery_days"},
				{Field: "orders.revenue", Alias: "revenue"},
				{Field: "orders.quantity", Alias: "quantity"},
			},
		},
		Point: VisualPoint{
			Identity: []string{"order_id"},
			X:        "delivery_days",
			Y:        "revenue",
			Size:     "quantity",
			Color:    "segment",
			Label:    "customer",
			Tooltip:  []string{"customer", "delivery_days", "revenue"},
			ColorScale: VisualPointColorScale{
				Kind: "categorical",
			},
			Overplot: VisualPointOverplot{
				Strategy:       "opacity",
				Opacity:        0.55,
				LargeMode:      "automatic",
				LargeThreshold: 2_000,
			},
			Brush: []string{"rectangle", "lasso"},
		},
		Interaction: Interaction{PointSelection: SelectionInteraction{
			Mappings: []SelectionMapping{{Value: "order_id"}},
			Targets:  []string{"detail"},
		}},
	}

	if err := validateVisualQueryShape("orders", valid); err != nil {
		t.Fatalf("validateVisualQueryShape() error = %v", err)
	}
	if err := validatePointVisual("orders", valid); err != nil {
		t.Fatalf("validatePointVisual() error = %v", err)
	}
}

func TestScatterPointBindingsRejectUnstableOrInvalidChannels(t *testing.T) {
	t.Parallel()

	base := Visual{
		Type: "scatter",
		Query: VisualQuery{
			Dimensions: []FieldRef{{Field: "orders.id", Alias: "order_id"}},
			Measures: []FieldRef{
				{Field: "orders.delivery_days", Alias: "delivery_days"},
				{Field: "orders.revenue", Alias: "revenue"},
			},
		},
		Point: VisualPoint{
			Identity: []string{"order_id"},
			X:        "delivery_days",
			Y:        "revenue",
		},
	}

	tests := []struct {
		name string
		edit func(*Visual)
		want string
	}{
		{
			name: "missing identity",
			edit: func(visual *Visual) { visual.Point.Identity = nil },
			want: "requires point.identity",
		},
		{
			name: "measure identity",
			edit: func(visual *Visual) { visual.Point.Identity = []string{"revenue"} },
			want: "identity field \"revenue\" must reference a dimension or time alias",
		},
		{
			name: "unknown x",
			edit: func(visual *Visual) { visual.Point.X = "unknown" },
			want: "x references unknown query alias",
		},
		{
			name: "dimension y",
			edit: func(visual *Visual) { visual.Point.Y = "order_id" },
			want: "y field \"order_id\" must reference a measure",
		},
		{
			name: "dimension size",
			edit: func(visual *Visual) { visual.Point.Size = "order_id" },
			want: "size field \"order_id\" must reference a measure",
		},
		{
			name: "measure series",
			edit: func(visual *Visual) { visual.Point.Series = "revenue" },
			want: "series field \"revenue\" must reference a dimension or time alias",
		},
		{
			name: "duplicate tooltip",
			edit: func(visual *Visual) { visual.Point.Tooltip = []string{"revenue", "revenue"} },
			want: "duplicate point.tooltip field",
		},
		{
			name: "unknown brush",
			edit: func(visual *Visual) { visual.Point.Brush = []string{"circle"} },
			want: "unsupported point.brush gesture",
		},
		{
			name: "brush without selection",
			edit: func(visual *Visual) { visual.Point.Brush = []string{"rectangle"} },
			want: "point.brush requires point_selection",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			visual := base
			visual.Point.Identity = append([]string(nil), base.Point.Identity...)
			test.edit(&visual)
			err := validatePointVisual("orders", visual)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePointVisual() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestScatterSupportsTimeVersusValue(t *testing.T) {
	t.Parallel()

	visual := Visual{
		Type: "scatter",
		Query: VisualQuery{
			Dimensions: []FieldRef{{Field: "orders.id", Alias: "order_id"}},
			Time:       QueryTime{Field: "orders.created_at", Grain: "day", Alias: "created_at"},
			Measures:   []FieldRef{{Field: "orders.revenue", Alias: "revenue"}},
		},
		Point: VisualPoint{
			Identity: []string{"order_id"},
			X:        "created_at",
			Y:        "revenue",
		},
	}

	if err := validateVisualQueryShape("orders", visual); err != nil {
		t.Fatalf("validateVisualQueryShape() error = %v", err)
	}
	if err := validatePointVisual("orders", visual); err != nil {
		t.Fatalf("validatePointVisual() error = %v", err)
	}
}
