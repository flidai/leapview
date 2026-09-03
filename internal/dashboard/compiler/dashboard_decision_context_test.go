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
