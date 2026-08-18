package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestLowerCanonicalCartesianPresentationPreservesEveryField(t *testing.T) {
	legend := document.DashboardLegendPositionRight
	density := document.DashboardLabelDensityDense
	priority := []document.DashboardLabelPriority{document.DashboardLabelPriorityThreshold}
	labels := document.DashboardLabelPolicy{Density: density, Priority: &priority}
	stacking := document.DashboardStackingModePercent
	orientation := document.DashboardOrientationHorizontal
	showSymbols, smooth, dataZoom := true, true, true
	symbolSize := 14.0
	position := document.DashboardLabelPositionInside
	units := visualizationir.VisualizationDisplayUnitsMillions
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Legend: &legend, Labels: &labels, Stacking: &stacking, Orientation: &orientation, ShowSymbols: &showSymbols, Smooth: &smooth, DataZoom: &dataZoom, SymbolSize: &symbolSize, LabelPosition: &position, DisplayUnits: &units}}
	lowered, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeBar)
	if err != nil {
		t.Fatalf("lower presentation: %v", err)
	}
	got, ok := lowered.(visualizationir.CartesianVisualizationPresentation)
	if !ok {
		t.Fatalf("lowered type = %T", lowered)
	}
	if got.Legend != visualizationir.VisualizationLegendPositionRight || got.LabelPolicy.Density != visualizationir.VisualizationLabelDensityDense || len(got.LabelPolicy.Priority) != 1 || got.LabelPolicy.Priority[0] != visualizationir.VisualizationLabelPriorityThreshold || got.Stacking == nil || *got.Stacking != visualizationir.VisualizationStackingModePercent || got.Orientation == nil || *got.Orientation != visualizationir.VisualizationOrientationHorizontal || !got.ShowSymbols || !got.Smooth || !got.DataZoom || got.SymbolSize == nil || *got.SymbolSize != 14 || got.LabelPosition == nil || *got.LabelPosition != visualizationir.VisualizationLabelPositionInside || got.DisplayUnits == nil || *got.DisplayUnits != visualizationir.VisualizationDisplayUnitsMillions {
		t.Fatalf("lowered presentation dropped fields: %#v", got)
	}
}

func TestLowerCanonicalPointPresentationPreservesOverplotAndLabels(t *testing.T) {
	legend := document.DashboardLegendPositionRight
	labels := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAutomatic}
	opacity := 0.58
	largeMode := visualizationir.VisualizationPointLargeModeAutomatic
	threshold := int64(2000)
	value := document.DashboardPresentation{Value: &document.PointDashboardPresentation{
		Type: "point", Legend: &legend, Labels: &labels,
		Identity: []string{"order_id"}, X: "delivery_days", Y: "revenue",
		Color: pointStringPtr("status"), Tooltip: &[]string{"order_id", "status", "delivery_days", "revenue"},
		Overplot: &document.PointDashboardOverplot{Strategy: visualizationir.VisualizationPointOverplotStrategyOpacity, Opacity: &opacity, LargeMode: &largeMode, LargeThreshold: &threshold},
	}}
	lowered, err := LowerCanonicalDashboardPresentation(value, document.DashboardVisualTypeScatter)
	if err != nil {
		t.Fatalf("lower point presentation: %v", err)
	}
	got, ok := lowered.(visualizationir.PointVisualizationPresentation)
	if !ok {
		t.Fatalf("lowered type = %T", lowered)
	}
	if got.Legend != visualizationir.VisualizationLegendPositionRight || got.LabelPolicy.Density != visualizationir.VisualizationLabelDensityAutomatic || got.Overplot != visualizationir.VisualizationPointOverplotStrategyOpacity || got.Opacity != opacity || got.LargeMode != largeMode || got.LargeThreshold != threshold {
		t.Fatalf("point presentation dropped fields: %#v", got)
	}
}

func TestLowerCanonicalPointPresentationRejectsInvalidOverplot(t *testing.T) {
	strategy := visualizationir.VisualizationPointOverplotStrategy("invalid")
	_, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.PointDashboardPresentation{
		Type: "point", Identity: []string{"id"}, X: "x", Y: "y",
		Overplot: &document.PointDashboardOverplot{Strategy: strategy},
	}}, document.DashboardVisualTypeScatter)
	if err == nil {
		t.Fatal("invalid point overplot strategy accepted")
	}
}

func pointStringPtr(value string) *string { return &value }

func TestLowerCanonicalPresentationVariantsPreserveTableAndKPIFields(t *testing.T) {
	table, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: 36, ShowHeader: false, Striped: true}}, document.DashboardVisualTypeTable)
	if err != nil {
		t.Fatal(err)
	}
	grid := table.(visualizationir.GridVisualizationPresentation)
	if grid.RowHeight != 36 || grid.ShowHeader || !grid.Striped {
		t.Fatalf("table presentation = %#v", grid)
	}
	units := visualizationir.VisualizationDisplayUnitsThousands
	note, tone := "Target", visualizationir.VisualizationToneWarning
	kpi, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.KPIDashboardPresentation{Type: "kpi", DisplayUnits: &units, Note: &note, Tone: &tone}}, document.DashboardVisualTypeKpi)
	if err != nil {
		t.Fatal(err)
	}
	value := kpi.(visualizationir.KPIVisualizationPresentation)
	if value.DisplayUnits == nil || *value.DisplayUnits != visualizationir.VisualizationDisplayUnitsThousands || value.Note == nil || *value.Note != "Target" || value.Tone == nil || *value.Tone != visualizationir.VisualizationToneWarning || value.FavorableDirection != visualizationir.VisualizationKPIDirectionNeutral || value.MissingComparison != visualizationir.VisualizationKPIMissingComparisonShowUnavailable {
		t.Fatalf("kpi presentation = %#v", value)
	}
}

func TestLowerCanonicalPresentationVariantsPreserveFieldsAndDefaults(t *testing.T) {
	legend := document.DashboardLegendPositionTop
	labels := document.DashboardLabelPolicy{Density: document.DashboardLabelDensityAlways}
	orientation := document.DashboardOrientationHorizontal
	units := visualizationir.VisualizationDisplayUnitsBillions
	nodeGap, curveness := 18.0, .32
	layout := visualizationir.VisualizationHierarchyLayoutCircular
	focus := visualizationir.VisualizationGraphFocusAdjacency
	cases := []struct {
		name       string
		visualType document.DashboardVisualType
		value      document.DashboardPresentation
		check      func(t *testing.T, value any)
	}{
		{
			name: "proportional", visualType: document.DashboardVisualTypePie,
			value: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional", Legend: &legend, Labels: &labels, DisplayUnits: &units}},
			check: func(t *testing.T, value any) {
				got := value.(visualizationir.ProportionalVisualizationPresentation)
				if got.Legend != visualizationir.VisualizationLegendPositionTop || got.LabelPolicy.Density != visualizationir.VisualizationLabelDensityAlways || got.DisplayUnits == nil || *got.DisplayUnits != visualizationir.VisualizationDisplayUnitsBillions || got.Orientation != visualizationir.VisualizationOrientationVertical {
					t.Fatalf("proportional = %#v", got)
				}
			},
		},
		{
			name: "hierarchy", visualType: document.DashboardVisualTypeTree,
			value: document.DashboardPresentation{Value: &document.HierarchyDashboardPresentation{Type: "hierarchy", Orientation: &orientation, Layout: &layout, NodeGap: &nodeGap, Curveness: &curveness, Focus: &focus}},
			check: func(t *testing.T, value any) {
				got := value.(visualizationir.HierarchyVisualizationPresentation)
				if got.Orientation != visualizationir.VisualizationOrientationHorizontal || got.Legend != visualizationir.VisualizationLegendPositionBottom || got.Layout == nil || *got.Layout != visualizationir.VisualizationHierarchyLayoutCircular || got.NodeGap == nil || *got.NodeGap != 18 || got.Curveness == nil || *got.Curveness != .32 || got.Focus == nil || *got.Focus != visualizationir.VisualizationGraphFocusAdjacency {
					t.Fatalf("hierarchy = %#v", got)
				}
			},
		},
		{
			name: "polar", visualType: document.DashboardVisualTypeGauge,
			value: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", DisplayUnits: &units}},
			check: func(t *testing.T, value any) {
				got := value.(visualizationir.PolarVisualizationPresentation)
				if got.DisplayUnits == nil || *got.DisplayUnits != visualizationir.VisualizationDisplayUnitsBillions || !got.ShowPointer {
					t.Fatalf("polar = %#v", got)
				}
			},
		},
		{
			name: "geographic", visualType: document.DashboardVisualTypeMap,
			value: document.DashboardPresentation{Value: &document.GeographicDashboardPresentation{Type: "geographic"}},
			check: func(t *testing.T, value any) {
				got := value.(visualizationir.GeographicVisualizationPresentation)
				if !got.Roam || got.Theme != visualizationir.VisualizationMapThemeAuto || got.LabelDensity != visualizationir.VisualizationMapLabelDensityNormal || got.Camera.Mode != visualizationir.VisualizationMapCameraModeFitData || got.Camera.Padding != 32 || got.Camera.MaximumZoom != 14 || !got.Controls.Zoom || !got.Controls.Reset || !got.Controls.Compass {
					t.Fatalf("geographic = %#v", got)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, err := LowerCanonicalDashboardPresentation(test.value, test.visualType)
			if err != nil {
				t.Fatalf("lower: %v", err)
			}
			test.check(t, value)
		})
	}
}

func TestLowerCanonicalPresentationRejectsIncompatibleAndInvalidValues(t *testing.T) {
	if _, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.KPIDashboardPresentation{Type: "kpi"}}, document.DashboardVisualTypeBar); err == nil {
		t.Fatal("incompatible presentation accepted")
	}
	badLegend := document.DashboardLegendPosition("invalid")
	if _, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Legend: &badLegend}}, document.DashboardVisualTypeBar); err == nil {
		t.Fatal("invalid legend accepted")
	}
	zero := int32(0)
	if _, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.TableDashboardPresentation{Type: "table", RowHeight: zero}}, document.DashboardVisualTypeTable); err == nil {
		t.Fatal("zero table row height accepted")
	}
}

func TestLowerCanonicalPresentationForQueryRejectsShapeMismatch(t *testing.T) {
	_, err := LowerCanonicalDashboardPresentationForQuery(
		document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
		document.DashboardVisualTypeBar,
		LoweredDashboardQuery{Type: "records"},
	)
	if err == nil {
		t.Fatal("presentation accepted incompatible records query")
	}
}

func TestLowerCanonicalComboPresentationCompilesMeasureSeries(t *testing.T) {
	series := []document.DashboardComboSeries{
		{Field: "revenue", Mark: document.DashboardComboSeriesMark("area"), Axis: document.DashboardComboSeriesAxis("primary")},
		{Field: "order_count", Mark: document.DashboardComboSeriesMark("column"), Axis: document.DashboardComboSeriesAxis("secondary")},
	}
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Series: &series}}
	query := LoweredDashboardQuery{
		Type:        "aggregate",
		Binding:     visualizationdefinition.QueryBinding{Aggregate: &visualizationdefinition.AggregateQueryBinding{Metrics: []visualizationdefinition.FieldBinding{{Alias: "revenue"}, {Alias: "order_count"}}}},
		ResultFrame: []DashboardQueryResultField{{Name: "purchase_month"}, {Name: "revenue"}, {Name: "order_count"}},
	}
	lowered, err := LowerCanonicalDashboardPresentationForQuery(value, document.DashboardVisualTypeCombo, query)
	if err != nil {
		t.Fatalf("lower combo presentation: %v", err)
	}
	presentation, ok := lowered.(visualizationir.CartesianVisualizationPresentation)
	if !ok || presentation.ComboSeries == nil || len(*presentation.ComboSeries) != 2 {
		t.Fatalf("combo series = %#v", lowered)
	}
	if got := (*presentation.ComboSeries)[0]; got.SeriesValue != "revenue" || got.Mark != visualizationir.VisualizationCartesianMarkArea || got.Axis != visualizationir.VisualizationAxisPrimary {
		t.Fatalf("first combo series = %#v", got)
	}
	if got := (*presentation.ComboSeries)[1]; got.SeriesValue != "order_count" || got.Mark != visualizationir.VisualizationCartesianMarkColumn || got.Axis != visualizationir.VisualizationAxisSecondary {
		t.Fatalf("second combo series = %#v", got)
	}
}

func TestLowerCanonicalComboPresentationRejectsUnknownDuplicateAndInapplicableSeries(t *testing.T) {
	query := LoweredDashboardQuery{
		Type:        "aggregate",
		Binding:     visualizationdefinition.QueryBinding{Aggregate: &visualizationdefinition.AggregateQueryBinding{Metrics: []visualizationdefinition.FieldBinding{{Alias: "revenue"}}}},
		ResultFrame: []DashboardQueryResultField{{Name: "purchase_month"}, {Name: "revenue"}},
	}
	cases := []struct {
		name       string
		visualType document.DashboardVisualType
		series     []document.DashboardComboSeries
		want       string
	}{
		{name: "unknown result", visualType: document.DashboardVisualTypeCombo, series: []document.DashboardComboSeries{{Field: "missing", Mark: document.DashboardComboSeriesMark("line"), Axis: document.DashboardComboSeriesAxis("primary")}}, want: "not a compiled result field"},
		{name: "duplicate result", visualType: document.DashboardVisualTypeCombo, series: []document.DashboardComboSeries{{Field: "revenue", Mark: document.DashboardComboSeriesMark("line"), Axis: document.DashboardComboSeriesAxis("primary")}, {Field: "revenue", Mark: document.DashboardComboSeriesMark("area"), Axis: document.DashboardComboSeriesAxis("secondary")}}, want: "duplicates"},
		{name: "empty series", visualType: document.DashboardVisualTypeCombo, series: []document.DashboardComboSeries{}, want: "at least one entry"},
		{name: "non combo visual", visualType: document.DashboardVisualTypeLine, series: []document.DashboardComboSeries{{Field: "revenue", Mark: document.DashboardComboSeriesMark("line"), Axis: document.DashboardComboSeriesAxis("primary")}}, want: "only supported for combo"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Series: &test.series}}
			_, err := LowerCanonicalDashboardPresentationForQuery(value, test.visualType, query)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}
