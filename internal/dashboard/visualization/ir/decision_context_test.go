package ir

import (
	"strings"
	"testing"
)

func TestValidateSpecRejectsInvalidDecisionContext(t *testing.T) {
	t.Parallel()

	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Revenue",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
			{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
		Interactions:  []VisualizationInteraction{},
	}
	number := func(value float64) VisualizationReferenceValue {
		return VisualizationReferenceValue{Value: &NumberVisualizationReferenceValue{
			VisualizationReferenceValueBase: VisualizationReferenceValueBase{Kind: "number"}, Kind: "number", Value: value,
		}}
	}
	field := func(name string) VisualizationReferenceValue {
		return VisualizationReferenceValue{Value: &FieldVisualizationReferenceValue{
			VisualizationReferenceValueBase: VisualizationReferenceValueBase{Kind: "field"}, Kind: "field",
			Field: VisualizationFieldRef{Dataset: "primary", Field: name}, Reducer: VisualizationReferenceReducerFirst,
		}}
	}
	valid := func() VisualizationSpec {
		lines := []VisualizationReferenceLine{{ID: "target", Axis: VisualizationCartesianAxisPrimaryY, Value: number(80), Tone: VisualizationToneSuccess}}
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkLine,
			X: VisualizationFieldRef{Dataset: "primary", Field: "month"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}},
			ReferenceLines: &lines,
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
				ShowSymbols:               true,
			},
		}}
	}

	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("valid decision context: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CartesianVisualizationSpec)
		want   string
	}{
		{
			name: "unknown field binding",
			mutate: func(spec *CartesianVisualizationSpec) {
				(*spec.ReferenceLines)[0].Value = field("deleted_target")
			},
			want: `unknown visualization field "deleted_target"`,
		},
		{
			name: "duplicate identity",
			mutate: func(spec *CartesianVisualizationSpec) {
				bands := []VisualizationReferenceBand{{ID: "target", Axis: VisualizationCartesianAxisPrimaryY, From: number(70), To: number(90), Tone: VisualizationToneNeutral}}
				spec.ReferenceBands = &bands
			},
			want: `duplicate decision context ID "target"`,
		},
		{
			name: "invalid log domain",
			mutate: func(spec *CartesianVisualizationSpec) {
				minimum := 0.0
				axes := []VisualizationAxisConfiguration{{ID: VisualizationCartesianAxisPrimaryY, Scale: VisualizationAxisScaleLog, Zero: VisualizationAxisZeroPolicyExclude, Minimum: &minimum, TickDensity: VisualizationAxisTickDensityAutomatic}}
				spec.Axes = &axes
			},
			want: "log scale requires positive bounds",
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

func TestValidateSpecEnforcesStackingAndSeriesIntent(t *testing.T) {
	t.Parallel()

	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Revenue",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
			{ID: "status", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Status"},
			{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
		Interactions:  []VisualizationInteraction{},
	}
	stacking := VisualizationStackingModePercent
	order := int32(0)
	color := VisualizationColorIntentSuccess
	valid := func() VisualizationSpec {
		intent := []VisualizationSeriesIntent{{Value: "delivered", Order: &order, Color: &color}}
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkArea,
			X: VisualizationFieldRef{Dataset: "primary", Field: "month"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}},
			Series: &VisualizationFieldRef{Dataset: "primary", Field: "status"},
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
				ShowSymbols:               true, Stacking: &stacking, SeriesIntent: &intent,
			},
		}}
	}

	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("valid stacking and series intent: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CartesianVisualizationSpec)
		want   string
	}{
		{
			name: "percent without series",
			mutate: func(spec *CartesianVisualizationSpec) {
				spec.Series = nil
			},
			want: "percent stacking requires multiple series",
		},
		{
			name: "unsupported mark",
			mutate: func(spec *CartesianVisualizationSpec) {
				spec.Mark = VisualizationCartesianMarkHeatmap
			},
			want: `mark "heatmap" does not support stacking`,
		},
		{
			name: "percent with dual axes",
			mutate: func(spec *CartesianVisualizationSpec) {
				spec.Mark = VisualizationCartesianMarkCombo
				spec.Presentation.ComboSeries = &[]VisualizationComboSeries{{
					SeriesValue: "delivered",
					Mark:        VisualizationCartesianMarkLine,
					Axis:        VisualizationAxisSecondary,
				}}
			},
			want: "percent stacking cannot use dual axes",
		},
		{
			name: "duplicate series value",
			mutate: func(spec *CartesianVisualizationSpec) {
				*spec.Presentation.SeriesIntent = append(*spec.Presentation.SeriesIntent, (*spec.Presentation.SeriesIntent)[0])
			},
			want: `duplicate series intent "delivered"`,
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
