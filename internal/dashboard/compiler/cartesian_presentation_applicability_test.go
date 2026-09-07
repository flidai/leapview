package compiler

import (
	"math"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestLowerCanonicalCartesianPresentationRejectsInapplicableOptions(t *testing.T) {
	falseValue := false
	zero := 0.0
	orientation := document.DashboardOrientationHorizontal
	legend := document.DashboardLegendPositionRight
	stacking := document.DashboardStackingModeNormal
	emptyLines := []document.DashboardReferenceLine{}
	emptyBands := []document.DashboardReferenceBand{}
	emptyEvents := []document.DashboardEventAnnotation{}
	allColumnSeries := []document.DashboardComboSeries{{Field: "revenue", Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxisPrimary}}

	tests := []struct {
		name       string
		visualType document.DashboardVisualType
		set        func(*document.CartesianDashboardPresentation)
		want       string
	}{
		{name: "legend on waterfall", visualType: document.DashboardVisualTypeWaterfall, set: func(value *document.CartesianDashboardPresentation) { value.Legend = &legend }, want: "presentation.legend"},
		{name: "legend on histogram", visualType: document.DashboardVisualTypeHistogram, set: func(value *document.CartesianDashboardPresentation) { value.Legend = &legend }, want: "presentation.legend"},
		{name: "legend on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.Legend = &legend }, want: "presentation.legend"},
		{name: "legend on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Legend = &legend }, want: "presentation.legend"},
		{name: "stacking on waterfall", visualType: document.DashboardVisualTypeWaterfall, set: func(value *document.CartesianDashboardPresentation) { value.Stacking = &stacking }, want: "presentation.stacking"},
		{name: "stacking on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.Stacking = &stacking }, want: "presentation.stacking"},
		{name: "stacking on histogram", visualType: document.DashboardVisualTypeHistogram, set: func(value *document.CartesianDashboardPresentation) { value.Stacking = &stacking }, want: "presentation.stacking"},
		{name: "stacking on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Stacking = &stacking }, want: "presentation.stacking"},
		{name: "stacking on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Stacking = &stacking }, want: "presentation.stacking"},
		{name: "orientation on bar", visualType: document.DashboardVisualTypeBar, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "orientation on waterfall", visualType: document.DashboardVisualTypeWaterfall, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "orientation on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "orientation on histogram", visualType: document.DashboardVisualTypeHistogram, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "orientation on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "orientation on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Orientation = &orientation }, want: "presentation.orientation"},
		{name: "data zoom on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.DataZoom = &falseValue }, want: "presentation.dataZoom"},
		{name: "show symbols on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.ShowSymbols = &falseValue }, want: "presentation.showSymbols"},
		{name: "smooth on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.Smooth = &falseValue }, want: "presentation.smooth"},
		{name: "step on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.Step = &falseValue }, want: "presentation.step"},
		{name: "symbol size on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.SymbolSize = &zero }, want: "presentation.symbolSize"},
		{name: "show symbols on column", visualType: document.DashboardVisualTypeColumn, set: func(value *document.CartesianDashboardPresentation) { value.ShowSymbols = &falseValue }, want: "presentation.showSymbols"},
		{name: "smooth on bar", visualType: document.DashboardVisualTypeBar, set: func(value *document.CartesianDashboardPresentation) { value.Smooth = &falseValue }, want: "presentation.smooth"},
		{name: "step on histogram", visualType: document.DashboardVisualTypeHistogram, set: func(value *document.CartesianDashboardPresentation) { value.Step = &falseValue }, want: "presentation.step"},
		{name: "symbol size on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.SymbolSize = &zero }, want: "presentation.symbolSize"},
		{name: "line control on all-column combo", visualType: document.DashboardVisualTypeCombo, set: func(value *document.CartesianDashboardPresentation) {
			value.Series = &allColumnSeries
			value.ShowSymbols = &falseValue
		}, want: "presentation.showSymbols"},
		{name: "series on line", visualType: document.DashboardVisualTypeLine, set: func(value *document.CartesianDashboardPresentation) { value.Series = &allColumnSeries }, want: "presentation.series"},
		{name: "empty lines on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.ReferenceLines = &emptyLines }, want: "presentation.referenceLines"},
		{name: "empty bands on histogram", visualType: document.DashboardVisualTypeHistogram, set: func(value *document.CartesianDashboardPresentation) { value.ReferenceBands = &emptyBands }, want: "presentation.referenceBands"},
		{name: "empty events on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.EventAnnotations = &emptyEvents }, want: "presentation.eventAnnotations"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := &document.CartesianDashboardPresentation{Type: "cartesian"}
			test.set(value)
			_, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: value}, test.visualType)
			want := test.want + " is not supported for " + string(test.visualType) + " visuals"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestLowerCanonicalCartesianPresentationAllowsComboLineControlsForLineSeries(t *testing.T) {
	showSymbols, smooth, step, symbolSize := true, true, true, 14.0
	series := []document.DashboardComboSeries{
		{Field: "revenue", Mark: document.DashboardComboSeriesMarkLine, Axis: document.DashboardComboSeriesAxisPrimary},
		{Field: "orders", Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxisSecondary},
	}
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
		Type: "cartesian", Series: &series, ShowSymbols: &showSymbols, Smooth: &smooth, Step: &step, SymbolSize: &symbolSize,
	}}
	if _, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeCombo); err != nil {
		t.Fatalf("combo line controls rejected: %v", err)
	}
}

func TestLowerCanonicalCartesianPresentationDefersInvalidComboSeriesDiagnostics(t *testing.T) {
	showSymbols := true
	cases := []struct {
		name   string
		series []document.DashboardComboSeries
		want   string
	}{
		{name: "empty", series: []document.DashboardComboSeries{}, want: "combo presentation.series must contain at least one entry"},
		{name: "unknown mark", series: []document.DashboardComboSeries{{Field: "revenue", Mark: document.DashboardComboSeriesMark("spline"), Axis: document.DashboardComboSeriesAxisPrimary}}, want: "unsupported mark \"spline\""},
		{name: "invalid axis", series: []document.DashboardComboSeries{{Field: "revenue", Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxis("tertiary")}}, want: "unsupported axis \"tertiary\""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Series: &test.series, ShowSymbols: &showSymbols}}
			_, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeCombo)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLowerCanonicalCartesianPresentationValidatesSymbolSize(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  string
	}{
		{name: "nan", value: math.NaN(), want: "presentation.symbolSize must be finite"},
		{name: "infinity", value: math.Inf(1), want: "presentation.symbolSize must be finite"},
		{name: "zero", value: 0, want: "presentation.symbolSize must be greater than zero"},
		{name: "negative", value: -1, want: "presentation.symbolSize must be greater than zero"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			symbolSize := test.value
			value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", SymbolSize: &symbolSize}}
			_, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeLine)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
