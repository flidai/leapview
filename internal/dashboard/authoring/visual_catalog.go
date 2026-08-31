package authoring

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// VisualCatalogEntry is the builder-facing projection of the same closed
// visual registry exercised by the executable visual reference documentation.
type VisualCatalogEntry struct {
	Type          document.DashboardVisualType
	Label         string
	Group         string
	ReferenceHref string
}

// VisualRoleLimit is a builder-facing cardinality constraint from the
// canonical visualization contract. Missing roles are intentionally
// unbounded; the compiler remains the final authority.
type VisualRoleLimit struct {
	Role    string
	Maximum int32
}

// VisualFormatChoice is one closed enum value accepted by a format option.
type VisualFormatChoice struct {
	Value string
	Label string
}

// VisualFormatOption describes one scalar, renderer-neutral presentation
// property. Object bindings and rule arrays deliberately stay in Build/YAML.
type VisualFormatOption struct {
	Key         string
	Label       string
	Section     string
	Control     string
	Value       string
	Placeholder string
	Choices     []VisualFormatChoice
}

var canonicalVisualCatalog = []VisualCatalogEntry{
	{document.DashboardVisualTypeLine, "Line chart", "Cartesian", "/docs/visuals/line"},
	{document.DashboardVisualTypeArea, "Area chart", "Cartesian", "/docs/visuals/area"},
	{document.DashboardVisualTypeBar, "Bar chart", "Cartesian", "/docs/visuals/bar"},
	{document.DashboardVisualTypeColumn, "Column chart", "Cartesian", "/docs/visuals/column"},
	{document.DashboardVisualTypePie, "Pie chart", "Part to whole", "/docs/visuals/pie"},
	{document.DashboardVisualTypeDonut, "Donut chart", "Part to whole", "/docs/visuals/donut"},
	{document.DashboardVisualTypeScatter, "Scatter chart", "Distribution", "/docs/visuals/scatter"},
	{document.DashboardVisualTypeFunnel, "Funnel chart", "Part to whole", "/docs/visuals/funnel"},
	{document.DashboardVisualTypeTreemap, "Treemap", "Hierarchy & flow", "/docs/visuals/treemap"},
	{document.DashboardVisualTypeGauge, "Gauge", "Specialized", "/docs/visuals/gauge"},
	{document.DashboardVisualTypeHeatmap, "Heatmap", "Distribution", "/docs/visuals/heatmap"},
	{document.DashboardVisualTypeSankey, "Sankey", "Hierarchy & flow", "/docs/visuals/sankey"},
	{document.DashboardVisualTypeGraph, "Graph", "Hierarchy & flow", "/docs/visuals/graph"},
	{document.DashboardVisualTypeMap, "Map", "Specialized", "/docs/visuals/map"},
	{document.DashboardVisualTypeCandlestick, "Candlestick chart", "Cartesian", "/docs/visuals/candlestick"},
	{document.DashboardVisualTypeBoxplot, "Boxplot", "Distribution", "/docs/visuals/boxplot"},
	{document.DashboardVisualTypeCombo, "Combo chart", "Cartesian", "/docs/visuals/combo"},
	{document.DashboardVisualTypeWaterfall, "Waterfall chart", "Cartesian", "/docs/visuals/waterfall"},
	{document.DashboardVisualTypeHistogram, "Histogram", "Distribution", "/docs/visuals/histogram"},
	{document.DashboardVisualTypeRadar, "Radar chart", "Specialized", "/docs/visuals/radar"},
	{document.DashboardVisualTypeTree, "Tree", "Hierarchy & flow", "/docs/visuals/tree"},
	{document.DashboardVisualTypeSunburst, "Sunburst", "Hierarchy & flow", "/docs/visuals/sunburst"},
	{document.DashboardVisualTypeKpi, "KPI", "Specialized", "/docs/visuals/kpi"},
	{document.DashboardVisualTypeTable, "Table", "Tables", "/docs/visuals/table"},
	{document.DashboardVisualTypeMatrix, "Matrix", "Tables", "/docs/visuals/matrix"},
	{document.DashboardVisualTypePivot, "Pivot", "Tables", "/docs/visuals/pivot"},
}

// CanonicalVisualCatalog returns a copy so transports cannot mutate the
// reducer's authoritative registry.
func CanonicalVisualCatalog() []VisualCatalogEntry {
	return append([]VisualCatalogEntry(nil), canonicalVisualCatalog...)
}

// CanonicalVisualRoles describes the governed field wells used when a new
// visual is authored. Existing dashboards continue to project their exact
// query slots, while the picker can explain the minimum shape up front.
func CanonicalVisualRoles(visualType document.DashboardVisualType) []string {
	switch visualType {
	case document.DashboardVisualTypeHistogram, document.DashboardVisualTypeBoxplot,
		document.DashboardVisualTypeGauge, document.DashboardVisualTypeKpi:
		return []string{"metric"}
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMap:
		return []string{"detail"}
	default:
		return []string{"dimension", "metric"}
	}
}

// CanonicalVisualRoleLimits exposes only closed, renderer-enforced maxima so
// the builder never advertises a field assignment that strict compilation
// must reject.
func CanonicalVisualRoleLimits(visualType document.DashboardVisualType) []VisualRoleLimit {
	dimension := func(maximum int32) VisualRoleLimit { return VisualRoleLimit{Role: "dimension", Maximum: maximum} }
	metric := func(maximum int32) VisualRoleLimit { return VisualRoleLimit{Role: "metric", Maximum: maximum} }
	switch visualType {
	case document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeFunnel,
		document.DashboardVisualTypeWaterfall:
		return []VisualRoleLimit{dimension(1), metric(1)}
	case document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeGraph, document.DashboardVisualTypeSankey:
		return []VisualRoleLimit{dimension(2), metric(1)}
	case document.DashboardVisualTypeGauge, document.DashboardVisualTypeRadar:
		return []VisualRoleLimit{dimension(1), metric(1)}
	case document.DashboardVisualTypeKpi, document.DashboardVisualTypeHistogram, document.DashboardVisualTypeBoxplot:
		return []VisualRoleLimit{metric(1)}
	case document.DashboardVisualTypeCandlestick:
		return []VisualRoleLimit{dimension(1), metric(4)}
	case document.DashboardVisualTypeTreemap, document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst:
		return []VisualRoleLimit{metric(1)}
	case document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar,
		document.DashboardVisualTypeColumn, document.DashboardVisualTypeCombo:
		return []VisualRoleLimit{dimension(1)}
	default:
		return []VisualRoleLimit{}
	}
}

type visualFormatSpec struct {
	key, label, section, control, placeholder, defaultValue string
	path                                                    []string
	choices                                                 []VisualFormatChoice
	optional                                                bool
}

func choices(values ...string) []VisualFormatChoice {
	result := make([]VisualFormatChoice, 0, len(values))
	for _, value := range values {
		result = append(result, VisualFormatChoice{Value: value, Label: formatChoiceLabel(value)})
	}
	return result
}

func formatChoiceLabel(value string) string {
	value = strings.ReplaceAll(value, "_", " ")
	if value == "" {
		return "Default"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func formatSpec(key, label, section, control string, path []string, defaultValue string, optional bool, values ...string) visualFormatSpec {
	return visualFormatSpec{key: key, label: label, section: section, control: control, path: path, defaultValue: defaultValue, optional: optional, choices: choices(values...)}
}

func visualFormatSpecs(presentationType string) []visualFormatSpec {
	axis := formatSpec("axisVisible", "Show axes", "Display", "toggle", []string{"axisVisible"}, "true", true)
	legend := formatSpec("legend", "Legend", "Display", "select", []string{"legend"}, "right", true, "none", "top", "right", "bottom", "left")
	labels := formatSpec("labels.density", "Data labels", "Display", "select", []string{"labels", "density"}, "automatic", true, "hidden", "automatic", "dense", "always")
	labelLength := formatSpec("labels.maxCharacters", "Maximum label characters", "Labels", "number", []string{"labels", "maxCharacters"}, "24", true)
	labelSpacing := formatSpec("labels.minimumSpacing", "Minimum label spacing", "Labels", "number", []string{"labels", "minimumSpacing"}, "6", true)
	labelTooltip := formatSpec("labels.tooltipFallback", "Tooltip for truncated labels", "Labels", "toggle", []string{"labels", "tooltipFallback"}, "true", true)
	displayUnits := formatSpec("displayUnits", "Display units", "Values", "select", []string{"displayUnits"}, "auto", true, "auto", "none", "thousands", "millions", "billions", "trillions")
	switch presentationType {
	case "cartesian":
		return []visualFormatSpec{
			axis, legend, labels, labelLength, labelSpacing, labelTooltip,
			formatSpec("stacking", "Stacking", "Chart", "select", []string{"stacking"}, "none", true, "none", "normal", "percent"),
			formatSpec("orientation", "Orientation", "Chart", "select", []string{"orientation"}, "", true, "horizontal", "vertical"),
			formatSpec("showSymbols", "Show symbols", "Chart", "toggle", []string{"showSymbols"}, "true", true),
			formatSpec("smooth", "Smooth lines", "Chart", "toggle", []string{"smooth"}, "false", true),
			formatSpec("step", "Stepped lines", "Chart", "toggle", []string{"step"}, "false", true),
			formatSpec("dataZoom", "Data zoom", "Interaction", "toggle", []string{"dataZoom"}, "false", true),
			formatSpec("symbolSize", "Symbol size", "Chart", "number", []string{"symbolSize"}, "", true),
			formatSpec("labelPosition", "Label position", "Display", "select", []string{"labelPosition"}, "automatic", true, "automatic", "inside", "outside", "top"),
			displayUnits,
		}
	case "point":
		return []visualFormatSpec{axis, legend, labels, labelLength, labelSpacing, labelTooltip}
	case "proportional":
		return []visualFormatSpec{
			legend, labels, labelLength, labelSpacing, labelTooltip, displayUnits,
			formatSpec("orientation", "Orientation", "Chart", "select", []string{"orientation"}, "", true, "horizontal", "vertical"),
			formatSpec("rose", "Rose layout", "Chart", "toggle", []string{"rose"}, "false", true),
			formatSpec("centerLabel", "Center label", "Labels", "text", []string{"centerLabel"}, "", true),
			formatSpec("labelPosition", "Label position", "Labels", "select", []string{"labelPosition"}, "automatic", true, "automatic", "inside", "outside", "top"),
			formatSpec("innerRadius", "Inner radius", "Geometry", "number", []string{"innerRadius"}, "", true),
			formatSpec("outerRadius", "Outer radius", "Geometry", "number", []string{"outerRadius"}, "", true),
			formatSpec("align", "Alignment", "Geometry", "select", []string{"align"}, "", true, "left", "center", "right"),
			formatSpec("sort", "Sort", "Chart", "select", []string{"sort"}, "", true, "ascending", "descending"),
		}
	case "hierarchy":
		return []visualFormatSpec{
			legend, labels, labelLength, labelSpacing, labelTooltip,
			formatSpec("orientation", "Orientation", "Chart", "select", []string{"orientation"}, "", true, "horizontal", "vertical"),
			formatSpec("initialDepth", "Initial depth", "Hierarchy", "number", []string{"initialDepth"}, "", true),
			formatSpec("roam", "Pan and zoom", "Interaction", "toggle", []string{"roam"}, "false", true),
			formatSpec("layout", "Layout", "Hierarchy", "select", []string{"layout"}, "standard", true, "standard", "circular"),
			formatSpec("breadcrumb", "Breadcrumb", "Hierarchy", "toggle", []string{"breadcrumb"}, "true", true),
			formatSpec("nodeGap", "Node gap", "Geometry", "number", []string{"nodeGap"}, "", true),
			formatSpec("curveness", "Link curvature", "Geometry", "number", []string{"curveness"}, "", true),
			formatSpec("focus", "Focus", "Interaction", "select", []string{"focus"}, "none", true, "none", "adjacency"),
		}
	case "polar":
		return []visualFormatSpec{
			axis, legend, labels, labelLength, labelSpacing, labelTooltip, displayUnits,
			formatSpec("minimum", "Minimum", "Scale", "number", []string{"minimum"}, "", true),
			formatSpec("maximum", "Maximum", "Scale", "number", []string{"maximum"}, "", true),
			formatSpec("target", "Target", "Scale", "number", []string{"target"}, "", true),
			formatSpec("showPointer", "Show pointer", "Chart", "toggle", []string{"showPointer"}, "true", true),
			formatSpec("area", "Filled area", "Chart", "toggle", []string{"area"}, "false", true),
			formatSpec("progressWidth", "Progress width", "Geometry", "number", []string{"progressWidth"}, "", true),
		}
	case "geographic":
		return []visualFormatSpec{
			labels, labelLength, labelSpacing, labelTooltip,
			formatSpec("theme", "Map theme", "Map", "select", []string{"theme"}, "auto", true, "auto", "light", "dark"),
			formatSpec("basemap", "Basemap", "Map", "text", []string{"basemap"}, "", true),
			formatSpec("labelDensity", "Basemap labels", "Map", "select", []string{"labelDensity"}, "normal", true, "hidden", "normal", "dense"),
			formatSpec("roam", "Pan and zoom", "Interaction", "toggle", []string{"roam"}, "true", true),
			formatSpec("camera.mode", "Camera mode", "Camera", "select", []string{"camera", "mode"}, "fit_data", true, "fit_data", "fixed", "preserve"),
			formatSpec("camera.zoom", "Zoom", "Camera", "number", []string{"camera", "zoom"}, "", true),
			formatSpec("camera.padding", "Fit padding", "Camera", "number", []string{"camera", "padding"}, "24", true),
			formatSpec("camera.minimumZoom", "Minimum zoom", "Camera", "number", []string{"camera", "minimumZoom"}, "0", true),
			formatSpec("camera.maximumZoom", "Maximum zoom", "Camera", "number", []string{"camera", "maximumZoom"}, "10", true),
			formatSpec("controls.zoom", "Zoom controls", "Controls", "toggle", []string{"controls", "zoom"}, "true", true),
			formatSpec("controls.reset", "Reset control", "Controls", "toggle", []string{"controls", "reset"}, "true", true),
			formatSpec("controls.compass", "Compass control", "Controls", "toggle", []string{"controls", "compass"}, "true", true),
		}
	case "table":
		return []visualFormatSpec{
			formatSpec("rowHeight", "Row height", "Table", "number", []string{"rowHeight"}, "32", false),
			formatSpec("showHeader", "Show header", "Table", "toggle", []string{"showHeader"}, "true", false),
			formatSpec("striped", "Striped rows", "Table", "toggle", []string{"striped"}, "false", false),
		}
	case "kpi":
		return []visualFormatSpec{
			formatSpec("mode", "Mode", "KPI", "select", []string{"mode"}, "compact", true, "compact", "bullet", "progress"),
			formatSpec("delta", "Delta", "Comparison", "select", []string{"delta"}, "", true, "absolute", "relative"),
			formatSpec("favorableDirection", "Favorable direction", "Comparison", "select", []string{"favorableDirection"}, "neutral", true, "increase", "decrease", "neutral"),
			formatSpec("missingComparison", "Missing comparison", "Comparison", "select", []string{"missingComparison"}, "show_unavailable", true, "show_unavailable", "hide"),
			displayUnits,
			formatSpec("note", "Note", "KPI", "text", []string{"note"}, "", true),
			formatSpec("tone", "Tone", "KPI", "select", []string{"tone"}, "neutral", true, "neutral", "ink", "success", "warning", "danger"),
		}
	default:
		return nil
	}
}

// CanonicalVisualFormatOptions projects the actual authored presentation, so
// a builder created visual and a dashboards-as-code visual expose identical
// scalar/enum controls.
func CanonicalVisualFormatOptions(visual document.DashboardVisual) ([]VisualFormatOption, error) {
	presentationType, err := visual.Presentation.Type()
	if err != nil {
		return nil, err
	}
	raw, err := presentationObject(visual.Presentation)
	if err != nil {
		return nil, err
	}
	specs := visualFormatSpecs(presentationType)
	result := make([]VisualFormatOption, 0, len(specs))
	for _, spec := range specs {
		value := spec.defaultValue
		if current, ok := lookupFormatPath(raw, spec.path); ok {
			value = scalarFormatValue(current)
		}
		result = append(result, VisualFormatOption{Key: spec.key, Label: spec.label, Section: spec.section, Control: spec.control, Value: value, Placeholder: spec.placeholder, Choices: append([]VisualFormatChoice(nil), spec.choices...)})
	}
	return result, nil
}

func applyCanonicalVisualFormatOption(visual *document.DashboardVisual, key, value string) error {
	if visual == nil {
		return fmt.Errorf("%w: visual is required", ErrInvalidPayload)
	}
	presentationType, err := visual.Presentation.Type()
	if err != nil {
		return fmt.Errorf("%w: visual presentation: %v", ErrInvalidPayload, err)
	}
	var spec *visualFormatSpec
	for _, candidate := range visualFormatSpecs(presentationType) {
		if candidate.key == key {
			copy := candidate
			spec = &copy
			break
		}
	}
	if spec == nil {
		return fmt.Errorf("%w: format option %q is not supported by %s presentations", ErrInvalidPayload, key, presentationType)
	}
	raw, err := presentationObject(visual.Presentation)
	if err != nil {
		return fmt.Errorf("%w: visual presentation: %v", ErrInvalidPayload, err)
	}
	if value == "" && spec.optional {
		deleteFormatPath(raw, spec.path)
	} else {
		parsed, err := parseFormatValue(*spec, value)
		if err != nil {
			return err
		}
		if len(spec.path) > 1 && spec.path[0] == "labels" {
			if _, ok := raw["labels"].(map[string]any); !ok {
				raw["labels"] = map[string]any{"density": "automatic"}
			}
		}
		setFormatPath(raw, spec.path, parsed)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("%w: encode visual presentation: %v", ErrInvalidPayload, err)
	}
	var next document.DashboardPresentation
	if err := json.Unmarshal(encoded, &next); err != nil {
		return fmt.Errorf("%w: decode visual presentation: %v", ErrInvalidPayload, err)
	}
	visual.Presentation = next
	return nil
}

func presentationObject(presentation document.DashboardPresentation) (map[string]any, error) {
	encoded, err := json.Marshal(presentation)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func parseFormatValue(spec visualFormatSpec, value string) (any, error) {
	switch spec.control {
	case "toggle":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("%w: format option %q requires a boolean", ErrInvalidPayload, spec.key)
		}
		return parsed, nil
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("%w: format option %q requires a finite number", ErrInvalidPayload, spec.key)
		}
		return parsed, nil
	case "select":
		for _, choice := range spec.choices {
			if choice.Value == value {
				return value, nil
			}
		}
		return nil, fmt.Errorf("%w: format option %q does not accept %q", ErrInvalidPayload, spec.key, value)
	case "text":
		if len(value) > 256 {
			return nil, fmt.Errorf("%w: format option %q exceeds 256 characters", ErrInvalidPayload, spec.key)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("%w: format option %q has unsupported control %q", ErrInvalidPayload, spec.key, spec.control)
	}
}

func lookupFormatPath(value map[string]any, path []string) (any, bool) {
	var current any = value
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setFormatPath(value map[string]any, path []string, next any) {
	current := value
	for _, segment := range path[:len(path)-1] {
		child, ok := current[segment].(map[string]any)
		if !ok {
			child = map[string]any{}
			current[segment] = child
		}
		current = child
	}
	current[path[len(path)-1]] = next
}

func deleteFormatPath(value map[string]any, path []string) {
	current := value
	parents := []map[string]any{value}
	for _, segment := range path[:len(path)-1] {
		child, ok := current[segment].(map[string]any)
		if !ok {
			return
		}
		current = child
		parents = append(parents, current)
	}
	delete(current, path[len(path)-1])
	for index := len(parents) - 1; index > 0; index-- {
		if len(parents[index]) != 0 {
			break
		}
		delete(parents[index-1], path[index-1])
	}
}

func scalarFormatValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}
