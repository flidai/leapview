package ir

import (
	"strconv"
	"strings"
	"testing"
)

func TestValidateSpecAcceptsHeatmapDataZoom(t *testing.T) {
	t.Parallel()

	spec := VisualizationSpec{Value: &CartesianVisualizationSpec{
		VisualizationSpecBase: VisualizationSpecBase{
			Kind: "cartesian", Title: "Status heatmap",
			Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
				{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
				{ID: "status", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Status"},
				{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
			}}},
			DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
			Accessibility: VisualizationAccessibility{Title: "Status heatmap", Description: "Revenue by status and month"},
			Interactions:  []VisualizationInteraction{},
		},
		Kind: "cartesian", Mark: VisualizationCartesianMarkHeatmap,
		X: VisualizationFieldRef{Dataset: "primary", Field: "status"},
		Y: []VisualizationFieldRef{{Dataset: "primary", Field: "month"}, {Dataset: "primary", Field: "revenue"}},
		Presentation: CartesianVisualizationPresentation{
			VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden),
			DataZoom:                  true,
		},
	}}

	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("heatmap data zoom rejected by IR validation: %v", err)
	}
}

func TestValidateSpecRejectsAxisVisibleOutsideCartesianAndPoint(t *testing.T) {
	base := VisualizationSpecBase{Kind: "proportional", Title: "Test", Accessibility: VisualizationAccessibility{Title: "Test", Description: "Test"}}
	for _, axisVisible := range []bool{false, true} {
		axisVisible := axisVisible
		tests := []struct {
			name string
			kind string
			spec VisualizationSpec
		}{
			{name: "proportional", kind: "proportional", spec: VisualizationSpec{Value: &ProportionalVisualizationSpec{VisualizationSpecBase: base, Kind: "proportional", Presentation: ProportionalVisualizationPresentation{VisualizationPresentation: VisualizationPresentation{AxisVisible: &axisVisible}}}}},
			{name: "hierarchy", kind: "hierarchy", spec: VisualizationSpec{Value: &HierarchyVisualizationSpec{VisualizationSpecBase: VisualizationSpecBase{Kind: "hierarchy", Title: "Test", Accessibility: VisualizationAccessibility{Title: "Test", Description: "Test"}}, Kind: "hierarchy", Presentation: HierarchyVisualizationPresentation{VisualizationPresentation: VisualizationPresentation{AxisVisible: &axisVisible}}}}},
			{name: "polar", kind: "polar", spec: VisualizationSpec{Value: &PolarVisualizationSpec{VisualizationSpecBase: VisualizationSpecBase{Kind: "polar", Title: "Test", Accessibility: VisualizationAccessibility{Title: "Test", Description: "Test"}}, Kind: "polar", Presentation: PolarVisualizationPresentation{VisualizationPresentation: VisualizationPresentation{AxisVisible: &axisVisible}}}}},
			{name: "geographic", kind: "geographic", spec: VisualizationSpec{Value: &GeographicVisualizationSpec{VisualizationSpecBase: VisualizationSpecBase{Kind: "geographic", Title: "Test", Accessibility: VisualizationAccessibility{Title: "Test", Description: "Test"}}, Kind: "geographic", Presentation: GeographicVisualizationPresentation{VisualizationPresentation: VisualizationPresentation{AxisVisible: &axisVisible}}}}},
		}
		for _, test := range tests {
			t.Run(test.name+"/"+strconv.FormatBool(axisVisible), func(t *testing.T) {
				if err := ValidateSpec(test.spec); err == nil || !strings.Contains(err.Error(), "spec.presentation.axisVisible is unsupported for "+test.kind) {
					t.Fatalf("ValidateSpec() axisVisible=%t error = %v, want axisVisible diagnostic", axisVisible, err)
				}
			})
		}
	}
}
