package main

import (
	"fmt"

	"github.com/flidai/leapview/internal/app/site/visualdocs"
)

// These maps describe fields from the canonical Dashboard document. Keep the
// keys identical to their wire paths: compatibility aliases and retired
// snake_case names must never become documentation contracts again.
var queryFieldReferences = map[string]visualdocs.FieldReference{
	"datasets": {
		Type:        "named query mapping",
		Description: "Declares bounded context queries that inherit the visual's active semantic filters.",
	},
	"dataset": {
		Type:        "string",
		Description: "Selects the semantic dataset for a records query.",
	},
	"dimensions": {
		Type:        "field mapping",
		Description: "Groups query results and supplies category or hierarchy labels.",
	},
	"metrics": {
		Type:        "metric mapping",
		Description: "Selects governed semantic metrics consumed by the visual shape.",
	},
	"rows": {
		Type:        "field mapping",
		Description: "Selects row dimensions for a pivot query.",
	},
	"columns": {
		Type:        "field mapping",
		Description: "Selects column dimensions for a pivot query.",
	},
	"fields": {
		Type:        "field mapping",
		Description: "Selects physical record fields for a records query.",
	},
	"field": {
		Type:        "field reference",
		Description: "Selects the governed metric used by a histogram or distribution query.",
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

var presentationFieldReferences = map[string]visualdocs.FieldReference{
	"displayUnits":          field("string", "auto", []string{"auto", "none", "thousands", "millions", "billions", "trillions"}, "Chooses one governed magnitude for the complete visual scope."),
	"legend":                field("string", "none", []string{"none", "top", "right", "bottom", "left"}, "Controls the renderer-neutral legend position."),
	"labels":                field("label policy", "hidden", []string{"hidden", "automatic", "dense", "always"}, "Controls deterministic label density, priority, truncation, and tooltip fallback."),
	"stacking":              field("string", "none", []string{"none", "normal", "percent"}, "Declares explicit normal or 100% stacking."),
	"orientation":           field("string", "renderer default", []string{"horizontal", "vertical"}, "Controls the direction of a compatible visual."),
	"rose":                  booleanOption("false", "Uses radial sector magnitude for a rose pie."),
	"centerLabel":           field("string", "none", nil, "Adds a governed text label at the center of a donut."),
	"innerRadius":           field("number", "renderer default", []string{"number from 0 through 1"}, "Sets the inner proportional radius."),
	"outerRadius":           field("number", "renderer default", []string{"number from 0 through 1"}, "Sets the outer proportional radius."),
	"align":                 field("string", "renderer default", []string{"left", "center", "right"}, "Aligns compatible proportional stages."),
	"sort":                  field("string", "query order", []string{"ascending", "descending"}, "Orders compatible proportional stages without changing query semantics."),
	"initialDepth":          field("integer", "all levels", []string{"non-negative integer"}, "Limits the hierarchy depth expanded on first render."),
	"roam":                  booleanOption("false", "Allows governed hierarchy panning and zooming."),
	"layout":                field("string", "standard", []string{"standard", "circular"}, "Selects the deterministic hierarchy or graph layout."),
	"breadcrumb":            booleanOption("false", "Shows hierarchy navigation context."),
	"nodeGap":               field("number", "renderer default", []string{"non-negative number"}, "Sets the spacing between hierarchy or flow nodes."),
	"curveness":             field("number", "renderer default", []string{"number from 0 through 1"}, "Controls the curvature of graph and flow edges."),
	"focus":                 field("string", "none", []string{"none", "adjacency"}, "Controls whether interaction emphasizes adjacent graph nodes and edges."),
	"showSymbols":           booleanOption("true", "Shows point symbols on compatible Cartesian charts."),
	"smooth":                booleanOption("true", "Uses curved interpolation on compatible line and area charts."),
	"step":                  booleanOption("false", "Renders line segments as discrete steps between ordered categories."),
	"dataZoom":              booleanOption("false", "Adds bounded zoom controls to supported Cartesian charts."),
	"symbolSize":            field("number", "renderer default", []string{"positive number"}, "Sets point symbol size for compatible Cartesian charts."),
	"labelPosition":         field("string", "renderer default", []string{"automatic", "inside", "outside", "top"}, "Positions labels relative to their marks."),
	"identity":              field("result field list", "required", nil, "Binds one or more governed result fields as stable point identity."),
	"x":                     field("result field", "required", nil, "Binds the governed result field used for the horizontal point coordinate."),
	"y":                     field("result field", "required", nil, "Binds the governed result field used for the vertical point coordinate."),
	"size":                  field("result field", "none", nil, "Binds an optional governed numeric result field to point size."),
	"color":                 field("result field", "none", nil, "Binds an optional governed result field to point color."),
	"series":                field("result field", "none", nil, "Binds an optional governed result field to point series."),
	"label":                 field("result field", "none", nil, "Binds an optional governed result field to point labels."),
	"tooltip":               field("result field list", "none", nil, "Binds governed result fields exposed in point tooltips."),
	"colorScale":            field("point color scale", "none", []string{"categorical", "quantitative"}, "Constrains the governed point color channel and optional numeric domain."),
	"sizeScale":             field("point size scale", "none", nil, "Constrains the governed point size channel with explicit pixel bounds."),
	"overplot":              field("point overplot policy", "opacity", []string{"show_all", "opacity"}, "Controls deterministic point overlap, opacity, and large-result behavior."),
	"minimum":               field("number", "renderer default", nil, "Sets the lower bound of a polar scale."),
	"maximum":               field("number", "renderer default", nil, "Sets the upper bound of a polar scale."),
	"target":                field("number", "none", nil, "Marks an explicit target on a polar scale."),
	"showPointer":           booleanOption("true", "Shows the gauge pointer."),
	"area":                  booleanOption("false", "Fills the governed radar area."),
	"progressWidth":         field("number", "renderer default", []string{"positive number"}, "Sets the gauge progress arc width."),
	"rowHeight":             field("integer", "required", []string{"positive integer"}, "Sets the tabular row height."),
	"showHeader":            booleanOption("true", "Shows semantic table headers."),
	"striped":               booleanOption("false", "Alternates tabular row backgrounds."),
	"conditionalFormatting": field("closed conditional-format list", "none", []string{"gradient", "rules", "field"}, "Applies governed renderer-neutral styles to compiled result fields with explicit null and default outcomes."),
	"note":                  field("string", "none", nil, "Adds supporting context below a KPI value."),
	"tone":                  field("string", "neutral", []string{"neutral", "ink", "success", "warning", "danger"}, "Sets the semantic accent tone of a KPI card."),
	"mode":                  field("string", "compact", []string{"compact", "bullet", "progress"}, "Selects the KPI value presentation and its required semantic bindings."),
	"delta":                 field("string", "absolute", []string{"absolute", "relative"}, "Chooses absolute or relative comparison change."),
	"favorableDirection":    field("string", "neutral", []string{"increase", "decrease", "neutral"}, "States whether an increase or decrease is favorable."),
	"missingComparison":     field("string", "show_unavailable", []string{"show_unavailable", "hide"}, "Controls how an unavailable comparison is communicated."),
	"ranges":                field("range list", "none", []string{"ordered non-overlapping ranges"}, "Classifies KPI values with explicit labels and semantic tones."),
	"thresholds":            field("threshold list", "none", []string{"numeric thresholds"}, "Adds explicit KPI threshold markers with semantic tones."),
	"comparison":            field("KPI value binding", "none", nil, "Binds a governed comparison value from a named result dataset."),
	"goal":                  field("KPI value binding", "none", nil, "Binds a governed goal value from a named result dataset."),
	"trend":                 field("KPI trend binding", "none", nil, "Binds category and value fields for the KPI trend sparkline."),
	"theme":                 field("string", "auto", []string{"auto", "light", "dark"}, "Selects the map basemap theme."),
	"basemap":               field("string", "streets", []string{"streets", "blank"}, "Selects a pinned basemap asset or an intentionally blank background."),
	"labelDensity":          field("string", "normal", []string{"hidden", "normal", "dense"}, "Controls map label density."),
	"camera":                field("map camera", "fit_data", nil, "Configures bounded map camera behavior."),
	"controls":              field("map controls", "zoom, reset, compass", nil, "Enables explicit map controls."),
	"layers":                field("geographic layer list", "point", []string{"point", "choropleth", "heat", "density", "reference", "path"}, "Declares typed geographic layers and governed field bindings."),
}

func visualFieldReferences(queryFields, optionFields []string, chartType string) ([]visualdocs.FieldReference, error) {
	result := make([]visualdocs.FieldReference, 0, len(queryFields)+len(optionFields))
	for _, name := range queryFields {
		reference, ok := queryFieldReferences[name]
		if !ok {
			return nil, fmt.Errorf("query.%s has no canonical documentation field metadata", name)
		}
		if name == "datasets" {
			reference.Path = "datasets"
		} else {
			reference.Path = "query." + name
		}
		result = append(result, reference)
	}
	for _, name := range optionFields {
		reference, ok := presentationFieldReferences[name]
		if !ok {
			return nil, fmt.Errorf("presentation.%s has no canonical documentation field metadata", name)
		}
		if name == "series" && chartType == "combo" {
			reference = field("closed combo series list", "none", []string{"line", "area", "bar", "column"}, "Binds each compiled metric result field to one supported mark and the primary or secondary value axis.")
		}
		reference.Path = "presentation." + name
		reference.Default = visualOptionDefault(name, chartType, reference.Default)
		result = append(result, reference)
	}
	return result, nil
}

func visualOptionDefault(name, chartType, fallback string) string {
	switch name {
	case "smooth":
		if chartType == "line" || chartType == "area" {
			return "true"
		}
		return "false"
	case "symbolSize":
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
