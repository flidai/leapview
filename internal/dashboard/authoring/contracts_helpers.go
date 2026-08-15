package authoring

import (
	"slices"
	"strings"
)

var supportedVisualShapes = map[string]struct{}{
	"category_value": {}, "category_series_value": {}, "category_multi_measure": {}, "category_delta": {},
	"single_value": {}, "matrix": {}, "graph": {}, "geo": {}, "ohlc": {}, "distribution": {},
	"binned_measure": {}, "hierarchy": {}, "point": {},
}

type VisualizationCapability struct {
	Type           string
	Kind           string
	Renderer       string
	ResultShape    string
	SupportsSeries bool
}

var visualizationCapabilities = map[string]VisualizationCapability{
	"area":        {Type: "area", Kind: "chart", Renderer: "echarts", ResultShape: "category_value", SupportsSeries: true},
	"bar":         {Type: "bar", Kind: "chart", Renderer: "echarts", ResultShape: "category_value", SupportsSeries: true},
	"boxplot":     {Type: "boxplot", Kind: "chart", Renderer: "echarts", ResultShape: "distribution"},
	"candlestick": {Type: "candlestick", Kind: "chart", Renderer: "echarts", ResultShape: "ohlc"},
	"column":      {Type: "column", Kind: "chart", Renderer: "echarts", ResultShape: "category_value", SupportsSeries: true},
	"combo":       {Type: "combo", Kind: "chart", Renderer: "echarts", ResultShape: "category_multi_measure"},
	"donut":       {Type: "donut", Kind: "chart", Renderer: "echarts", ResultShape: "category_value"},
	"funnel":      {Type: "funnel", Kind: "chart", Renderer: "echarts", ResultShape: "category_value"},
	"gauge":       {Type: "gauge", Kind: "chart", Renderer: "echarts", ResultShape: "single_value"},
	"graph":       {Type: "graph", Kind: "chart", Renderer: "echarts", ResultShape: "graph"},
	"heatmap":     {Type: "heatmap", Kind: "chart", Renderer: "echarts", ResultShape: "matrix"},
	"histogram":   {Type: "histogram", Kind: "chart", Renderer: "echarts", ResultShape: "binned_measure"},
	"kpi":         {Type: "kpi", Kind: "kpi", Renderer: "html", ResultShape: "single_value"},
	"line":        {Type: "line", Kind: "chart", Renderer: "echarts", ResultShape: "category_value", SupportsSeries: true},
	"map":         {Type: "map", Kind: "chart", Renderer: "maplibre", ResultShape: "geo"},
	"matrix":      {Type: "matrix", Kind: "grid", Renderer: "tanstack", ResultShape: "matrix_window"},
	"pie":         {Type: "pie", Kind: "chart", Renderer: "echarts", ResultShape: "category_value"},
	"pivot":       {Type: "pivot", Kind: "grid", Renderer: "tanstack", ResultShape: "pivot_window"},
	"radar":       {Type: "radar", Kind: "chart", Renderer: "echarts", ResultShape: "category_value"},
	"sankey":      {Type: "sankey", Kind: "chart", Renderer: "echarts", ResultShape: "graph"},
	"scatter":     {Type: "scatter", Kind: "chart", Renderer: "echarts", ResultShape: "point"},
	"sunburst":    {Type: "sunburst", Kind: "chart", Renderer: "echarts", ResultShape: "hierarchy"},
	"table":       {Type: "table", Kind: "grid", Renderer: "tanstack", ResultShape: "detail_window"},
	"tree":        {Type: "tree", Kind: "chart", Renderer: "echarts", ResultShape: "hierarchy"},
	"treemap":     {Type: "treemap", Kind: "chart", Renderer: "echarts", ResultShape: "hierarchy"},
	"waterfall":   {Type: "waterfall", Kind: "chart", Renderer: "echarts", ResultShape: "category_delta"},
}

var supportedGeographicLayerKinds = []string{"choropleth", "density", "heat", "path", "point", "reference"}

// SupportedVisualizationTypes is the canonical public authoring catalog. It is
// consumed by documentation coverage tests so a new type cannot ship without
// an executable example.
func SupportedVisualizationTypes() []string {
	types := make([]string, 0, len(visualizationCapabilities))
	for visualType := range visualizationCapabilities {
		types = append(types, visualType)
	}
	slices.Sort(types)
	return types
}

func VisualizationCapabilityForType(visualType string) (VisualizationCapability, bool) {
	capability, ok := visualizationCapabilities[visualType]
	return capability, ok
}

// SupportedGeographicLayerKinds returns the closed geographic authoring union.
func SupportedGeographicLayerKinds() []string {
	return slices.Clone(supportedGeographicLayerKinds)
}

func SupportedVisualShapes() []string {
	shapes := make([]string, 0, len(supportedVisualShapes))
	for shape := range supportedVisualShapes {
		shapes = append(shapes, shape)
	}
	slices.Sort(shapes)
	return shapes
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func titleFromIdentifier(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func supportsVisualKind(kind string) bool {
	return kind == "chart" || kind == "kpi"
}

func supportsVisualShape(shape string) bool {
	_, ok := supportedVisualShapes[shape]
	return ok
}

func rendererSupportsType(renderer, chartType string) bool {
	capability, ok := visualizationCapabilities[chartType]
	return ok && capability.Renderer == renderer
}

func supportsSeries(shape string) bool {
	return shape == "category_series_value"
}

func rendererSupportsShapeType(renderer, shape, chartType string) bool {
	if renderer == "html" {
		return shape == "single_value" && (chartType == "kpi" || chartType == "")
	}
	if renderer == "maplibre" {
		return shape == "geo" && chartType == "map"
	}
	if renderer != "echarts" {
		return false
	}
	switch shape {
	case "category_value":
		switch chartType {
		case "line", "area", "bar", "column", "pie", "donut", "funnel", "treemap", "radar":
			return true
		}
	case "category_series_value":
		return rendererTypeSupportsSeries(renderer, chartType)
	case "category_multi_measure":
		return chartType == "combo"
	case "category_delta":
		return chartType == "waterfall"
	case "single_value":
		return chartType == "gauge"
	case "matrix":
		return chartType == "heatmap"
	case "graph":
		return chartType == "sankey" || chartType == "graph"
	case "geo":
		return chartType == "map"
	case "ohlc":
		return chartType == "candlestick"
	case "distribution":
		return chartType == "boxplot"
	case "binned_measure":
		return chartType == "histogram"
	case "hierarchy":
		return chartType == "tree" || chartType == "treemap" || chartType == "sunburst"
	case "point":
		return chartType == "scatter"
	}
	return false
}

func rendererTypeSupportsSeries(renderer, chartType string) bool {
	capability, ok := visualizationCapabilities[chartType]
	return ok && capability.Renderer == renderer && capability.SupportsSeries
}
