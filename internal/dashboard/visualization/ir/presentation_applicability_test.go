package ir

import "testing"

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
