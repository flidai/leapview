package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestLowerCanonicalDecisionContextCompilesAuthoringContract(t *testing.T) {
	title := "Revenue"
	minimum, maximum := 0.0, 100.0
	number := func(value float64) document.DashboardReferenceValue {
		return document.DashboardReferenceValue{Value: &document.NumberDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "number"}, Kind: "number", Value: value}}
	}
	field := func(name string, reducer visualizationir.VisualizationReferenceReducer) document.DashboardReferenceValue {
		return document.DashboardReferenceValue{Value: &document.FieldDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "field"}, Kind: "field", Field: name, Reducer: reducer}}
	}
	text := func(value string) document.DashboardReferenceValue {
		return document.DashboardReferenceValue{Value: &document.TextDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "text"}, Kind: "text", Value: value}}
	}
	presentation := document.CartesianDashboardPresentation{
		Type:             "cartesian",
		Axes:             &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisPrimaryY, Title: &title, Scale: visualizationir.VisualizationAxisScaleLinear, Zero: visualizationir.VisualizationAxisZeroPolicyExclude, Minimum: &minimum, Maximum: &maximum, TickDensity: visualizationir.VisualizationAxisTickDensityDense}},
		ReferenceLines:   &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number(80), Tone: visualizationir.VisualizationToneSuccess}},
		ReferenceBands:   &[]document.DashboardReferenceBand{{ID: "forecast", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, From: field("revenue", visualizationir.VisualizationReferenceReducerMinimum), To: field("revenue", visualizationir.VisualizationReferenceReducerMaximum), Tone: visualizationir.VisualizationToneNeutral}},
		EventAnnotations: &[]document.DashboardEventAnnotation{{ID: "launch", Axis: visualizationir.VisualizationCartesianAxisX, Value: text("2026-09"), Label: "Launch", Tone: visualizationir.VisualizationToneNeutral}},
	}
	authored := document.DashboardPresentation{Value: &presentation}
	query := LoweredDashboardQuery{Type: "aggregate", Binding: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultCategoryValue, Aggregate: &visualizationdefinition.AggregateQueryBinding{Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "purchase_month", Alias: "purchase_month"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}}}, ResultFrame: []DashboardQueryResultField{{Source: "purchase_month", Name: "purchase_month"}, {Source: "revenue", Name: "revenue"}}}
	lowered, err := LowerCanonicalDashboardPresentation(authored, document.DashboardVisualTypeLine)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := canonicalVisualizationSpec("revenue", document.DashboardVisual{Type: document.DashboardVisualTypeLine, Presentation: authored}, query, lowered, nil, dashboardQueryTestModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeLine, query); err != nil {
		t.Fatal(err)
	}
	got := spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if got.Axes == nil || len(*got.Axes) != 1 || got.ReferenceLines == nil || got.ReferenceBands == nil || got.EventAnnotations == nil {
		t.Fatalf("decision context not compiled: %#v", got)
	}
	axis := (*got.Axes)[0]
	if axis.Type != visualizationir.VisualizationAxisTypeAutomatic || axis.Inversion != visualizationir.VisualizationAxisInversionAutomatic || axis.Ticks != visualizationir.VisualizationAxisTickVisibilityAutomatic || axis.Grid != visualizationir.VisualizationAxisGridVisibilityAutomatic || axis.LabelRotation != visualizationir.VisualizationAxisLabelRotationAutomatic || axis.DateUnit != visualizationir.VisualizationDateDisplayUnitAutomatic {
		t.Fatalf("axis compatibility defaults not compiled: %#v", axis)
	}
	band := (*got.ReferenceBands)[0]
	from := band.From.Value.(*visualizationir.FieldVisualizationReferenceValue)
	if from.Field.Dataset != "primary" || from.Field.Field != "revenue" || from.Reducer != visualizationir.VisualizationReferenceReducerMinimum {
		t.Fatalf("field reference = %#v", from)
	}
	if err := visualizationir.ValidateSpec(spec); err != nil {
		t.Fatalf("compiled IR invalid: %v", err)
	}
}

func TestLowerCanonicalDecisionContextRejectsInvalidAuthoring(t *testing.T) {
	number := document.DashboardReferenceValue{Value: &document.NumberDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "number"}, Kind: "number", Value: 1}}
	units := visualizationir.VisualizationDisplayUnitsMillions
	categoryType := visualizationir.VisualizationAxisTypeCategory
	timeType := visualizationir.VisualizationAxisTypeTime
	dateUnit := visualizationir.VisualizationDateDisplayUnitMonth
	minimum := 0.0
	zeroInclude := visualizationir.VisualizationAxisZeroPolicyInclude
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Name: "category"}, {Name: "value"}}}
	baseSpec := func(mark visualizationir.VisualizationCartesianMark) visualizationir.VisualizationSpec {
		return visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
			Kind: "cartesian", Mark: mark,
			VisualizationSpecBase: visualizationir.VisualizationSpecBase{Datasets: []visualizationir.VisualizationDatasetSchema{{
				ID: "primary", Fields: []visualizationir.VisualizationField{
					{ID: "category", DataType: visualizationir.VisualizationDataTypeString},
					{ID: "value", DataType: visualizationir.VisualizationDataTypeDecimal},
				},
			}}},
		}}
	}
	tests := []struct {
		name, want string
		visualType document.DashboardVisualType
		authored   document.CartesianDashboardPresentation
		mark       visualizationir.VisualizationCartesianMark
	}{
		{name: "unsupported reference", want: "presentation.referenceLines", visualType: document.DashboardVisualTypeHeatmap, mark: visualizationir.VisualizationCartesianMarkHeatmap, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number, Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "duplicate ids", want: "presentation.referenceBands[0].id duplicates presentation.referenceLines[0].id", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "same", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number, Tone: visualizationir.VisualizationToneNeutral}}, ReferenceBands: &[]document.DashboardReferenceBand{{ID: "same", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, From: number, To: number, Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "unknown field", want: "presentation.referenceLines[0].value.field", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: document.DashboardReferenceValue{Value: &document.FieldDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "field"}, Kind: "field", Field: "missing", Reducer: visualizationir.VisualizationReferenceReducerMean}}, Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "nonnumeric display units", want: "presentation.axes[0].displayUnits requires a numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic, DisplayUnits: &units}}}},
		{name: "category scale", want: "presentation.axes[0].scale linear requires an effective numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Type: &categoryType, Scale: visualizationir.VisualizationAxisScaleLinear, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "time type on category", want: "presentation.axes[0].type time requires a date or temporal axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Type: &timeType, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "date unit on category", want: "presentation.axes[0].dateUnit requires an effective time axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, DateUnit: &dateUnit, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "domain on category", want: "presentation.axes[0] domain bounds require an effective numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, Minimum: &minimum, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "numeric axis forced category", want: "presentation.axes[0].displayUnits requires a numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisPrimaryY, Type: &categoryType, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, DisplayUnits: &units, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "zero on category", want: "presentation.axes[0].zero requires an effective numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: zeroInclude, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "log on category", want: "presentation.axes[0].scale log requires an effective numeric axis", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleLog, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}}}},
		{name: "unknown tone", want: "presentation.referenceLines[0].tone", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number, Tone: visualizationir.VisualizationTone("invalid")}}}},
		{name: "reversed band", want: "presentation.referenceBands[0].from must be less than", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceBands: &[]document.DashboardReferenceBand{{ID: "range", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, From: document.DashboardReferenceValue{Value: &document.NumberDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "number"}, Kind: "number", Value: 2}}, To: number, Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "numeric reducer on text", want: "requires a numeric result field", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "mean", Axis: visualizationir.VisualizationCartesianAxisX, Value: document.DashboardReferenceValue{Value: &document.FieldDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "field"}, Kind: "field", Field: "category", Reducer: visualizationir.VisualizationReferenceReducerMean}}, Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "event on value axis", want: "presentation.eventAnnotations[0].axis must be x", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", EventAnnotations: &[]document.DashboardEventAnnotation{{ID: "event", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number, Label: "Event", Tone: visualizationir.VisualizationToneNeutral}}}},
		{name: "secondary on non combo", want: "secondary_y requires a combo visual", visualType: document.DashboardVisualTypeLine, mark: visualizationir.VisualizationCartesianMarkLine, authored: document.CartesianDashboardPresentation{Type: "cartesian", ReferenceLines: &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisSecondaryY, Value: number, Tone: visualizationir.VisualizationToneNeutral}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := baseSpec(test.mark)
			authored := document.DashboardPresentation{Value: &test.authored}
			err := lowerCanonicalDecisionContext(&spec, authored, test.visualType, query)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLowerCanonicalDecisionContextSupportsScatter(t *testing.T) {
	number := document.DashboardReferenceValue{Value: &document.NumberDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "number"}, Kind: "number", Value: 50}}
	axes := []document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisPrimaryY, Scale: visualizationir.VisualizationAxisScaleLinear, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityNormal}}
	lines := []document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: number, Tone: visualizationir.VisualizationToneWarning}}
	point := document.PointDashboardPresentation{Type: "point", Identity: []string{"category"}, X: "revenue", Y: "revenue", Axes: &axes, ReferenceLines: &lines}
	authored := document.DashboardPresentation{Value: &point}
	query := LoweredDashboardQuery{Type: "aggregate", Binding: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultCategoryValue, Aggregate: &visualizationdefinition.AggregateQueryBinding{Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "category", Alias: "category"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}}}, ResultFrame: []DashboardQueryResultField{{Source: "category", Name: "category"}, {Source: "revenue", Name: "revenue"}}}
	lowered, err := LowerCanonicalDashboardPresentation(authored, document.DashboardVisualTypeScatter)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := canonicalVisualizationSpec("scatter", document.DashboardVisual{Type: document.DashboardVisualTypeScatter, Presentation: authored}, query, lowered, nil, dashboardQueryTestModel())
	if err != nil {
		t.Fatal(err)
	}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeScatter, query); err != nil {
		t.Fatal(err)
	}
	got := spec.Value.(*visualizationir.PointVisualizationSpec)
	if got.Axes == nil || got.ReferenceLines == nil {
		t.Fatalf("scatter decision context not compiled: %#v", got)
	}
	if err := visualizationir.ValidateSpec(spec); err != nil {
		t.Fatalf("compiled scatter IR invalid: %v", err)
	}
}

func TestLowerCanonicalDecisionContextUsesCartesianXCategoryDefault(t *testing.T) {
	units := visualizationir.VisualizationDisplayUnitsMillions
	typeValue := visualizationir.VisualizationAxisTypeValue
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: visualizationir.VisualizationSpecBase{Datasets: []visualizationir.VisualizationDatasetSchema{{
			ID: "primary", Fields: []visualizationir.VisualizationField{
				{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal},
			},
		}}},
		Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkLine,
		X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"},
		Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
	}}
	axis := document.DashboardAxisConfiguration{ID: visualizationir.VisualizationCartesianAxisX, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, DisplayUnits: &units, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}
	authored := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Axes: &[]document.DashboardAxisConfiguration{axis}}}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeLine, LoweredDashboardQuery{}); err == nil || !strings.Contains(err.Error(), "displayUnits requires a numeric axis") {
		t.Fatalf("automatic numeric cartesian X should retain category semantics, error = %v", err)
	}
	axis.Type = &typeValue
	authored.Value.(*document.CartesianDashboardPresentation).Axes = &[]document.DashboardAxisConfiguration{axis}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeLine, LoweredDashboardQuery{}); err != nil {
		t.Fatalf("explicit value cartesian X should permit numeric display units: %v", err)
	}
	stacking := visualizationir.VisualizationStackingModePercent
	spec.Value.(*visualizationir.CartesianVisualizationSpec).Presentation.Stacking = &stacking
	axis.ID = visualizationir.VisualizationCartesianAxisPrimaryY
	authored.Value.(*document.CartesianDashboardPresentation).Axes = &[]document.DashboardAxisConfiguration{axis}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeLine, LoweredDashboardQuery{}); err == nil || !strings.Contains(err.Error(), "incompatible with percent stacking") {
		t.Fatalf("percent primary axis display units should be rejected, error = %v", err)
	}
}

func TestLowerCanonicalDecisionContextUsesFirstPrimaryComboOwner(t *testing.T) {
	t.Parallel()

	primaryType := visualizationir.VisualizationAxisTypeValue
	base := visualizationir.VisualizationSpecBase{Datasets: []visualizationir.VisualizationDatasetSchema{{
		ID: "primary", Fields: []visualizationir.VisualizationField{
			{ID: "month", DataType: visualizationir.VisualizationDataTypeString},
			{ID: "secondary_value", DataType: visualizationir.VisualizationDataTypeString},
			{ID: "primary_value", DataType: visualizationir.VisualizationDataTypeDecimal},
		},
	}}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: base,
		Kind:                  "cartesian",
		Mark:                  visualizationir.VisualizationCartesianMarkCombo,
		X:                     visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "month"},
		Y: []visualizationir.VisualizationFieldRef{
			{Dataset: "primary", Field: "secondary_value"},
			{Dataset: "primary", Field: "primary_value"},
		},
		Presentation: visualizationir.CartesianVisualizationPresentation{ComboSeries: &[]visualizationir.VisualizationComboSeries{
			{SeriesValue: "secondary_value", Axis: visualizationir.VisualizationAxisSecondary},
			{SeriesValue: "primary_value", Axis: visualizationir.VisualizationAxisPrimary},
		}},
	}}
	authored := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
		Type: "cartesian",
		Axes: &[]document.DashboardAxisConfiguration{{ID: visualizationir.VisualizationCartesianAxisPrimaryY, Type: &primaryType, Scale: visualizationir.VisualizationAxisScaleAutomatic, Zero: visualizationir.VisualizationAxisZeroPolicyAutomatic, TickDensity: visualizationir.VisualizationAxisTickDensityAutomatic}},
	}}
	if err := lowerCanonicalDecisionContext(&spec, authored, document.DashboardVisualTypeCombo, LoweredDashboardQuery{}); err != nil {
		t.Fatalf("primary combo owner should be taken from canonical Y order: %v", err)
	}

	allSecondary := spec
	allSecondary.Value = &visualizationir.CartesianVisualizationSpec{
		VisualizationSpecBase: base,
		Kind:                  "cartesian",
		Mark:                  visualizationir.VisualizationCartesianMarkCombo,
		X:                     visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "month"},
		Y:                     spec.Value.(*visualizationir.CartesianVisualizationSpec).Y,
		Presentation:          visualizationir.CartesianVisualizationPresentation{ComboSeries: &[]visualizationir.VisualizationComboSeries{{SeriesValue: "secondary_value", Axis: visualizationir.VisualizationAxisSecondary}, {SeriesValue: "primary_value", Axis: visualizationir.VisualizationAxisSecondary}}},
	}
	primaryNumber := document.DashboardReferenceValue{Value: &document.NumberDashboardReferenceValue{DashboardReferenceValueBase: document.DashboardReferenceValueBase{Kind: "number"}, Kind: "number", Value: 10}}
	for _, test := range []struct {
		name string
		set  func(*document.CartesianDashboardPresentation)
		want string
	}{
		{name: "primary reference line", set: func(value *document.CartesianDashboardPresentation) {
			value.ReferenceLines = &[]document.DashboardReferenceLine{{ID: "target", Axis: visualizationir.VisualizationCartesianAxisPrimaryY, Value: primaryNumber}}
		}, want: "presentation.referenceLines[0].axis requires a primary_y combo series"},
		{name: "x reference line owner", set: func(value *document.CartesianDashboardPresentation) {
			value.ReferenceLines = &[]document.DashboardReferenceLine{{ID: "launch", Axis: visualizationir.VisualizationCartesianAxisX, Value: primaryNumber}}
		}, want: "presentation.referenceLines[0].axis requires a primary_y combo series"},
		{name: "horizontal event annotation owner", set: func(value *document.CartesianDashboardPresentation) {
			value.EventAnnotations = &[]document.DashboardEventAnnotation{{ID: "launch", Axis: visualizationir.VisualizationCartesianAxisX, Value: primaryNumber, Label: "Launch"}}
		}, want: "presentation.eventAnnotations[0].axis requires a primary_y combo series"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := &document.CartesianDashboardPresentation{Type: "cartesian"}
			test.set(value)
			if err := lowerCanonicalDecisionContext(&allSecondary, document.DashboardPresentation{Value: value}, document.DashboardVisualTypeCombo, LoweredDashboardQuery{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
