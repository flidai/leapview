package ir

import (
	"strings"
	"testing"
)

func TestValidateSpecEnforcesDataBoundMetadata(t *testing.T) {
	t.Parallel()

	binding := func(field string, reducer VisualizationReferenceReducer, fallback string) *VisualizationTextBinding {
		return &VisualizationTextBinding{
			Field: VisualizationFieldRef{Dataset: "context", Field: field}, Reducer: reducer,
			Prefix: "Revenue — ", Fallback: fallback,
		}
	}
	valid := func() VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: VisualizationSpecBase{
				Kind: "cartesian", Title: "Revenue",
				Datasets: []VisualizationDatasetSchema{
					{ID: "primary", Fields: []VisualizationField{
						{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
						{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
					}},
					{ID: "context", Fields: []VisualizationField{
						{ID: "region", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Region"},
						{ID: "target", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Target"},
					}},
				},
				DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
				Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
				Interactions:  []VisualizationInteraction{},
				MetadataBindings: &VisualizationMetadataBindings{
					Title: binding("region", VisualizationReferenceReducerFirst, "Revenue"),
				},
			},
			Kind: "cartesian", Mark: VisualizationCartesianMarkLine,
			X: VisualizationFieldRef{Dataset: "primary", Field: "month"},
			Y: []VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
				ShowSymbols:               true,
			},
		}}
	}

	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("valid data-bound metadata: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VisualizationSpecBase)
		want   string
	}{
		{"unknown dataset", func(base *VisualizationSpecBase) {
			base.MetadataBindings.Title.Field.Dataset = "deleted"
		}, `unknown visualization dataset "deleted"`},
		{"unknown field", func(base *VisualizationSpecBase) {
			base.MetadataBindings.Title.Field.Field = "deleted"
		}, `unknown visualization field "deleted"`},
		{"empty fallback", func(base *VisualizationSpecBase) {
			base.MetadataBindings.Title.Fallback = ""
		}, "requires a non-empty fallback"},
		{"nonnumeric mean", func(base *VisualizationSpecBase) {
			base.MetadataBindings.Title.Reducer = VisualizationReferenceReducerMean
		}, "requires a numeric field"},
		{"unsupported reducer", func(base *VisualizationSpecBase) {
			base.MetadataBindings.Title.Reducer = "mode"
		}, `unsupported reducer "mode"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid()
			test.mutate(&spec.Value.(*CartesianVisualizationSpec).VisualizationSpecBase)
			err := ValidateSpec(spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
