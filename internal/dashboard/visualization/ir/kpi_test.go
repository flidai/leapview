package ir

import (
	"strings"
	"testing"
)

func TestValidateSpecEnforcesGovernedKPIComparisonContract(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	base := VisualizationSpecBase{
		Kind: "kpi", Title: "Revenue",
		Datasets: []VisualizationDatasetSchema{
			{ID: "primary", Fields: []VisualizationField{
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
			}},
			{ID: "comparison", Fields: []VisualizationField{
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Previous revenue"},
			}},
			{ID: "goal", Fields: []VisualizationField{
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Target"},
			}},
			{ID: "trend", Fields: []VisualizationField{
				{ID: "period", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDate, Label: "Month"},
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
			}},
		},
		DataBudget:    VisualizationDataBudget{MaxRows: 24, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue against prior period and target"},
		Interactions:  []VisualizationInteraction{},
	}
	valid := func() VisualizationSpec {
		return VisualizationSpec{Value: &KPIVisualizationSpec{
			VisualizationSpecBase: base,
			Kind:                  "kpi",
			Value:                 VisualizationFieldRef{Dataset: "primary", Field: "value"},
			Comparison: &VisualizationKPIValueBinding{
				Field:   VisualizationFieldRef{Dataset: "comparison", Field: "value"},
				Reducer: VisualizationReferenceReducerFirst,
				Label:   "Previous",
			},
			Goal: &VisualizationKPIValueBinding{
				Field:   VisualizationFieldRef{Dataset: "goal", Field: "value"},
				Reducer: VisualizationReferenceReducerFirst,
				Label:   "Target",
			},
			Trend: &VisualizationKPITrendBinding{
				Category: VisualizationFieldRef{Dataset: "trend", Field: "period"},
				Value:    VisualizationFieldRef{Dataset: "trend", Field: "value"},
			},
			Presentation: KPIVisualizationPresentation{
				Mode:               VisualizationKPIModeBullet,
				Delta:              VisualizationKPIDeltaModeRelative,
				FavorableDirection: VisualizationKPIDirectionIncrease,
				MissingComparison:  VisualizationKPIMissingComparisonShowUnavailable,
				Ranges: []VisualizationKPIQualitativeRange{{
					Minimum: &minimum, Maximum: &maximum, Label: "On track", Tone: VisualizationToneSuccess,
				}},
			},
		}}
	}

	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("valid KPI contract: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*KPIVisualizationSpec)
		want   string
	}{
		{
			name: "comparison must be numeric",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Comparison.Field.Field = "period"
				spec.Comparison.Field.Dataset = "trend"
			},
			want: "comparison field must be numeric",
		},
		{
			name: "mean reducer must be numeric",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Comparison.Field.Field = "period"
				spec.Comparison.Field.Dataset = "trend"
				spec.Comparison.Reducer = VisualizationReferenceReducerMean
			},
			want: "comparison field must be numeric",
		},
		{
			name: "bullet requires goal",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Goal = nil
			},
			want: `mode "bullet" requires a goal`,
		},
		{
			name: "comparison requires explicit direction",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Presentation.FavorableDirection = ""
			},
			want: "comparison requires favorable direction",
		},
		{
			name: "trend value must be numeric",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Trend.Value = spec.Trend.Category
			},
			want: "trend value field must be numeric",
		},
		{
			name: "ranges must not overlap",
			mutate: func(spec *KPIVisualizationSpec) {
				leftMaximum, rightMinimum := 60.0, 50.0
				spec.Presentation.Ranges = []VisualizationKPIQualitativeRange{
					{Maximum: &leftMaximum, Label: "Behind", Tone: VisualizationToneDanger},
					{Minimum: &rightMinimum, Label: "Ahead", Tone: VisualizationToneSuccess},
				}
			},
			want: "qualitative ranges overlap",
		},
		{
			name: "range labels are mandatory",
			mutate: func(spec *KPIVisualizationSpec) {
				spec.Presentation.Ranges[0].Label = ""
			},
			want: "qualitative range 0 requires a label",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			specification := valid()
			test.mutate(specification.Value.(*KPIVisualizationSpec))
			err := ValidateSpec(specification)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSpecKPIConditionalFormattingRequiresValueField(t *testing.T) {
	t.Parallel()

	color := VisualizationColorIntentDanger
	format := VisualizationConditionalFormat{
		ID: "kpi-value", Target: VisualizationConditionalTargetKpiValue,
		Field: VisualizationFieldRef{Dataset: "comparison", Field: "value"},
		Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
			Low: VisualizationConditionalStyle{Color: &color}, High: VisualizationConditionalStyle{Color: &color}, NullStyle: VisualizationConditionalStyle{Color: &color},
		}},
	}
	spec := VisualizationSpec{Value: &KPIVisualizationSpec{
		VisualizationSpecBase: VisualizationSpecBase{
			Kind: "kpi", Title: "Revenue",
			Datasets: []VisualizationDatasetSchema{
				{ID: "primary", Fields: []VisualizationField{{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"}}},
				{ID: "comparison", Fields: []VisualizationField{{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Previous revenue"}}},
			},
			DataBudget:    VisualizationDataBudget{MaxRows: 1, RequiredCompleteness: VisualizationCompletenessComplete},
			Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue"},
			Interactions:  []VisualizationInteraction{}, ConditionalFormatting: &[]VisualizationConditionalFormat{format},
		},
		Kind: "kpi", Value: VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Presentation: KPIVisualizationPresentation{Mode: VisualizationKPIModeCompact, Delta: VisualizationKPIDeltaModeAbsolute, FavorableDirection: VisualizationKPIDirectionNeutral, MissingComparison: VisualizationKPIMissingComparisonShowUnavailable, Ranges: []VisualizationKPIQualitativeRange{}},
	}}
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "not the rendered KPI value field") {
		t.Fatalf("ValidateSpec() error = %v, want KPI value-field diagnostic", err)
	}

	(*spec.Value.(*KPIVisualizationSpec).ConditionalFormatting)[0].Field = VisualizationFieldRef{Dataset: "primary", Field: "value"}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("ValidateSpec() rejected KPI value conditional formatting: %v", err)
	}
}
