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
	automaticAxis := func(id VisualizationCartesianAxis) VisualizationAxisConfiguration {
		return VisualizationAxisConfiguration{
			ID: id, Type: VisualizationAxisTypeAutomatic, Scale: VisualizationAxisScaleAutomatic, Zero: VisualizationAxisZeroPolicyAutomatic,
			Inversion: VisualizationAxisInversionAutomatic, Ticks: VisualizationAxisTickVisibilityAutomatic, Grid: VisualizationAxisGridVisibilityAutomatic,
			LabelRotation: VisualizationAxisLabelRotationAutomatic, DateUnit: VisualizationDateDisplayUnitAutomatic, TickDensity: VisualizationAxisTickDensityAutomatic,
		}
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
				axis := automaticAxis(VisualizationCartesianAxisPrimaryY)
				axis.Scale, axis.Zero, axis.Minimum = VisualizationAxisScaleLog, VisualizationAxisZeroPolicyExclude, &minimum
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "log scale requires positive bounds",
		},
		{
			name: "category domain",
			mutate: func(spec *CartesianVisualizationSpec) {
				minimum := 0.0
				typeValue := VisualizationAxisTypeCategory
				axis := automaticAxis(VisualizationCartesianAxisX)
				axis.Type, axis.Minimum = typeValue, &minimum
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "domain bounds require an effective numeric field",
		},
		{
			name: "automatic numeric X display units",
			mutate: func(spec *CartesianVisualizationSpec) {
				spec.X.Field = "revenue"
				units := VisualizationDisplayUnitsMillions
				axis := automaticAxis(VisualizationCartesianAxisX)
				axis.DisplayUnits = &units
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "display units require an effective numeric field",
		},
		{
			name: "percent primary display units",
			mutate: func(spec *CartesianVisualizationSpec) {
				stacking := VisualizationStackingModePercent
				spec.Presentation.Stacking = &stacking
				spec.Y = []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}, {Dataset: "primary", Field: "revenue"}}
				units := VisualizationDisplayUnitsAuto
				axis := automaticAxis(VisualizationCartesianAxisPrimaryY)
				axis.DisplayUnits = &units
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "display units are incompatible with percent stacking",
		},
		{
			name: "category scale",
			mutate: func(spec *CartesianVisualizationSpec) {
				typeValue := VisualizationAxisTypeCategory
				axis := automaticAxis(VisualizationCartesianAxisX)
				axis.Type, axis.Scale = typeValue, VisualizationAxisScaleLinear
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "linear scale requires an effective numeric field",
		},
		{
			name: "invalid axis enum",
			mutate: func(spec *CartesianVisualizationSpec) {
				invalid := VisualizationAxisGridVisibility("invalid")
				axis := automaticAxis(VisualizationCartesianAxisX)
				axis.Grid = invalid
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "unsupported grid visibility",
		},
		{
			name: "invalid legacy axis enum",
			mutate: func(spec *CartesianVisualizationSpec) {
				axis := automaticAxis(VisualizationCartesianAxisX)
				axis.Scale = VisualizationAxisScale("invalid")
				axes := []VisualizationAxisConfiguration{axis}
				spec.Axes = &axes
			},
			want: "unsupported scale",
		},
		{
			name: "omitted required axis policy",
			mutate: func(spec *CartesianVisualizationSpec) {
				axes := []VisualizationAxisConfiguration{{ID: VisualizationCartesianAxisX, Scale: VisualizationAxisScaleAutomatic, Zero: VisualizationAxisZeroPolicyAutomatic, TickDensity: VisualizationAxisTickDensityAutomatic}}
				spec.Axes = &axes
			},
			want: "unsupported type",
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
	automaticNumericX := valid()
	automaticNumericX.Value.(*CartesianVisualizationSpec).X.Field = "revenue"
	automaticAxisConfig := automaticAxis(VisualizationCartesianAxisX)
	automaticNumericX.Value.(*CartesianVisualizationSpec).Axes = &[]VisualizationAxisConfiguration{automaticAxisConfig}
	if err := ValidateSpec(automaticNumericX); err != nil {
		t.Fatalf("automatic numeric cartesian X should retain category semantics without numeric policies: %v", err)
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
				spec.Series = nil
				spec.Y = append(spec.Y, spec.Y[0])
				spec.Presentation.ComboSeries = &[]VisualizationComboSeries{{
					SeriesValue: "revenue",
					Mark:        VisualizationCartesianMarkLine,
					Axis:        VisualizationAxisSecondary,
				}}
			},
			want: "percent stacking cannot use dual axes",
		},
		{
			name: "percent with presentation display units",
			mutate: func(spec *CartesianVisualizationSpec) {
				units := VisualizationDisplayUnitsAuto
				spec.Presentation.DisplayUnits = &units
			},
			want: "percent stacking cannot use presentation display units",
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

func TestValidateSpecSeriesIntentSupportsSingleMetricAndRejectsNoOpOrWhitespace(t *testing.T) {
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
	newSpec := func(intents []VisualizationSeriesIntent) VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkLine,
			X: VisualizationFieldRef{Dataset: "primary", Field: "month"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}},
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom),
				SeriesIntent:              &intents,
			},
		}}
	}

	color := VisualizationColorIntentSuccess
	if err := ValidateSpec(newSpec([]VisualizationSeriesIntent{{Value: "revenue", Color: &color}})); err != nil {
		t.Fatalf("single-metric color intent should be valid: %v", err)
	}
	order := int32(0)
	if err := ValidateSpec(newSpec([]VisualizationSeriesIntent{{Value: "revenue", Order: &order}})); err == nil || !strings.Contains(err.Error(), "series intent[0].order") || !strings.Contains(err.Error(), "single metric") {
		t.Fatalf("single-metric order error = %v, want path-bearing diagnostic", err)
	}
	if err := ValidateSpec(newSpec([]VisualizationSeriesIntent{})); err == nil || !strings.Contains(err.Error(), "series intent must contain at least one intent") {
		t.Fatalf("empty series intent error = %v, want no-op diagnostic", err)
	}
	if err := ValidateSpec(newSpec([]VisualizationSeriesIntent{{Value: " revenue "}})); err == nil || !strings.Contains(err.Error(), "series intent[0].value") || !strings.Contains(err.Error(), "surrounding whitespace") {
		t.Fatalf("surrounding whitespace error = %v, want path-bearing diagnostic", err)
	}
}

func TestValidateSpecGainLossColorsAreCandlestickOnly(t *testing.T) {
	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "OHLC",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "date", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Date"},
			{ID: "open", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Open"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "OHLC", Description: "OHLC"}, Interactions: []VisualizationInteraction{},
	}
	color := VisualizationColorIntentSuccess
	newSpec := func(mark VisualizationCartesianMark, gain, loss *VisualizationColorIntent) VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: mark,
			X: VisualizationFieldRef{Dataset: "primary", Field: "date"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "open"}},
			Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden), GainColor: gain, LossColor: loss},
		}}
	}
	if err := ValidateSpec(newSpec(VisualizationCartesianMarkCandlestick, &color, nil)); err != nil {
		t.Fatalf("candlestick gain color rejected: %v", err)
	}
	if err := ValidateSpec(newSpec(VisualizationCartesianMarkCandlestick, nil, &color)); err != nil {
		t.Fatalf("candlestick loss color rejected: %v", err)
	}
	for _, mark := range []VisualizationCartesianMark{
		VisualizationCartesianMarkLine, VisualizationCartesianMarkArea, VisualizationCartesianMarkBar,
		VisualizationCartesianMarkColumn, VisualizationCartesianMarkCombo, VisualizationCartesianMarkWaterfall,
		VisualizationCartesianMarkHeatmap, VisualizationCartesianMarkHistogram, VisualizationCartesianMarkBoxplot,
	} {
		for _, test := range []struct {
			name       string
			gain, loss *VisualizationColorIntent
		}{
			{name: "gain", gain: &color},
			{name: "loss", loss: &color},
		} {
			t.Run(string(mark)+"/"+test.name, func(t *testing.T) {
				err := ValidateSpec(newSpec(mark, test.gain, test.loss))
				want := "spec.presentation." + test.name + "Color"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want %q", err, want)
				}
			})
		}
	}
	for name, field := range map[string]string{"gain": "gainColor", "loss": "lossColor"} {
		t.Run("invalid/"+name, func(t *testing.T) {
			invalid := VisualizationColorIntent("invalid")
			gain, loss := (*VisualizationColorIntent)(nil), (*VisualizationColorIntent)(nil)
			if field == "gainColor" {
				gain = &invalid
			} else {
				loss = &invalid
			}
			err := ValidateSpec(newSpec(VisualizationCartesianMarkCandlestick, gain, loss))
			want := "spec.presentation." + field
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestValidateSpecFinancialLabelsAreHiddenOnly(t *testing.T) {
	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Financial",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "date", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Date"},
			{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Financial", Description: "Financial"}, Interactions: []VisualizationInteraction{},
	}
	newSpec := func(mark VisualizationCartesianMark, density VisualizationLabelDensity, position *VisualizationLabelPosition) VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: mark,
			X: VisualizationFieldRef{Dataset: "primary", Field: "date"}, Y: []VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
			Presentation: CartesianVisualizationPresentation{
				VisualizationPresentation: VisualizationPresentation{Legend: VisualizationLegendPositionHidden, LabelPolicy: VisualizationLabelPolicy{Density: density, Priority: []VisualizationLabelPriority{}, MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true}},
				LabelPosition:             position,
			},
		}}
	}
	for _, mark := range []VisualizationCartesianMark{VisualizationCartesianMarkCandlestick, VisualizationCartesianMarkBoxplot} {
		t.Run(string(mark)+"/hidden", func(t *testing.T) {
			if err := ValidateSpec(newSpec(mark, VisualizationLabelDensityHidden, nil)); err != nil {
				t.Fatalf("hidden financial labels rejected: %v", err)
			}
		})
		for _, density := range []VisualizationLabelDensity{VisualizationLabelDensityAutomatic, VisualizationLabelDensityDense, VisualizationLabelDensityAlways} {
			t.Run(string(mark)+"/"+string(density), func(t *testing.T) {
				err := ValidateSpec(newSpec(mark, density, nil))
				if err == nil || !strings.Contains(err.Error(), "spec.presentation.labelPolicy") {
					t.Fatalf("error = %v, want path-bearing label policy diagnostic", err)
				}
			})
		}
		t.Run(string(mark)+"/labelPosition", func(t *testing.T) {
			position := VisualizationLabelPositionInside
			err := ValidateSpec(newSpec(mark, VisualizationLabelDensityHidden, &position))
			if err == nil || !strings.Contains(err.Error(), "spec.presentation.labelPosition") {
				t.Fatalf("error = %v, want path-bearing label position diagnostic", err)
			}
		})
	}
}

func TestValidateSpecUsesComboAxisOwnersForContext(t *testing.T) {
	t.Parallel()

	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Combo",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
			{ID: "secondary_value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeString, Label: "Secondary"},
			{ID: "primary_value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Primary"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Combo", Description: "Combo"},
		Interactions:  []VisualizationInteraction{},
	}
	axis := VisualizationAxisConfiguration{ID: VisualizationCartesianAxisPrimaryY, Type: VisualizationAxisTypeValue, Scale: VisualizationAxisScaleAutomatic, Zero: VisualizationAxisZeroPolicyAutomatic, Inversion: VisualizationAxisInversionAutomatic, TickDensity: VisualizationAxisTickDensityAutomatic, Ticks: VisualizationAxisTickVisibilityAutomatic, Grid: VisualizationAxisGridVisibilityAutomatic, LabelRotation: VisualizationAxisLabelRotationAutomatic, DateUnit: VisualizationDateDisplayUnitAutomatic}
	number := func(value float64) VisualizationReferenceValue {
		return VisualizationReferenceValue{Value: &NumberVisualizationReferenceValue{VisualizationReferenceValueBase: VisualizationReferenceValueBase{Kind: "number"}, Kind: "number", Value: value}}
	}
	valid := func() VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkCombo,
			X:    VisualizationFieldRef{Dataset: "primary", Field: "month"},
			Y:    []VisualizationFieldRef{{Dataset: "primary", Field: "secondary_value"}, {Dataset: "primary", Field: "primary_value"}},
			Axes: &[]VisualizationAxisConfiguration{axis},
			Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom), ComboSeries: &[]VisualizationComboSeries{
				{SeriesValue: "secondary_value", Axis: VisualizationAxisSecondary},
				{SeriesValue: "primary_value", Axis: VisualizationAxisPrimary},
			}},
		}}
	}
	if err := ValidateSpec(valid()); err != nil {
		t.Fatalf("primary axis should use the first primary combo field rather than Y[0]: %v", err)
	}

	allSecondary := valid()
	combo := allSecondary.Value.(*CartesianVisualizationSpec)
	combo.Axes = nil
	combo.Presentation.ComboSeries = &[]VisualizationComboSeries{
		{SeriesValue: "secondary_value", Axis: VisualizationAxisSecondary},
		{SeriesValue: "primary_value", Axis: VisualizationAxisSecondary},
	}
	for _, test := range []struct {
		name string
		set  func(*CartesianVisualizationSpec)
		want string
	}{
		{name: "primary reference line", set: func(spec *CartesianVisualizationSpec) {
			spec.ReferenceLines = &[]VisualizationReferenceLine{{ID: "target", Axis: VisualizationCartesianAxisPrimaryY, Value: number(10)}}
		}, want: "primary_y decision context requires a primary_y combo series"},
		{name: "x reference line owner", set: func(spec *CartesianVisualizationSpec) {
			spec.ReferenceLines = &[]VisualizationReferenceLine{{ID: "launch", Axis: VisualizationCartesianAxisX, Value: number(10)}}
		}, want: "x decision context requires a primary_y combo series"},
		{name: "secondary reference band", set: func(spec *CartesianVisualizationSpec) {
			spec.Presentation.ComboSeries = &[]VisualizationComboSeries{{SeriesValue: "primary_value", Axis: VisualizationAxisPrimary}}
			spec.ReferenceBands = &[]VisualizationReferenceBand{{ID: "range", Axis: VisualizationCartesianAxisSecondaryY, From: number(1), To: number(2)}}
		}, want: "secondary_y decision context requires a secondary_y combo series"},
		{name: "event annotation", set: func(spec *CartesianVisualizationSpec) {
			spec.EventAnnotations = &[]VisualizationEventAnnotation{{ID: "launch", Axis: VisualizationCartesianAxisX, Value: number(1), Label: "Launch"}}
		}, want: "event annotation \"launch\" requires a primary_y combo series"},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := allSecondary
			spec.Value = &CartesianVisualizationSpec{
				VisualizationSpecBase: combo.VisualizationSpecBase,
				Kind:                  combo.Kind,
				Mark:                  combo.Mark,
				X:                     combo.X,
				Y:                     combo.Y,
				Axes:                  combo.Axes,
				Presentation:          combo.Presentation,
			}
			test.set(spec.Value.(*CartesianVisualizationSpec))
			if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSpecRejectsInvalidComboSeriesPolicies(t *testing.T) {
	t.Parallel()

	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Combo",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "month", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Month"},
			{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
			{ID: "orders", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Orders"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Combo", Description: "Combo"},
		Interactions:  []VisualizationInteraction{},
	}
	valid := func(series []VisualizationComboSeries) VisualizationSpec {
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkCombo,
			X:            VisualizationFieldRef{Dataset: "primary", Field: "month"},
			Y:            []VisualizationFieldRef{{Dataset: "primary", Field: "revenue"}, {Dataset: "primary", Field: "orders"}},
			Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom), ComboSeries: &series},
		}}
	}
	cases := []struct {
		name   string
		series []VisualizationComboSeries
		want   string
	}{
		{name: "surrounding whitespace", series: []VisualizationComboSeries{{SeriesValue: " revenue ", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}, {SeriesValue: "orders", Mark: VisualizationCartesianMarkArea, Axis: VisualizationAxisSecondary}}, want: "combo series[0].seriesValue"},
		{name: "duplicate value", series: []VisualizationComboSeries{{SeriesValue: "revenue", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}, {SeriesValue: "revenue", Mark: VisualizationCartesianMarkArea, Axis: VisualizationAxisSecondary}}, want: "duplicate combo series value"},
		{name: "unknown metric", series: []VisualizationComboSeries{{SeriesValue: "missing", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}}, want: "not a compiled metric field"},
		{name: "missing metric", series: []VisualizationComboSeries{{SeriesValue: "revenue", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}}, want: "configure every compiled metric"},
		{name: "mixed orientation", series: []VisualizationComboSeries{{SeriesValue: "revenue", Mark: VisualizationCartesianMarkBar, Axis: VisualizationAxisPrimary}, {SeriesValue: "orders", Mark: VisualizationCartesianMarkColumn, Axis: VisualizationAxisSecondary}}, want: "cannot mix bar and column"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSpec(valid(test.series)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want substring %q", err, test.want)
			}
		})
	}
	dynamic := valid([]VisualizationComboSeries{{SeriesValue: "revenue", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}, {SeriesValue: "orders", Mark: VisualizationCartesianMarkArea, Axis: VisualizationAxisSecondary}})
	dynamicSpec := dynamic.Value.(*CartesianVisualizationSpec)
	dynamicSpec.Presentation.ComboSeries = nil
	dynamicSpec.Series = &VisualizationFieldRef{Dataset: "primary", Field: "month"}
	if err := ValidateSpec(dynamic); err == nil || !strings.Contains(err.Error(), "combo.series") || !strings.Contains(err.Error(), "dynamic category series") {
		t.Fatalf("dynamic combo series error = %v, want path-bearing rejection", err)
	}

	duplicateOrder := int32(2)
	intentSpec := valid([]VisualizationComboSeries{{SeriesValue: "revenue", Mark: VisualizationCartesianMarkLine, Axis: VisualizationAxisPrimary}})
	intentSpec.Value.(*CartesianVisualizationSpec).Presentation.SeriesIntent = &[]VisualizationSeriesIntent{
		{Value: "revenue", Order: &duplicateOrder}, {Value: "orders", Order: &duplicateOrder},
	}
	if err := ValidateSpec(intentSpec); err == nil || !strings.Contains(err.Error(), "duplicate series order") {
		t.Fatalf("ValidateSpec() duplicate order error = %v", err)
	}
}
