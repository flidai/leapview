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
	"displayUnits":  field("string", "auto", []string{"auto", "none", "thousands", "millions", "billions", "trillions"}, "Chooses one governed magnitude for the complete visual scope."),
	"legend":        field("string", "none", []string{"none", "top", "right", "bottom", "left"}, "Controls the renderer-neutral legend position."),
	"labels":        field("label policy", "hidden", []string{"hidden", "automatic", "dense", "always"}, "Controls deterministic label density, priority, truncation, and tooltip fallback."),
	"stacking":      field("string", "none", []string{"none", "normal", "percent"}, "Declares explicit normal or 100% stacking."),
	"orientation":   field("string", "renderer default", []string{"horizontal", "vertical"}, "Controls the direction of a compatible visual."),
	"showSymbols":   booleanOption("true", "Shows point symbols on compatible Cartesian charts."),
	"smooth":        booleanOption("true", "Uses curved interpolation on compatible line and area charts."),
	"dataZoom":      booleanOption("false", "Adds bounded zoom controls to supported Cartesian charts."),
	"symbolSize":    field("number", "renderer default", []string{"positive number"}, "Sets point symbol size for compatible Cartesian charts."),
	"labelPosition": field("string", "renderer default", []string{"automatic", "inside", "outside", "top"}, "Positions labels relative to their marks."),
	"rowHeight":     field("integer", "required", []string{"positive integer"}, "Sets the tabular row height."),
	"showHeader":    booleanOption("true", "Shows semantic table headers."),
	"striped":       booleanOption("false", "Alternates tabular row backgrounds."),
	"note":          field("string", "none", nil, "Adds supporting context below a KPI value."),
	"tone":          field("string", "neutral", []string{"neutral", "ink", "success", "warning", "danger"}, "Sets the semantic accent tone of a KPI card."),
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
