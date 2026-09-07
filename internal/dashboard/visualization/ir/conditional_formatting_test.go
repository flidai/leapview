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

func TestValidateConditionalFormattingTargetAllowlists(t *testing.T) {
	t.Parallel()

	allTargets := []VisualizationConditionalTarget{
		VisualizationConditionalTargetMarkFill,
		VisualizationConditionalTarget("mark_stroke"),
		VisualizationConditionalTargetSeriesColor,
		VisualizationConditionalTargetLabelForeground,
		VisualizationConditionalTargetVisualBackground,
		VisualizationConditionalTargetCellForeground,
		VisualizationConditionalTargetCellBackground,
		VisualizationConditionalTargetKpiValue,
		VisualizationConditionalTargetIcon,
	}
	tests := []struct {
		kind    string
		allowed []VisualizationConditionalTarget
	}{
		{kind: "point", allowed: []VisualizationConditionalTarget{VisualizationConditionalTargetMarkFill}},
		{kind: "proportional", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetMarkFill, VisualizationConditionalTargetSeriesColor,
		}},
		{kind: "kpi", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetVisualBackground, VisualizationConditionalTargetKpiValue,
		}},
		{kind: "table", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetCellForeground, VisualizationConditionalTargetCellBackground, VisualizationConditionalTargetIcon,
		}},
		{kind: "matrix", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetCellForeground, VisualizationConditionalTargetCellBackground, VisualizationConditionalTargetIcon,
		}},
		{kind: "pivot", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetCellForeground, VisualizationConditionalTargetCellBackground, VisualizationConditionalTargetIcon,
		}},
		{kind: "cartesian", allowed: []VisualizationConditionalTarget{
			VisualizationConditionalTargetMarkFill,
			VisualizationConditionalTargetSeriesColor, VisualizationConditionalTargetLabelForeground,
			VisualizationConditionalTargetIcon,
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.kind, func(t *testing.T) {
			t.Parallel()
			allowed := make(map[VisualizationConditionalTarget]struct{}, len(test.allowed))
			for _, target := range test.allowed {
				allowed[target] = struct{}{}
			}
			for _, target := range allTargets {
				target := target
				t.Run(string(target), func(t *testing.T) {
					_, wantAllowed := allowed[target]
					err := validateConditionalFormattingTarget(test.kind, VisualizationConditionalFormat{Target: target})
					if wantAllowed {
						if err != nil {
							t.Fatalf("target %q rejected for %s: %v", target, test.kind, err)
						}
						return
					}
					if err == nil {
						t.Fatalf("target %q accepted for %s; error = %v", target, test.kind, err)
					}
				})
			}
		})
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
		{name: "waterfall metric channel", valid: cartesian(VisualizationCartesianMarkWaterfall, []VisualizationFieldRef{ref("start"), ref("value")}, format("value", VisualizationConditionalTargetMarkFill)), invalid: cartesian(VisualizationCartesianMarkWaterfall, []VisualizationFieldRef{ref("start"), ref("value")}, format("start", VisualizationConditionalTargetMarkFill)), want: "cartesian value channel"},
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

func TestValidateSpecRejectsRemovedCartesianMarkStrokeTarget(t *testing.T) {
	t.Parallel()

	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	removedTarget := VisualizationConditionalTarget("mark_stroke")
	tests := []struct {
		name string
		mark VisualizationCartesianMark
		y    []VisualizationFieldRef
	}{
		{name: "line", mark: VisualizationCartesianMarkLine, y: []VisualizationFieldRef{ref("value")}},
		{name: "area", mark: VisualizationCartesianMarkArea, y: []VisualizationFieldRef{ref("value")}},
		{name: "bar", mark: VisualizationCartesianMarkBar, y: []VisualizationFieldRef{ref("value")}},
		{name: "column", mark: VisualizationCartesianMarkColumn, y: []VisualizationFieldRef{ref("value")}},
		{name: "combo", mark: VisualizationCartesianMarkCombo, y: []VisualizationFieldRef{ref("value")}},
		{name: "waterfall", mark: VisualizationCartesianMarkWaterfall, y: []VisualizationFieldRef{ref("start"), ref("value")}},
		{name: "heatmap", mark: VisualizationCartesianMarkHeatmap, y: []VisualizationFieldRef{ref("row"), ref("value")}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			format := VisualizationConditionalFormat{
				ID: "stroke", Target: removedTarget, Field: ref("value"),
				Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
					VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
					Low: VisualizationConditionalStyle{Color: colorIntent(VisualizationColorIntentDanger)}, High: VisualizationConditionalStyle{Color: colorIntent(VisualizationColorIntentDanger)}, NullStyle: VisualizationConditionalStyle{Color: colorIntent(VisualizationColorIntentDanger)},
				}},
			}
			base := VisualizationSpecBase{
				Kind: "cartesian", Title: test.name, Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
					{ID: "x", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "X"},
					{ID: "row", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Row"},
					{ID: "start", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Start"},
					{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
				}}},
				DataBudget: VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: VisualizationCompletenessComplete}, Accessibility: VisualizationAccessibility{Title: test.name, Description: test.name},
				Interactions: []VisualizationInteraction{}, ConditionalFormatting: &[]VisualizationConditionalFormat{format},
			}
			spec := VisualizationSpec{Value: &CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: test.mark, X: ref("x"), Y: test.y, Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom)}}}
			err := ValidateSpec(spec)
			if err == nil || !strings.Contains(err.Error(), `conditional formatting "stroke" target: unsupported target "mark_stroke"`) {
				t.Fatalf("ValidateSpec() error = %v, want removed target diagnostic", err)
			}
		})
	}
}

func colorIntent(value VisualizationColorIntent) *VisualizationColorIntent { return &value }

func TestValidateSpecAcceptsDirectIRWaterfallValueBeforeStart(t *testing.T) {
	t.Parallel()
	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	color := VisualizationColorIntentDanger
	format := VisualizationConditionalFormat{
		ID: "value", Target: VisualizationConditionalTargetMarkFill, Field: ref("value"),
		Rule: VisualizationConditionalRule{Value: &GradientVisualizationConditionalRule{
			VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 1,
			Low: VisualizationConditionalStyle{Color: &color}, High: VisualizationConditionalStyle{Color: &color}, NullStyle: VisualizationConditionalStyle{Color: &color},
		}},
	}
	base := VisualizationSpecBase{
		Kind: "cartesian", Title: "Waterfall", Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
			{ID: "label", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Label"},
			{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
			{ID: "start", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Start"},
		}}},
		DataBudget: VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete}, Accessibility: VisualizationAccessibility{Title: "Waterfall", Description: "Waterfall"},
		Interactions: []VisualizationInteraction{}, ConditionalFormatting: &[]VisualizationConditionalFormat{format},
	}
	spec := VisualizationSpec{Value: &CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: VisualizationCartesianMarkWaterfall, X: ref("label"), Y: []VisualizationFieldRef{ref("value"), ref("start")},
		Presentation: CartesianVisualizationPresentation{VisualizationPresentation: testVisualizationPresentation(VisualizationLegendPositionBottom)},
	}}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("direct-IR waterfall [value, start] rejected: %v", err)
	}
}

func TestValidateSpecRejectsUndeliveredConditionalFormattingSources(t *testing.T) {
	t.Parallel()
	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	color := VisualizationColorIntentDanger
	icon := VisualizationIconIntentWarning
	fieldRule := func(source string) VisualizationConditionalFormat {
		return VisualizationConditionalFormat{
			ID: "source-check", Target: VisualizationConditionalTargetCellBackground, Field: ref("value"),
			Rule: VisualizationConditionalRule{Value: &FieldVisualizationConditionalRule{
				VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "field"}, Kind: "field", Source: ref(source),
				Values: map[string]VisualizationConditionalStyle{"late": {Color: &color, Icon: &icon}}, NullStyle: VisualizationConditionalStyle{Color: &color, Icon: &icon}, DefaultStyle: VisualizationConditionalStyle{Color: &color, Icon: &icon},
			}},
		}
	}
	base := func(kind string, formats []VisualizationConditionalFormat) VisualizationSpecBase {
		return VisualizationSpecBase{
			Kind: kind, Title: kind, Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
				{ID: "row", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Row"},
				{ID: "column", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Column"},
				{ID: "status", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Status"},
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
				{ID: "hidden_calculation", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Hidden calculation", Provenance: &VisualizationFieldProvenance{Kind: VisualizationFieldProvenanceKindVisualCalculation}},
			}}},
			DataBudget: VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete}, Accessibility: VisualizationAccessibility{Title: kind, Description: kind},
			Interactions: []VisualizationInteraction{}, ConditionalFormatting: &formats,
		}
	}
	presentation := GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}
	table := func(source string, columns []VisualizationFieldRef) VisualizationSpec {
		return VisualizationSpec{Value: &TableVisualizationSpec{VisualizationSpecBase: base("table", []VisualizationConditionalFormat{fieldRule(source)}), Kind: "table", Columns: func() []TableVisualizationColumn {
			result := make([]TableVisualizationColumn, len(columns))
			for index, column := range columns {
				result[index] = TableVisualizationColumn{Field: column, Label: column.Field}
			}
			return result
		}(), Presentation: presentation}}
	}
	pivot := func(source string) VisualizationSpec {
		return VisualizationSpec{Value: &PivotVisualizationSpec{VisualizationSpecBase: base("pivot", []VisualizationConditionalFormat{fieldRule(source)}), Kind: "pivot", Rows: []VisualizationFieldRef{ref("row")}, Columns: []VisualizationFieldRef{ref("column")}, Metrics: []VisualizationFieldRef{ref("value")}, MetricFormatting: map[string][]TableVisualizationFormattingRule{}, Presentation: presentation}}
	}
	if err := ValidateSpec(table("status", []VisualizationFieldRef{ref("value"), ref("status")})); err != nil {
		t.Fatalf("delivered detail-table source rejected: %v", err)
	}
	if err := ValidateSpec(table("hidden_calculation", []VisualizationFieldRef{ref("value")})); err == nil || !strings.Contains(err.Error(), "source: field \"hidden_calculation\" is not delivered in table rows") {
		t.Fatalf("hidden detail calculation source error = %v", err)
	}
	if err := ValidateSpec(pivot("column")); err == nil || !strings.Contains(err.Error(), "source: field \"column\" is not delivered in pivot rows") {
		t.Fatalf("hidden pivot column source error = %v", err)
	}
	if err := ValidateSpec(pivot("value")); err != nil {
		t.Fatalf("delivered pivot metric source rejected: %v", err)
	}
}

func TestValidateSpecRejectsMetricSourcesForTabularRowTargets(t *testing.T) {
	t.Parallel()
	ref := func(field string) VisualizationFieldRef {
		return VisualizationFieldRef{Dataset: "primary", Field: field}
	}
	color := VisualizationColorIntentDanger
	icon := VisualizationIconIntentWarning
	format := func(kind, target, source string) VisualizationSpec {
		conditional := VisualizationConditionalFormat{
			ID: "row-metric-source", Target: VisualizationConditionalTargetCellBackground, Field: ref(target),
			Rule: VisualizationConditionalRule{Value: &FieldVisualizationConditionalRule{
				VisualizationConditionalRuleBase: VisualizationConditionalRuleBase{Kind: "field"}, Kind: "field", Source: ref(source),
				Values: map[string]VisualizationConditionalStyle{"late": {Color: &color, Icon: &icon}}, NullStyle: VisualizationConditionalStyle{Color: &color, Icon: &icon}, DefaultStyle: VisualizationConditionalStyle{Color: &color, Icon: &icon},
			}},
		}
		base := VisualizationSpecBase{
			Kind: kind, Title: kind, Datasets: []VisualizationDatasetSchema{{ID: "primary", Fields: []VisualizationField{
				{ID: "row", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Row"},
				{ID: "column", Role: VisualizationFieldRoleDimension, DataType: VisualizationDataTypeString, Label: "Column"},
				{ID: "value", Role: VisualizationFieldRoleMetric, DataType: VisualizationDataTypeDecimal, Label: "Value"},
			}}},
			DataBudget:    VisualizationDataBudget{MaxRows: 10, RequiredCompleteness: VisualizationCompletenessComplete},
			Accessibility: VisualizationAccessibility{Title: kind, Description: kind},
			Interactions:  []VisualizationInteraction{}, ConditionalFormatting: &[]VisualizationConditionalFormat{conditional},
		}
		presentation := GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}
		if kind == "matrix" {
			return VisualizationSpec{Value: &MatrixVisualizationSpec{VisualizationSpecBase: base, Kind: kind, Rows: []VisualizationFieldRef{ref("row")}, Columns: []VisualizationFieldRef{ref("column")}, Metrics: []VisualizationFieldRef{ref("value")}, MetricFormatting: map[string][]TableVisualizationFormattingRule{}, Presentation: presentation}}
		}
		return VisualizationSpec{Value: &PivotVisualizationSpec{VisualizationSpecBase: base, Kind: kind, Rows: []VisualizationFieldRef{ref("row")}, Columns: []VisualizationFieldRef{ref("column")}, Metrics: []VisualizationFieldRef{ref("value")}, MetricFormatting: map[string][]TableVisualizationFormattingRule{}, Presentation: presentation}}
	}

	for _, test := range []struct {
		kind string
		want string
	}{
		{kind: "matrix", want: "metric source \"value\" cannot drive matrix row target \"row\""},
		{kind: "pivot", want: "metric source \"value\" cannot drive pivot row target \"row\""},
	} {
		t.Run(test.kind+" rejects row target", func(t *testing.T) {
			if err := ValidateSpec(format(test.kind, "row", "value")); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSpec() error = %v, want containing %q", err, test.want)
			}
		})
		t.Run(test.kind+" keeps metric remap", func(t *testing.T) {
			if err := ValidateSpec(format(test.kind, "value", "value")); err != nil {
				t.Fatalf("metric target with metric source rejected: %v", err)
			}
		})
	}
}
