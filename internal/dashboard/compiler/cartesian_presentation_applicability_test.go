package compiler

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestLowerCanonicalCartesianPresentationRejectsInapplicableOptions(t *testing.T) {
	falseValue := false
	zero := 0.0
	orientation := document.DashboardOrientationHorizontal
	legend := document.DashboardLegendPositionRight
	legendItems := []document.DashboardLegendItem{{Value: "open"}}
	labelsHidden := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityHidden}
	labelsAutomatic := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAutomatic}
	labelsDense := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityDense}
	labelsAlways := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAlways}
	labelPosition := document.DashboardLabelPositionInside
	stacking := document.DashboardStackingModeNormal
	emptyLines := []document.DashboardReferenceLine{}
	emptyBands := []document.DashboardReferenceBand{}
	emptyEvents := []document.DashboardEventAnnotation{}
	seriesIntents := []document.DashboardSeriesIntent{{Value: "revenue"}}
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
		{name: "legend items on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.LegendItems = &legendItems }, want: "presentation.legendItems"},
		{name: "hidden labels on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsHidden }, want: "presentation.labels"},
		{name: "hidden labels on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsHidden }, want: "presentation.labels"},
		{name: "automatic labels on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsAutomatic }, want: "presentation.labels"},
		{name: "automatic labels on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsAutomatic }, want: "presentation.labels"},
		{name: "dense labels on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsDense }, want: "presentation.labels"},
		{name: "dense labels on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsDense }, want: "presentation.labels"},
		{name: "always labels on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsAlways }, want: "presentation.labels"},
		{name: "always labels on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.Labels = &labelsAlways }, want: "presentation.labels"},
		{name: "label position on candlestick", visualType: document.DashboardVisualTypeCandlestick, set: func(value *document.CartesianDashboardPresentation) { value.LabelPosition = &labelPosition }, want: "presentation.labelPosition"},
		{name: "label position on boxplot", visualType: document.DashboardVisualTypeBoxplot, set: func(value *document.CartesianDashboardPresentation) { value.LabelPosition = &labelPosition }, want: "presentation.labelPosition"},
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
		{name: "series intent on heatmap", visualType: document.DashboardVisualTypeHeatmap, set: func(value *document.CartesianDashboardPresentation) { value.SeriesIntent = &seriesIntents }, want: "presentation.seriesIntent"},
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

func TestLowerCanonicalCartesianPresentationAllowsHeatmapDataZoom(t *testing.T) {
	for _, dataZoom := range []bool{false, true} {
		t.Run(fmt.Sprintf("%t", dataZoom), func(t *testing.T) {
			lowered, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
				Type: "cartesian", DataZoom: &dataZoom,
			}}, document.DashboardVisualTypeHeatmap)
			if err != nil {
				t.Fatalf("heatmap data zoom=%t rejected: %v", dataZoom, err)
			}
			presentation, ok := lowered.(visualizationir.CartesianVisualizationPresentation)
			if !ok || presentation.DataZoom != dataZoom {
				t.Fatalf("lowered heatmap presentation = %#v, want dataZoom=%t", lowered, dataZoom)
			}
		})
	}
}

func TestLowerCanonicalPresentationAxisVisibleApplicability(t *testing.T) {
	for _, visible := range []bool{false, true} {
		visible := visible
		for _, visualType := range []document.DashboardVisualType{
			document.DashboardVisualTypeLine,
			document.DashboardVisualTypeArea,
			document.DashboardVisualTypeBar,
			document.DashboardVisualTypeColumn,
			document.DashboardVisualTypeCandlestick,
			document.DashboardVisualTypeBoxplot,
			document.DashboardVisualTypeCombo,
			document.DashboardVisualTypeHeatmap,
			document.DashboardVisualTypeWaterfall,
			document.DashboardVisualTypeHistogram,
		} {
			t.Run(string(visualType)+fmt.Sprintf("_%t", visible), func(t *testing.T) {
				value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
					DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: &visible}, Type: "cartesian",
				}}
				if _, err := LowerCanonicalDashboardPresentation(value, visualType); err != nil {
					t.Fatalf("cartesian %s axisVisible=%t rejected: %v", visualType, visible, err)
				}
			})
		}
		t.Run(fmt.Sprintf("point_%t", visible), func(t *testing.T) {
			value := document.DashboardPresentation{Value: &document.PointDashboardPresentation{
				DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: &visible}, Type: "point",
				Identity: []string{"id"}, X: "x", Y: "y",
			}}
			if _, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeScatter); err != nil {
				t.Fatalf("point axisVisible=%t rejected: %v", visible, err)
			}
		})
	}

	tests := []struct {
		name  string
		type_ document.DashboardVisualType
		value document.DashboardPresentation
	}{
		{name: "proportional", type_: document.DashboardVisualTypePie, value: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "proportional"}}},
		{name: "hierarchy", type_: document.DashboardVisualTypeTreemap, value: document.DashboardPresentation{Value: &document.HierarchyDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "hierarchy"}}},
		{name: "polar", type_: document.DashboardVisualTypeRadar, value: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "polar"}}},
		{name: "geographic", type_: document.DashboardVisualTypeMap, value: document.DashboardPresentation{Value: &document.GeographicDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "geographic"}}},
		{name: "table", type_: document.DashboardVisualTypeTable, value: document.DashboardPresentation{Value: &document.TableDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "table", RowHeight: 32}}},
		{name: "kpi", type_: document.DashboardVisualTypeKpi, value: document.DashboardPresentation{Value: &document.KPIDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{AxisVisible: boolPtr(false)}, Type: "kpi"}}},
	}
	for _, test := range tests {
		for _, visible := range []bool{false, true} {
			t.Run(test.name+fmt.Sprintf("_%t", visible), func(t *testing.T) {
				value := test.value
				base, err := value.Base()
				if err != nil {
					t.Fatal(err)
				}
				flag := visible
				base.AxisVisible = &flag
				_, err = LowerCanonicalDashboardPresentation(value, test.type_)
				want := "presentation.axisVisible is not supported for " + string(test.type_) + " visuals"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("axisVisible=%t error = %v, want %q", visible, err, want)
				}
			})
		}
	}
}

func boolPtr(value bool) *bool { return &value }

func TestLowerCanonicalCartesianPresentationRejectsDataZoomAcrossFamilies(t *testing.T) {
	dataZoom := true
	for _, visualType := range []document.DashboardVisualType{
		document.DashboardVisualTypePie,
		document.DashboardVisualTypeScatter,
		document.DashboardVisualTypeMap,
	} {
		t.Run(string(visualType), func(t *testing.T) {
			_, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
				Type: "cartesian", DataZoom: &dataZoom,
			}}, visualType)
			want := "visual type \"" + string(visualType) + "\" requires "
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want cross-family presentation rejection containing %q", err, want)
			}
		})
	}
}

func TestLowerCanonicalCartesianPresentationRejectsPercentDisplayUnits(t *testing.T) {
	stacking := document.DashboardStackingModePercent
	units := visualizationir.VisualizationDisplayUnitsAuto
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Stacking: &stacking, DisplayUnits: &units}}
	_, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeLine)
	if err == nil || !strings.Contains(err.Error(), "presentation.displayUnits is incompatible with percent stacking") {
		t.Fatalf("error = %v, want percent display-unit rejection", err)
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
