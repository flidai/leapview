package compiler

import (
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompiledScatterOwnsTruePointChannelsAndStableIdentity(t *testing.T) {
	t.Parallel()

	authored := dashboardauthoring.Visual{
		Type: "scatter",
		Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{
				{Field: "orders.id", Alias: "order_id"},
				{Field: "orders.segment", Alias: "segment"},
			},
			Metrics: []dashboardauthoring.FieldRef{
				{Field: "orders.delivery_days", Alias: "delivery_days"},
				{Field: "orders.revenue", Alias: "revenue"},
				{Field: "orders.quantity", Alias: "quantity"},
			},
		},
		Point: dashboardauthoring.VisualPoint{
			Identity: []string{"order_id"}, X: "delivery_days", Y: "revenue", Size: "quantity", Color: "segment",
			Tooltip: []string{"segment", "revenue"},
			Brush:   []string{"rectangle", "lasso"},
		},
		Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "orders.id", Value: "order_id"}},
			Targets:  []string{"detail"},
		}},
	}

	spec, err := compileBuiltInVisualizationSpec("orders", authored, nil)
	if err != nil {
		t.Fatalf("compileBuiltInVisualizationSpec() error = %v", err)
	}
	point, ok := spec.Value.(*visualizationir.PointVisualizationSpec)
	if !ok {
		t.Fatalf("spec = %T, want PointVisualizationSpec", spec.Value)
	}
	if point.X.Field != "delivery_days" || point.Y.Field != "revenue" || point.Size == nil || point.Size.Field != "quantity" {
		t.Fatalf("point channels = %#v", point)
	}
	if len(point.Identity) != 1 || point.Identity[0].Field != "order_id" {
		t.Fatalf("identity = %#v", point.Identity)
	}
	if got := point.Datasets[0].Fields[0].Role; got != visualizationir.VisualizationFieldRoleIdentity {
		t.Fatalf("order_id role = %q, want identity", got)
	}
	if point.ColorScale == nil || point.ColorScale.Kind != visualizationir.VisualizationPointColorScaleKindCategorical {
		t.Fatalf("color scale = %#v, want categorical", point.ColorScale)
	}
	if point.SizeScale == nil || point.SizeScale.MinimumPixels != 8 || point.SizeScale.MaximumPixels != 40 {
		t.Fatalf("size scale = %#v, want default 8..40", point.SizeScale)
	}
	if len(point.Presentation.Brush) != 2 || point.Presentation.LargeThreshold != 2_000 {
		t.Fatalf("presentation = %#v", point.Presentation)
	}
	if err := visualizationir.ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() error = %v", err)
	}
}
