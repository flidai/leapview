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
			want: "not rendered by the cartesian y channel",
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

func TestValidateSpecRejectsConditionalFormattingTargetsOutsideVisibleChannels(t *testing.T) {
	t.Parallel()

	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	fields := []VisualizationField{
		{ID: "x", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "X"},
		{ID: "row", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Row"},
		{ID: "column", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeDecimal, Label: "Column"},
		{ID: "start", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Start"},
		{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
		{ID: "metric", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Metric"},
	}
	base := func(kind string, format VisualizationConditionalFormat) VisualizationSpecBase {
		return VisualizationSpecBase{
			Kind: kind, Title: kind, Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
			DataBudget:    VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete},
			Accessibility: VisualizationAccessibility{Title: kind, Description: kind},
			Interactions:  []VisualizationInteraction{}, ConditionalFormatting: &[]VisualizationConditionalFormat{format},
		}
	}
	format := func(field string, target VisualizationConditionalTarget) VisualizationConditionalFormat {
		color := VisualizationColorIntentDanger
		style := VisualizationConditionalStyle{Color: &color}
		return VisualizationConditionalFormat{
			ID: field, Target: target, Field: ref(field),
			Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
				VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
				Low: style, High: style, NullStyle: style,
			}},
		}
	}
	presentation := testVisualizationPresentation(VisualizationLegendPositionBottom)
	cartesian := func(mark VisualizationCartesianMark, y []VisualizationFieldRef, format VisualizationConditionalFormat) VisualizationSpec {
		baseValue := base("cartesian", format)
		return VisualizationSpec{Value: &CartesianVisualizationSpec{
			VisualizationSpecBase: baseValue, Kind: "cartesian", Mark: mark, X: ref("x"), Y: y,
			Presentation: CartesianVisualizationPresentation{VisualizationPresentation: presentation},
		}}
	}
	proportional := func(value, category string) VisualizationSpec {
		baseValue := base("proportional", format(value, VisualizationConditionalTargetMarkFill))
		return VisualizationSpec{Value: &ProportionalVisualizationSpec{
			VisualizationSpecBase: baseValue, Kind: "proportional", Mark: VisualizationProportionalMarkPie,
			Category: ref(category), Value: ref("value"),
			Presentation: ProportionalVisualizationPresentation{VisualizationPresentation: presentation, Orientation: VisualizationOrientationVertical},
		}}
	}
	table := func(kind, field string, visible []string) VisualizationSpec {
		columns := make([]TableVisualizationColumn, 0, len(visible))
		for _, visibleField := range visible {
			columns = append(columns, TableVisualizationColumn{Field: ref(visibleField), Label: visibleField, Formatting: []TableVisualizationFormattingRule{}})
		}
		baseValue := base(kind, format(field, VisualizationConditionalTargetCellBackground))
		presentation := GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}
		if kind == "table" {
			return VisualizationSpec{Value: &TableVisualizationSpec{VisualizationSpecBase: baseValue, Kind: kind, Columns: columns, Presentation: presentation}}
		}
		if kind == "matrix" {
			return VisualizationSpec{Value: &MatrixVisualizationSpec{VisualizationSpecBase: baseValue, Kind: kind, Rows: []VisualizationFieldRef{ref("row")}, Columns: []VisualizationFieldRef{ref("column")}, Metrics: []VisualizationFieldRef{ref("metric")}, MetricFormatting: map[string][]TableVisualizationFormattingRule{}, Presentation: presentation}}
		}
		return VisualizationSpec{Value: &PivotVisualizationSpec{VisualizationSpecBase: baseValue, Kind: kind, Rows: []VisualizationFieldRef{ref("row")}, Columns: []VisualizationFieldRef{ref("column")}, Metrics: []VisualizationFieldRef{ref("metric")}, MetricFormatting: map[string][]TableVisualizationFormattingRule{}, Presentation: presentation}}
	}

	tests := []struct {
		name    string
		valid   VisualizationSpec
		invalid VisualizationSpec
		want    string
	}{
		{name: "cartesian y channel", valid: cartesian(VisualizationCartesianMarkColumn, []VisualizationFieldRef{ref("value")}, format("value", VisualizationConditionalTargetMarkFill)), invalid: cartesian(VisualizationCartesianMarkColumn, []VisualizationFieldRef{ref("value")}, format("x", VisualizationConditionalTargetMarkFill)), want: "not rendered by the cartesian y channel"},
		{name: "heatmap value channel", valid: cartesian(VisualizationCartesianMarkHeatmap, []VisualizationFieldRef{ref("row"), ref("value")}, format("value", VisualizationConditionalTargetMarkFill)), invalid: cartesian(VisualizationCartesianMarkHeatmap, []VisualizationFieldRef{ref("row"), ref("value")}, format("row", VisualizationConditionalTargetMarkFill)), want: "cartesian y[1] value channel"},
		{name: "waterfall metric channel", valid: cartesian(VisualizationCartesianMarkWaterfall, []VisualizationFieldRef{ref("start"), ref("value")}, format("value", VisualizationConditionalTargetMarkFill)), invalid: cartesian(VisualizationCartesianMarkWaterfall, []VisualizationFieldRef{ref("start"), ref("value")}, format("start", VisualizationConditionalTargetMarkFill)), want: "cartesian y[1] metric channel"},
		{name: "proportional value channel", valid: proportional("value", "row"), invalid: proportional("row", "row"), want: "proportional value channel"},
		{name: "table visible column", valid: table("table", "value", []string{"value"}), invalid: table("table", "column", []string{"value"}), want: "visible table column"},
		{name: "matrix metric alias", valid: table("matrix", "metric", nil), invalid: table("matrix", "column", nil), want: "visible matrix row or metric alias"},
		{name: "pivot metric alias", valid: table("pivot", "metric", nil), invalid: table("pivot", "column", nil), want: "visible pivot row or metric alias"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSpec(test.valid); err != nil {
				t.Fatalf("valid spec rejected: %v", err)
			}
			err := ValidateSpec(test.invalid)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
