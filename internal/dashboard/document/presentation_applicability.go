package document

// SupportsPresentationFamily reports whether a visual type uses the named
// closed presentation family. It is intentionally limited to the static
// visual/type relationship; authored-value and query-shape checks remain in
// the compiler and builder projection.
func SupportsPresentationFamily(visualType DashboardVisualType, presentationType string) bool {
	switch presentationType {
	case "cartesian":
		switch visualType {
		case DashboardVisualTypeLine, DashboardVisualTypeArea, DashboardVisualTypeBar, DashboardVisualTypeColumn,
			DashboardVisualTypeCandlestick, DashboardVisualTypeBoxplot, DashboardVisualTypeCombo,
			DashboardVisualTypeHeatmap, DashboardVisualTypeWaterfall, DashboardVisualTypeHistogram:
			return true
		}
	case "point":
		return visualType == DashboardVisualTypeScatter
	case "proportional":
		return visualType == DashboardVisualTypePie || visualType == DashboardVisualTypeDonut || visualType == DashboardVisualTypeFunnel
	case "hierarchy":
		switch visualType {
		case DashboardVisualTypeTreemap, DashboardVisualTypeSankey, DashboardVisualTypeGraph, DashboardVisualTypeTree, DashboardVisualTypeSunburst:
			return true
		}
	case "polar":
		return visualType == DashboardVisualTypeGauge || visualType == DashboardVisualTypeRadar
	case "geographic":
		return visualType == DashboardVisualTypeMap
	case "table":
		return visualType == DashboardVisualTypeTable || visualType == DashboardVisualTypeMatrix || visualType == DashboardVisualTypePivot
	case "kpi":
		return visualType == DashboardVisualTypeKpi
	}
	return false
}

// SupportsPresentationField reports static mark applicability for the bounded
// inventory of fields shared by the compiler and builder format catalog. It
// is not a complete presentation/schema-field registry; fields such as
// tooltip, camera, and map controls are intentionally outside this helper.
// It does not inspect authored values: combo line-control fields and
// categorical point legends need authored presentation/query context and are
// refined by their callers.
func SupportsPresentationField(visualType DashboardVisualType, field string) bool {
	switch field {
	case "axisVisible":
		return SupportsPresentationFamily(visualType, "cartesian") || SupportsPresentationFamily(visualType, "point")
	case "dataZoom":
		return SupportsPresentationFamily(visualType, "cartesian")
	case "labels":
		return (SupportsPresentationFamily(visualType, "cartesian") && visualType != DashboardVisualTypeCandlestick && visualType != DashboardVisualTypeBoxplot) ||
			SupportsPresentationFamily(visualType, "point") || SupportsPresentationFamily(visualType, "proportional") ||
			SupportsPresentationFamily(visualType, "hierarchy") || SupportsPresentationFamily(visualType, "polar")
	case "labelPosition":
		return (SupportsPresentationFamily(visualType, "cartesian") && visualType != DashboardVisualTypeCandlestick && visualType != DashboardVisualTypeBoxplot) ||
			SupportsPresentationFamily(visualType, "proportional")
	case "legend":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeBar ||
			visualType == DashboardVisualTypeColumn || visualType == DashboardVisualTypeCombo || visualType == DashboardVisualTypeCandlestick ||
			visualType == DashboardVisualTypeScatter || SupportsPresentationFamily(visualType, "proportional") || visualType == DashboardVisualTypeRadar
	case "legendTitle":
		return SupportsPresentationField(visualType, "legend")
	case "legendItems":
		return SupportsPresentationField(visualType, "legend") && visualType != DashboardVisualTypeCandlestick
	case "displayUnits":
		return SupportsPresentationFamily(visualType, "cartesian") || SupportsPresentationFamily(visualType, "proportional") ||
			SupportsPresentationFamily(visualType, "polar") || SupportsPresentationFamily(visualType, "kpi")
	case "stacking":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeBar ||
			visualType == DashboardVisualTypeColumn || visualType == DashboardVisualTypeCombo
	case "orientation":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeColumn ||
			visualType == DashboardVisualTypeCombo || visualType == DashboardVisualTypeFunnel ||
			visualType == DashboardVisualTypeTree || visualType == DashboardVisualTypeSankey
	case "showSymbols", "smooth", "step", "symbolSize":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeCombo
	case "series":
		return visualType == DashboardVisualTypeCombo
	case "seriesIntent":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeBar ||
			visualType == DashboardVisualTypeColumn || visualType == DashboardVisualTypeCombo
	case "gainColor", "lossColor":
		return visualType == DashboardVisualTypeCandlestick
	case "referenceLines", "referenceBands", "eventAnnotations":
		return visualType == DashboardVisualTypeLine || visualType == DashboardVisualTypeArea || visualType == DashboardVisualTypeBar ||
			visualType == DashboardVisualTypeColumn || visualType == DashboardVisualTypeCombo || visualType == DashboardVisualTypeWaterfall ||
			visualType == DashboardVisualTypeScatter
	case "rose", "outerRadius":
		return visualType == DashboardVisualTypePie || visualType == DashboardVisualTypeDonut
	case "centerLabel", "innerRadius":
		return visualType == DashboardVisualTypeDonut
	case "align", "sort":
		return visualType == DashboardVisualTypeFunnel
	case "initialDepth":
		return visualType == DashboardVisualTypeTree || visualType == DashboardVisualTypeTreemap
	case "roam":
		return visualType == DashboardVisualTypeGraph || visualType == DashboardVisualTypeTree || visualType == DashboardVisualTypeTreemap || visualType == DashboardVisualTypeSunburst || visualType == DashboardVisualTypeMap
	case "layout":
		return visualType == DashboardVisualTypeGraph || visualType == DashboardVisualTypeTree
	case "breadcrumb":
		return visualType == DashboardVisualTypeTreemap
	case "nodeGap":
		return visualType == DashboardVisualTypeSankey
	case "curveness":
		return visualType == DashboardVisualTypeGraph || visualType == DashboardVisualTypeSankey
	case "focus":
		return visualType == DashboardVisualTypeGraph
	case "minimum", "target", "showPointer", "progressWidth", "thresholds":
		return visualType == DashboardVisualTypeGauge
	case "maximum":
		return visualType == DashboardVisualTypeGauge || visualType == DashboardVisualTypeRadar
	case "area":
		return visualType == DashboardVisualTypeRadar
	}
	return false
}
