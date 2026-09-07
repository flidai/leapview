package http

import (
	"fmt"
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
	return columns, warnings
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
