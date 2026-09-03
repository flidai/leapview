package ir

import (
	"strings"
	"testing"
)

func TestValidateSpecEnforcesGovernedConditionalFormatting(t *testing.T) {
	t.Parallel()

	color := func(value VisualizationColorIntent) *VisualizationColorIntent { return &value }
	icon := func(value VisualizationIconIntent) *VisualizationIconIntent { return &value }
	style := func(colorValue VisualizationColorIntent, iconValue VisualizationIconIntent) VisualizationConditionalStyle {
		return VisualizationConditionalStyle{Color: color(colorValue), Icon: icon(iconValue)}
	}
	valid := func() VisualizationSpec {
		formats := []VisualizationConditionalFormat{
			{
				ID: "revenue-gradient", Target: VisualizationConditionalTargetMarkFill,
				Field: VisualizationFieldRef{Dataset: "primary", Field: "revenue"},
				Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
					VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient",
					Minimum: 0, Maximum: 100,
					Low:       VisualizationConditionalStyle{Color: color(VisualizationColorIntentDanger)},
					High:      VisualizationConditionalStyle{Color: color(VisualizationColorIntentSuccess)},
					NullStyle: VisualizationConditionalStyle{Color: color(VisualizationColorIntentNeutral)},
				}},
			},
			{
				ID: "status-values", Target: VisualizationConditionalTargetIcon,
				Field: VisualizationFieldRef{Dataset: "primary", Field: "revenue"},
				Rule: VisualizationConditionalRule{Value: &FieldVisualizationConditionalRule{
					VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "field"}, Kind: "field",
					Source:       VisualizationFieldRef{Dataset: "primary", Field: "status"},
					Values:       map[string]VisualizationConditionalStyle{"late": style(VisualizationColorIntentDanger, VisualizationIconIntentWarning)},
					NullStyle:    VisualizationConditionalStyle{Icon: icon(VisualizationIconIntentWarning)},
					DefaultStyle: style(VisualizationColorIntentInk, VisualizationIconIntentCircle),
				}},
			},
		}
		base := VisualizationSpecBase{
			Kind: "cartesian", Title: "Revenue",
			Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
				{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
				{ID: "status", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Status"},
				{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
			}}},
			DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
			Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
			Interactions:  []VisualizationInteraction{}, ConditionalFormatting: &formats,
		}
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkColumn,
			X: VisualizationFieldRef{Dataset: "primary", Field: "month"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}},
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
				ShowSymbols:               true,
			},
		}}
	}

	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("valid conditional formatting: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CartesianVisualizationSpec)
		want   string
	}{
		{
			name: "unknown target field",
			mutate: func(spec *CartesianVisualizationSpec) {
				(*spec.ConditionalFormatting)[0].Field.Field = "deleted"
			},
			want: `unknown visualization field "deleted"`,
		},
		{
			name: "gradient on category",
			mutate: func(spec *CartesianVisualizationSpec) {
				(*spec.ConditionalFormatting)[0].Field.Field = "status"
			},
			want: "gradient requires a numeric field",
		},
		{
			name: "duplicate identity",
			mutate: func(spec *CartesianVisualizationSpec) {
				(*spec.ConditionalFormatting)[1].ID = "revenue-gradient"
			},
			want: `duplicate conditional formatting ID "revenue-gradient"`,
		},
		{
			name: "invalid domain",
			mutate: func(spec *CartesianVisualizationSpec) {
				rule := (*spec.ConditionalFormatting)[0].Rule.Value.(*GradientVisualizationConditionalRule)
				rule.Minimum = rule.Maximum
			},
			want: "minimum must be less than maximum",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := valid()
			test.mutate(spec.Value.(*CartesianVisualizationSpec))
			err := ValidateSpec(spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSpecAllowsGovernedProportionalCategoryColors(t *testing.T) {
	t.Parallel()
	data1 := VisualizationColorIntentData1
	circle := VisualizationIconIntentCircle
	formats := []VisualizationConditionalFormat{{
		ID: "status-colors", Target: VisualizationConditionalTargetSeriesColor,
		Field: VisualizationFieldRef{Dataset: "primary", Field: "orders"},
		Rule: VisualizationConditionalRule{Value: &FieldVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "field"}, Kind: "field",
			Source: VisualizationFieldRef{Dataset: "primary", Field: "status"},
			Values: map[string]VisualizationConditionalStyle{
				"delivered": {Color: &data1, Icon: &circle},
			},
			NullStyle:    VisualizationConditionalStyle{Color: &data1, Icon: &circle},
			DefaultStyle: VisualizationConditionalStyle{Color: &data1, Icon: &circle},
		}},
	}}
	base := VisualizationSpecBase{
		Kind: "proportional", Title: "Orders by status",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "status", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Status"},
			{ID: "orders", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeInteger, Label: "Orders"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Orders by status", Description: "Order status share"},
		Interactions:  []VisualizationInteraction{}, ConditionalFormatting: &formats,
	}
	spec := VisualizationSpec{Value: &ProportionalVisualizationSpec{
		VisualizationSpecBase: base, Kind: "proportional", Mark: VisualizationProportionalMarkDonut,
		Category: VisualizationFieldRef{Dataset: "primary", Field: "status"},
		Value:    VisualizationFieldRef{Dataset: "primary", Field: "orders"},
		Presentation: ProportionalVisualizationPresentation{
			VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
		},
	}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("valid proportional conditional formatting: %v", err)
	}
}
