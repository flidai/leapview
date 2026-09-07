package ir

import (
	"math"
	"strings"
	"testing"
)

func TestValidateSpecAcceptsPointColorScaleKinds(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	tests := []struct {
		name  string
		field string
		scale PointVisualizationColorScale
	}{
		{name: "categorical", field: "state", scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindCategorical}},
		{name: "quantitative", field: "revenue", scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindQuantitative, Minimum: &minimum, Maximum: &maximum}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec := pointContractSpec(test.field, &test.scale)
			if err := ValidateSpec(spec); err != nil {
				t.Fatalf("ValidateSpec() error = %v", err)
			}
		})
	}
}

func TestValidateSpecRejectsInvalidPointColorScales(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	tests := []struct {
		name  string
		field string
		scale PointVisualizationColorScale
		want  string
	}{
		{
			name:  "categorical domain",
			field: "state",
			scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindCategorical, Minimum: &minimum, Maximum: &maximum},
			want:  "domain requires a quantitative scale",
		},
		{
			name:  "unknown kind",
			field: "state",
			scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKind("unknown")},
			want:  "color scale kind \"unknown\" is unsupported",
		},
		{
			name:  "nonfinite minimum",
			field: "revenue",
			scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindQuantitative, Minimum: floatPtr(math.NaN()), Maximum: &maximum},
			want:  "minimum must be finite",
		},
		{
			name:  "nonfinite maximum",
			field: "revenue",
			scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindQuantitative, Minimum: &minimum, Maximum: floatPtr(math.Inf(1))},
			want:  "maximum must be finite",
		},
		{
			name:  "reversed domain",
			field: "revenue",
			scale: PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindQuantitative, Minimum: &maximum, Maximum: &minimum},
			want:  "minimum must be less than maximum",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := ValidateSpec(pointContractSpec(test.field, &test.scale))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateSpecRejectsQuantitativePointColorOnDimension(t *testing.T) {
	t.Parallel()

	scale := PointVisualizationColorScale{Kind: VisualizationPointColorScaleKindQuantitative}
	err := ValidateSpec(pointContractSpec("state", &scale))
	if err == nil || !strings.Contains(err.Error(), "quantitative point color scale requires a numeric field") {
		t.Fatalf("ValidateSpec() error = %v, want quantitative color field diagnostic", err)
	}
}

func TestValidateSpecPointConditionalFormattingTargets(t *testing.T) {
	t.Parallel()

	valid := pointGradientConditionalFormat("mark-fill", "revenue", VisualizationConditionalTargetMarkFill)
	if err := ValidateSpec(pointContractSpec("", nil, valid)); err != nil {
		t.Fatalf("ValidateSpec() rejected one point mark_fill rule: %v", err)
	}

	duplicate := pointGradientConditionalFormat("second-mark-fill", "x", VisualizationConditionalTargetMarkFill)
	if err := ValidateSpec(pointContractSpec("", nil, valid, duplicate)); err == nil || !strings.Contains(err.Error(), "point visualizations allow one mark_fill rule") {
		t.Fatalf("ValidateSpec() error = %v, want duplicate mark_fill diagnostic", err)
	}

	for _, target := range []VisualizationConditionalTarget{
		VisualizationConditionalTargetMarkStroke,
		VisualizationConditionalTargetSeriesColor,
		VisualizationConditionalTargetLabelForeground,
		VisualizationConditionalTargetVisualBackground,
		VisualizationConditionalTargetCellForeground,
		VisualizationConditionalTargetCellBackground,
		VisualizationConditionalTargetKpiValue,
		VisualizationConditionalTargetIcon,
	} {
		t.Run(string(target), func(t *testing.T) {
			format := pointGradientConditionalFormat("invalid-"+string(target), "revenue", target)
			err := ValidateSpec(pointContractSpec("", nil, format))
			if err == nil || !strings.Contains(err.Error(), "incompatible with point visualizations") {
				t.Fatalf("ValidateSpec() error = %v, want point target diagnostic", err)
			}
		})
	}
}

func TestValidateSpecPointMarkFillRequiresColorForEveryOutcome(t *testing.T) {
	t.Parallel()

	icon := VisualizationIconIntentWarning
	color := VisualizationColorIntentDanger
	iconOnly := VisualizationConditionalStyle{Icon: &icon}
	colored := VisualizationConditionalStyle{Color: &color, Icon: &icon}
	tests := []struct {
		name string
		rule VisualizationConditionalRule
		want string
	}{
		{name: "gradient null", rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
			Low: colored, High: colored, NullStyle: iconOnly,
		}}, want: "null style requires color"},
		{name: "rules default", rule: VisualizationConditionalRule{Value: &RulesVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "rules"}, Kind: "rules",
			Rules:     []VisualizationConditionalThreshold{{Operator: VisualizationComparisonOperatorGreaterThan, Value: 50, Style: colored}},
			NullStyle: colored, DefaultStyle: iconOnly,
		}}, want: "default style requires color"},
		{name: "field value", rule: VisualizationConditionalRule{Value: &FieldVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "field"}, Kind: "field",
			Source: VisualizationFieldRef{Dataset: "primary", Field: "state"}, Values: map[string]VisualizationConditionalStyle{"late": iconOnly},
			NullStyle: colored, DefaultStyle: colored,
		}}, want: `value "late" style requires color`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format := VisualizationConditionalFormat{
				ID: "fill", Target: VisualizationConditionalTargetMarkFill,
				Field: VisualizationFieldRef{Dataset: "primary", Field: "revenue"}, Rule: test.rule,
			}
			err := ValidateSpec(pointContractSpec("", nil, format))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func pointContractSpec(colorField string, scale *PointVisualizationColorScale, formats ...VisualizationConditionalFormat) VisualizationSpec {
	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	base := VisualizationSpecBase{
		Kind: "point", Title: "Orders",
		Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "order_id", Role: VisualizationFieldRoleIdentity, DataType: VisualizationDataTypeString, Label: "Order"},
			{ID: "x", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "X"},
			{ID: "y", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Y"},
			{ID: "state", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "State"},
			{ID: "revenue", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Revenue"},
		}}},
		DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
		Accessibility: VisualizationAccessibility{Title: "Orders", Description: "Order delivery and revenue"},
		Interactions:  []VisualizationInteraction{},
	}
	if formats != nil {
		base.ConditionalFormatting = &formats
	}
	point := &PointVisualizationSpec{
		VisualizationSpecBase: base, Kind: "point", Identity: []VisualizationFieldRef{ref("order_id")}, X: ref("x"), Y: ref("y"),
		Presentation: PointVisualizationPresentation{
			VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionHidden),
			Overplot:                  VisualizationPointOverplotStrategyOpacity,
			Opacity:                   0.7,
			LargeMode:                 VisualizationPointLargeModeAutomatic,
			LargeThreshold:            10000,
			Brush:                     []VisualizationPointBrushGesture{},
		},
	}
	if colorField != "" {
		color := ref(colorField)
		point.Color = &color
		point.ColorScale = scale
	}
	return VisualizationSpec{Value: point}
}

func pointGradientConditionalFormat(id, field string, target VisualizationConditionalTarget) VisualizationConditionalFormat {
	color := VisualizationColorIntentDanger
	style := VisualizationConditionalStyle{Color: &color}
	return VisualizationConditionalFormat{
		ID: id, Target: target, Field: VisualizationFieldRef{Dataset: "primary", Field: field},
		Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
			Low: style, High: style, NullStyle: style,
		}},
	}
}

func floatPtr(value float64) *float64 { return &value }
