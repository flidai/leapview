package compiler

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
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
			value: document.DashboardPresentation{Value: &document.HierarchyDashboardPresentation{Type: "hierarchy", Orientation: &orientation}},
			check: func(t *testing.T, value any) {
				got := value.(visualizationir.HierarchyVisualizationPresentation)
				if got.Orientation != visualizationir.VisualizationOrientationHorizontal || got.Legend != visualizationir.VisualizationLegendPositionBottom {
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
