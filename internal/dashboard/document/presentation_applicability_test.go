package document

import "testing"

func TestSupportsPresentationFamilyUsesClosedVisualFamilies(t *testing.T) {
	tests := []struct {
		visualType       DashboardVisualType
		presentationType string
		want             bool
	}{
		{DashboardVisualTypeLine, "cartesian", true},
		{DashboardVisualTypeScatter, "point", true},
		{DashboardVisualTypePie, "proportional", true},
		{DashboardVisualTypeTreemap, "hierarchy", true},
		{DashboardVisualTypeRadar, "polar", true},
		{DashboardVisualTypeMap, "geographic", true},
		{DashboardVisualTypeTable, "table", true},
		{DashboardVisualTypeKpi, "kpi", true},
		{DashboardVisualTypeMap, "cartesian", false},
		{DashboardVisualTypeBar, "point", false},
	}
	for _, test := range tests {
		if got := SupportsPresentationFamily(test.visualType, test.presentationType); got != test.want {
			t.Errorf("SupportsPresentationFamily(%q, %q) = %t, want %t", test.visualType, test.presentationType, got, test.want)
		}
	}
}

func TestSupportsPresentationFieldKeepsStaticAndDynamicBoundaries(t *testing.T) {
	tests := []struct {
		visualType DashboardVisualType
		field      string
		want       bool
	}{
		{DashboardVisualTypeLine, "axisVisible", true},
		{DashboardVisualTypeScatter, "axisVisible", true},
		{DashboardVisualTypeMap, "axisVisible", false},
		{DashboardVisualTypeHistogram, "dataZoom", true},
		{DashboardVisualTypeScatter, "dataZoom", false},
		{DashboardVisualTypeBoxplot, "labels", false},
		{DashboardVisualTypeCandlestick, "legendItems", false},
		{DashboardVisualTypeScatter, "legend", true},
		{DashboardVisualTypeCombo, "showSymbols", true},
		{DashboardVisualTypeBar, "showSymbols", false},
		{DashboardVisualTypeColumn, "orientation", true},
		{DashboardVisualTypeBar, "orientation", false},
		{DashboardVisualTypeWaterfall, "referenceLines", true},
		{DashboardVisualTypeHeatmap, "referenceLines", false},
		{DashboardVisualTypeScatter, "referenceLines", true},
		{DashboardVisualTypeDonut, "innerRadius", true},
		{DashboardVisualTypePie, "innerRadius", false},
		{DashboardVisualTypeRadar, "maximum", true},
		{DashboardVisualTypeGauge, "area", false},
		{DashboardVisualTypeMap, "roam", true},
		{DashboardVisualTypeScatter, "labelPosition", false},
		{DashboardVisualTypeLine, "unknown", false},
	}
	for _, test := range tests {
		if got := SupportsPresentationField(test.visualType, test.field); got != test.want {
			t.Errorf("SupportsPresentationField(%q, %q) = %t, want %t", test.visualType, test.field, got, test.want)
		}
	}
}
