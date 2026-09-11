package http

import (
	"fmt"
	"reflect"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerVisualizationColumns(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) ([]explorerVisualizationColumn, []string) {
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	aliases := make(map[string]string)
	for _, field := range spec.Dimensions {
		aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
	}
	for _, field := range spec.Metrics {
		aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
	}
	if spec.Pivot != nil {
		for _, field := range spec.Pivot.Rows {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
		for _, field := range spec.Pivot.Columns {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
		for _, field := range spec.Pivot.Metrics {
			aliases[field.Field] = firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), field.Field)
		}
	}
	if spec.Time != nil {
		aliases[spec.Time.Field] = firstExplorerNonEmpty(aliases[spec.Time.Field], projectsignals.ValueOrZero(spec.Time.Alias), spec.Time.Field)
	}
	// Result columns use the deterministic query aliases (for example,
	// `status` for `orders.status`) when no authored alias exists. Keep the
	// semantic field ID qualified so pivot totals and drill targets retain
	// lineage while still matching the returned output key.
	for field, alias := range explorerSpecQueryAliases(spec) {
		if aliases[field] == "" || aliases[field] == field {
			aliases[field] = alias
		}
	}
	columns := make([]explorerVisualizationColumn, 0, len(result.Columns))
	warnings := []string{}
	seen := map[string]struct{}{}
	for _, column := range result.Columns {
		output := strings.TrimSpace(column.Key)
		if output == "" {
			warnings = append(warnings, "visualization projection ignored a result column without a key")
			continue
		}
		if _, exists := seen[output]; exists {
			warnings = append(warnings, fmt.Sprintf("visualization projection ignored duplicate result column %q", output))
			continue
		}
		seen[output] = struct{}{}
		semantic := output
		if field, ok := fieldByID[output]; ok {
			semantic = field.ID
		} else {
			for fieldID, alias := range aliases {
				if alias == output {
					semantic = fieldID
					break
				}
			}
		}
		metadata, hasMetadata := fieldByID[semantic]
		role := visualizationir.VisualizationFieldRoleDimension
		dataType := visualizationDataType(projectsignals.ValueOrZero(column.Type))
		dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
		label := strings.TrimSpace(column.Label)
		if hasMetadata {
			dataset = firstExplorerNonEmpty(metadata.DatasetID, dataset)
			label = firstExplorerNonEmpty(label, metadata.Label, output)
			if metadata.Kind == "metric" {
				role = visualizationir.VisualizationFieldRoleMetric
			}
			if metadata.Type != nil {
				dataType = visualizationDataType(*metadata.Type)
			}
		} else {
			label = firstExplorerNonEmpty(label, output)
			for _, metric := range spec.Metrics {
				if metric.Field == semantic || aliases[metric.Field] == output {
					role = visualizationir.VisualizationFieldRoleMetric
				}
			}
		}
		temporal := dataType == visualizationir.VisualizationDataTypeDate || dataType == visualizationir.VisualizationDataTypeTemporal
		grain := ""
		if spec.Time != nil && (spec.Time.Field == semantic || aliases[spec.Time.Field] == output) {
			temporal = true
			grain = string(spec.Time.Grain)
		}
		for _, dimension := range spec.Dimensions {
			if dimension.Field == semantic || aliases[dimension.Field] == output {
				if dimension.Grain != nil {
					grain = string(*dimension.Grain)
				}
			}
		}
		columns = append(columns, explorerVisualizationColumn{Output: output, Semantic: semantic, Dataset: dataset, Label: label, Role: role, DataType: dataType, Temporal: temporal, Grain: grain})
	}
	if explorerApplyAuthoredVisualizationFormats(spec, columns, &warnings) {
		for index := range columns {
			columns[index].FormatFallback = true
		}
	}
	return columns, warnings
}

type explorerAuthoredVisualizationField struct {
	Name string
	Ref  exploration.ExplorationVisualizationFieldRef
}

func explorerApplyAuthoredVisualizationFormats(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn, warnings *[]string) bool {
	refs := explorerAuthoredVisualizationFields(spec)
	formats := make(map[string]*visualizationir.VisualizationFormat, len(refs))
	conflicts := make(map[string]struct{})
	fallback := false
	for _, authored := range refs {
		if authored.Ref.Format == nil {
			continue
		}
		index, ok := explorerColumnIndexFor(columns, authored.Ref.Field)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("authored %s format field %q is unavailable in the governed result; showing table", authored.Name, authored.Ref.Field))
			fallback = true
			continue
		}
		format, err := explorerVisualizationFormatChecked(authored.Ref.Format)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("authored %s format for field %q is not representable in visualization IR: %v; showing table", authored.Name, authored.Ref.Field, err))
			columns[index].Format = nil
			fallback = true
			continue
		}
		output := columns[index].Output
		if _, exists := conflicts[output]; exists {
			continue
		}
		if previous, exists := formats[output]; exists {
			if !reflect.DeepEqual(previous, format) {
				*warnings = append(*warnings, fmt.Sprintf("authored visualization formats for field %q conflict; showing table", authored.Ref.Field))
				columns[index].Format = nil
				delete(formats, output)
				conflicts[output] = struct{}{}
				fallback = true
			}
			continue
		}
		formats[output] = format
		columns[index].Format = format
	}
	return fallback
}

func explorerAuthoredVisualizationFields(spec exploration.ExplorationSpec) []explorerAuthoredVisualizationField {
	if spec.Visualization == nil || spec.Visualization.Value == nil {
		return nil
	}
	// A decoded union may hold a typed nil pointer. Treat that malformed input
	// as having no authored fields so projection can continue to its safe table
	// path without panicking.
	value := reflect.ValueOf(spec.Visualization.Value)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return nil
	}
	refs := make([]explorerAuthoredVisualizationField, 0, 8)
	add := func(name string, ref exploration.ExplorationVisualizationFieldRef) {
		refs = append(refs, explorerAuthoredVisualizationField{Name: name, Ref: ref})
	}
	addOptional := func(name string, ref *exploration.ExplorationVisualizationFieldRef) {
		if ref != nil {
			add(name, *ref)
		}
	}
	addList := func(name string, values []exploration.ExplorationVisualizationFieldRef) {
		for index, ref := range values {
			add(fmt.Sprintf("%s[%d]", name, index), ref)
		}
	}
	switch value := spec.Visualization.Value.(type) {
	case *exploration.CartesianExplorationVisualization:
		addOptional("cartesian x", value.X)
		if value.Y != nil {
			addList("cartesian y", *value.Y)
		}
		addOptional("cartesian series", value.Series)
	case *exploration.KPIExplorationVisualization:
		add("KPI value", value.Value)
		addOptional("KPI comparison", value.Comparison)
		addOptional("KPI goal", value.Goal)
		if value.Trend != nil {
			add("KPI trend category", value.Trend.Category)
			add("KPI trend value", value.Trend.Value)
		}
	case *exploration.ProportionalExplorationVisualization:
		add("proportional category", value.Category)
		add("proportional value", value.Value)
		addOptional("proportional series", value.Series)
	}
	return refs
}

func explorerMalformedVisualizationWarning(spec exploration.ExplorationSpec) string {
	if spec.Visualization == nil {
		return ""
	}
	if spec.Visualization.Value == nil {
		return "authored visualization is missing a variant; showing table"
	}
	value := reflect.ValueOf(spec.Visualization.Value)
	if value.Kind() == reflect.Ptr && value.IsNil() {
		return "authored visualization has a nil variant; showing table"
	}
	return ""
}

func visualizationDataType(value string) visualizationir.VisualizationDataType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "bool", "boolean":
		return visualizationir.VisualizationDataTypeBoolean
	case "int", "integer", "int32", "int64":
		return visualizationir.VisualizationDataTypeInteger
	case "decimal", "numeric":
		return visualizationir.VisualizationDataTypeDecimal
	case "float", "float32", "float64", "number":
		return visualizationir.VisualizationDataTypeFloat
	case "date":
		return visualizationir.VisualizationDataTypeDate
	case "datetime", "timestamp", "time", "temporal":
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}

func explorerColumnIndexFor(columns []explorerVisualizationColumn, ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	for index, column := range columns {
		if column.Output == ref || column.Semantic == ref {
			return index, true
		}
	}
	return 0, false
}
