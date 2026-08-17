package main

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/app/site/visualdocs"
)

var queryFieldReferences = map[string]visualdocs.FieldReference{
	"datasets": {
		Type:        "named query mapping",
		Description: "Declares bounded comparison, goal, trend, and decision-context queries that inherit the visual's active semantic filters.",
	},
	"table": {
		Type:        "string",
		Description: "Selects the fact table when the semantic model cannot infer one from the referenced fields.",
	},
	"dimensions": {
		Type:        "field mapping",
		Description: "Groups query results and supplies category, hierarchy, matrix, graph, or geographic labels.",
	},
	"series": {
		Type:        "field reference",
		Description: "Splits one metric into named series for compatible chart shapes.",
	},
	"metrics": {
		Type:        "metric mapping",
		Description: "Selects the governed semantic metrics consumed by the visual shape.",
	},
	"time": {
		Type:        "time reference",
		Description: "Groups a time field at an explicit grain.",
	},
	"sort": {
		Type:        "sort list",
		Description: "Orders query results by a returned field or metric alias.",
	},
	"limit": {
		Type:          "integer",
		Default:       "no limit",
		AllowedValues: []string{"positive integer"},
		Description:   "Caps the number of rows returned to the renderer.",
	},
}

var kpiFieldReferences = map[string]visualdocs.FieldReference{
	"mode":                field("string", "compact", []string{"compact", "bullet", "progress"}, "Selects compact value, bullet, or progress presentation; bullet and progress require a goal."),
	"comparison":          field("value binding", "none", nil, "Binds a labeled comparison to a compiler-owned dataset field and deterministic reducer."),
	"goal":                field("value binding", "none", nil, "Binds an explicit labeled target used by bullet and progress modes."),
	"trend":               field("trend binding", "none", nil, "Binds ordered category and numeric value fields for the compact sparkline."),
	"delta":               field("string", "absolute", []string{"absolute", "relative"}, "Controls whether change is formatted in the value's unit or as a relative percentage."),
	"favorable_direction": field("string", "neutral", []string{"increase", "decrease", "neutral"}, "Defines whether an increase or decrease is favorable; LeapView never infers business meaning from the sign."),
	"missing_comparison":  field("string", "show_unavailable", []string{"show_unavailable", "hide"}, "Controls whether missing comparison context is stated explicitly or intentionally hidden."),
	"ranges":              field("ordered qualitative range list", "none", nil, "Maps non-overlapping numeric ranges to a status label and semantic tone, with unmatched values reported as out of range."),
}

var presentationFieldReferences = map[string]visualdocs.FieldReference{
	"area":                   booleanOption("true", "Fills the radar polygon so its overall profile is easier to compare."),
	"axes":                   field("axis list", "automatic", []string{"x", "primary_y", "secondary_y"}, "Declares renderer-neutral titles, domains, zero policies, linear or log scales, units, display-unit overrides, and tick density."),
	"display_units":          field("string", "auto", []string{"auto", "none", "thousands", "millions", "billions", "trillions"}, "Chooses one governed magnitude for the complete visual scope; auto uses at most three significant digits while none preserves canonical semantic formatting."),
	"histogram_bins":         field("integer", "20", []string{"5–60"}, "Controls the number of equal-width histogram bins."),
	"breadcrumb":             booleanOption("false", "Shows the treemap hierarchy breadcrumb."),
	"center_label":           field("string | boolean", "none", nil, "Adds a total or custom label to the center of a donut."),
	"curveness":              field("number", "renderer default", []string{"0–1"}, "Controls the curvature of graph or Sankey links."),
	"data_zoom":              booleanOption("false", "Adds inside and slider zoom controls to supported Cartesian charts."),
	"dual_axis":              booleanOption("false", "Places the second combo series on a separate value axis."),
	"event_annotations":      field("event list", "none", nil, "Places labeled events on the horizontal axis using literal or governed field-reduced values."),
	"focus":                  field("string", "renderer default", []string{"adjacency", "descendant"}, "Selects which related graph or hierarchy elements receive emphasis."),
	"align":                  field("string", "center", []string{"left", "center", "right"}, "Aligns funnel stages within the plotting area."),
	"initial_depth":          field("integer", "-1", []string{"-1 or greater"}, "Sets the deepest hierarchy level expanded initially; -1 expands all levels."),
	"labels":                 field("label policy", "hidden", []string{"hidden", "automatic", "dense", "always"}, "Controls deterministic label density, priority preservation, grapheme-safe truncation, collision spacing, and tooltip fallback."),
	"label_position":         field("string", "renderer default", []string{"top", "bottom", "left", "right", "inside", "outside"}, "Positions value labels relative to their marks."),
	"layout":                 field("string", "force", []string{"force", "circular"}, "Selects the graph node layout algorithm."),
	"legend":                 field("boolean | string", "false", []string{"true", "false", "top", "bottom", "left", "right"}, "Shows the legend and optionally selects its position."),
	"maximum":                field("number", "required for gauges", nil, "Sets the explicit upper bound of a gauge scale."),
	"minimum":                field("number", "required for gauges", nil, "Sets the explicit lower bound of a gauge scale."),
	"node_gap":               field("number", "8", []string{"0 or greater"}, "Sets the vertical gap between Sankey nodes."),
	"note":                   field("string", "none", nil, "Adds supporting context below a KPI value."),
	"orientation":            field("string", "renderer default", []string{"horizontal", "vertical"}, "Controls the direction of tree or Sankey layout."),
	"progress_width":         field("number", "12", []string{"positive number"}, "Sets the width of the gauge progress arc."),
	"inner_radius":           field("number", "renderer default", []string{"0–1"}, "Sets the inner radius of a donut."),
	"outer_radius":           field("number", "renderer default", []string{"0–1"}, "Sets the outer radius of a pie or donut."),
	"roam":                   booleanOption("renderer default", "Enables supported pan, zoom, or hierarchy navigation interactions."),
	"reference_bands":        field("reference band list", "none", nil, "Adds typed tolerance or target ranges using literal or governed field-reduced bounds."),
	"reference_lines":        field("reference line list", "none", nil, "Adds typed targets or thresholds using literal or governed field-reduced values."),
	"rose":                   booleanOption("false", "Scales pie sectors as a rose chart."),
	"series_types":           field("mapping", "automatic", []string{"line", "bar", "column"}, "Maps combo series names or metric aliases to renderer types."),
	"series_colors":          field("semantic color mapping", "automatic", nil, "Assigns stable semantic color intents to known series identities."),
	"series_order":           field("string list", "query order", nil, "Declares stable priority order for known series identities."),
	"show_labels":            booleanOption("renderer default", "Shows value labels directly on chart marks."),
	"show_symbols":           booleanOption("true", "Shows point symbols on line and area series."),
	"smooth":                 booleanOption("true", "Uses curved interpolation for line and area series."),
	"sort":                   field("string", "descending", []string{"ascending", "descending", "none"}, "Controls the renderer-side ordering of funnel stages."),
	"stacked":                booleanOption("false", "Stacks compatible bar, column, line, or area series."),
	"stacking":               field("string", "none", []string{"none", "normal", "percent"}, "Declares explicit normal or 100% stacking while retaining raw tooltip values."),
	"step":                   booleanOption("false", "Draws line segments as discrete steps."),
	"symbol_size":            field("number", "renderer default", []string{"positive number"}, "Sets point symbol size for line, area, and scatter series."),
	"target":                 field("number", "none", nil, "Adds a labeled gauge target that must fall within the configured domain."),
	"thresholds":             field("threshold list", "none", nil, "Maps gauge thresholds to scale positions and colors."),
	"tooltip":                field("field list", "automatic", nil, "Selects compiler-known primary dataset fields shown in tooltips."),
	"tone":                   field("string", "neutral", []string{"neutral", "ink", "success", "warning", "danger"}, "Sets the semantic accent tone of a KPI card."),
	"conditional_formatting": field("conditional format list", "none", []string{"gradient", "rules", "field"}, "Applies governed gradients, ordered rules, or bound-field styles with explicit null and default outcomes."),
}

func visualFieldReferences(queryFields, optionFields []string, chartType string) ([]visualdocs.FieldReference, error) {
	result := make([]visualdocs.FieldReference, 0, len(queryFields)+len(optionFields))
	for _, name := range queryFields {
		reference, ok := queryFieldReferences[name]
		if !ok {
			return nil, fmt.Errorf("query.%s has no documentation field metadata", name)
		}
		if name == "datasets" {
			reference.Path = "datasets"
		} else {
			reference.Path = "query." + name
		}
		result = append(result, reference)
	}
	for _, name := range optionFields {
		if strings.HasPrefix(name, "kpi.") {
			fieldName := strings.TrimPrefix(name, "kpi.")
			reference, ok := kpiFieldReferences[fieldName]
			if !ok {
				return nil, fmt.Errorf("kpi.%s has no documentation field metadata", fieldName)
			}
			reference.Path = "kpi." + fieldName
			result = append(result, reference)
			continue
		}
		reference, ok := presentationFieldReferences[name]
		if !ok {
			return nil, fmt.Errorf("presentation.%s has no documentation field metadata", name)
		}
		reference.Path = "presentation." + name
		reference.Default = visualOptionDefault(name, chartType, reference.Default)
		result = append(result, reference)
	}
	if chartType == "map" {
		result = append(result,
			visualdocs.FieldReference{Path: "presentation.basemap", Type: "string", Default: "world_countries", AllowedValues: []string{"world_countries", "none"}, Description: "Selects the vendored, content-addressed world basemap or disables geographic context explicitly."},
			visualdocs.FieldReference{Path: "geo.layers", Type: "geographic layer list", AllowedValues: []string{"choropleth", "point", "heat", "density"}, Description: "Declares typed geographic layers and binds their geometry or coordinates to query aliases."},
		)
	}
	return result, nil
}

func visualOptionDefault(name, chartType, fallback string) string {
	switch name {
	case "curveness":
		if chartType == "graph" {
			return "0.18"
		}
		return "0.5"
	case "focus":
		if chartType == "tree" {
			return "descendant"
		}
		return "adjacency"
	case "orient":
		if chartType == "tree" {
			return "LR"
		}
		return "automatic"
	case "radius":
		if chartType == "donut" {
			return "48%, 72%"
		}
		return "0%, 72%"
	case "roam":
		switch chartType {
		case "graph", "map", "tree":
			return "true"
		default:
			return "false"
		}
	case "show_labels":
		switch chartType {
		case "pie", "donut", "funnel", "map":
			return "true"
		default:
			return "false"
		}
	case "smooth":
		if chartType == "line" || chartType == "area" {
			return "true"
		}
		return "false"
	case "symbol_size":
		if chartType == "scatter" {
			return "9"
		}
		return "7"
	default:
		return fallback
	}
}

func booleanOption(defaultValue, description string) visualdocs.FieldReference {
	return field("boolean", defaultValue, []string{"true", "false"}, description)
}

func field(valueType, defaultValue string, allowed []string, description string) visualdocs.FieldReference {
	return visualdocs.FieldReference{Type: valueType, Default: defaultValue, AllowedValues: allowed, Description: description}
}
