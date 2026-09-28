package http

import (
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerVisualizationColumns(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) ([]explorerVisualizationColumn, string) {
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		if field.ID != "" {
			fieldByID[field.ID] = field
		}
	}
	aliases := explorerVisualizationAliases(spec)
	semanticByOutput := make(map[string]string, len(aliases))
	for semantic, alias := range aliases {
		if alias != "" {
			if previous, exists := semanticByOutput[alias]; exists && previous != semantic {
				return nil, "visualization projection rejected ambiguous result column aliases"
			}
			semanticByOutput[alias] = semantic
		}
	}
	selectedMetrics := make(map[string]struct{}, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		selectedMetrics[metric.Field] = struct{}{}
	}
	if spec.Pivot != nil {
		for _, metric := range spec.Pivot.Metrics {
			selectedMetrics[metric.Field] = struct{}{}
		}
	}

	columns := make([]explorerVisualizationColumn, 0, len(result.Columns))
	seen := make(map[string]struct{}, len(result.Columns))
	for _, resultColumn := range result.Columns {
		output := strings.TrimSpace(resultColumn.Key)
		if output == "" {
			return nil, "visualization projection rejected a result column without a key"
		}
		if _, exists := seen[output]; exists {
			return nil, "visualization projection rejected duplicate result column keys"
		}
		seen[output] = struct{}{}

		semantic := output
		if _, ok := fieldByID[output]; !ok {
			if field, ok := semanticByOutput[output]; ok {
				semantic = field
			}
		}
		metadata, hasMetadata := fieldByID[semantic]
		role := visualizationir.VisualizationFieldRoleDimension
		typeName := strings.TrimSpace(projectsignals.ValueOrZero(resultColumn.Type))
		dataType := explorerVisualizationDataType(typeName)
		dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
		label := firstExplorerNonEmpty(resultColumn.Label, output)
		if hasMetadata {
			dataset = firstExplorerNonEmpty(metadata.DatasetID, dataset)
			label = firstExplorerNonEmpty(resultColumn.Label, metadata.Label, output)
			if metadata.Kind == "metric" {
				role = visualizationir.VisualizationFieldRoleMetric
			}
			if metadata.Type != nil && strings.TrimSpace(*metadata.Type) != "" {
				dataType = explorerVisualizationDataType(*metadata.Type)
				typeName = strings.TrimSpace(*metadata.Type)
			}
		}
		if typeName == "" {
			dataType = explorerVisualizationInferDataType(result.Rows, output)
		}
		if _, metric := selectedMetrics[semantic]; metric {
			role = visualizationir.VisualizationFieldRoleMetric
		}
		for fieldID, alias := range aliases {
			if alias == output {
				if _, metric := selectedMetrics[fieldID]; metric {
					role = visualizationir.VisualizationFieldRoleMetric
				}
			}
		}
		if role == visualizationir.VisualizationFieldRoleMetric {
			if observedType, ok := explorerVisualizationNumericType(result.Rows, output); ok {
				// A governed metric is numeric. When catalog metadata describes
				// its aggregate operation rather than its result type, prefer a
				// homogeneous numeric frame observed in the governed result.
				if dataType == visualizationir.VisualizationDataTypeString || dataType == visualizationir.VisualizationDataTypeFloat || dataType == visualizationir.VisualizationDataTypeInteger || observedType == visualizationir.VisualizationDataTypeFloat {
					dataType = observedType
				}
			}
		}

		grain := ""
		if spec.Time != nil && (semantic == spec.Time.Field || aliases[spec.Time.Field] == output) {
			grain = string(spec.Time.Grain)
		}
		for _, dimension := range spec.Dimensions {
			if semantic == dimension.Field || aliases[dimension.Field] == output {
				if dimension.Grain != nil {
					grain = string(*dimension.Grain)
				}
			}
		}
		temporal := dataType == visualizationir.VisualizationDataTypeDate || dataType == visualizationir.VisualizationDataTypeTemporal
		if grain != "" {
			temporal = true
		}
		columns = append(columns, explorerVisualizationColumn{Output: output, Semantic: semantic, Dataset: dataset, Label: label, Role: role, DataType: dataType, Temporal: temporal, Grain: grain})
	}
	return columns, ""
}

func explorerVisualizationInferDataType(rows []map[string]any, key string) visualizationir.VisualizationDataType {
	for _, row := range rows {
		switch row[key].(type) {
		case bool:
			return visualizationir.VisualizationDataTypeBoolean
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return visualizationir.VisualizationDataTypeInteger
		case float32, float64:
			return visualizationir.VisualizationDataTypeFloat
		}
	}
	return visualizationir.VisualizationDataTypeString
}

func explorerVisualizationNumericType(rows []map[string]any, key string) (visualizationir.VisualizationDataType, bool) {
	observed := false
	dataType := visualizationir.VisualizationDataTypeInteger
	for _, row := range rows {
		value := row[key]
		if value == nil {
			continue
		}
		observed = true
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			continue
		case float32, float64:
			dataType = visualizationir.VisualizationDataTypeFloat
		default:
			return "", false
		}
	}
	return dataType, observed
}

func explorerVisualizationDataType(value string) visualizationir.VisualizationDataType {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "bool", "boolean":
		return visualizationir.VisualizationDataTypeBoolean
	case "int", "integer", "int32", "int64":
		return visualizationir.VisualizationDataTypeInteger
	case "count", "count_distinct", "integer_count":
		return visualizationir.VisualizationDataTypeInteger
	case "sum", "avg", "average", "min", "max", "decimal", "numeric", "number_sum", "number_average":
		return visualizationir.VisualizationDataTypeDecimal
	case "float", "float32", "float64", "number", "double":
		return visualizationir.VisualizationDataTypeFloat
	case "date":
		return visualizationir.VisualizationDataTypeDate
	case "datetime", "timestamp", "time", "temporal":
		return visualizationir.VisualizationDataTypeTemporal
	case "geography", "geometry", "geographic":
		return visualizationir.VisualizationDataTypeGeographic
	default:
		return visualizationir.VisualizationDataTypeString
	}
}

func explorerVisualizationAliases(spec exploration.ExplorationSpec) map[string]string {
	selected := make([]string, 0, len(spec.Dimensions)+len(spec.Metrics)+1)
	aliases := make(map[string]string, cap(selected))
	selectedSet := map[string]struct{}{}
	appendSelected := func(field string) {
		if _, exists := selectedSet[field]; exists {
			return
		}
		selectedSet[field] = struct{}{}
		selected = append(selected, field)
	}
	for _, dimension := range spec.Dimensions {
		appendSelected(dimension.Field)
		if dimension.Alias != nil && strings.TrimSpace(*dimension.Alias) != "" {
			aliases[dimension.Field] = strings.TrimSpace(*dimension.Alias)
		}
	}
	for _, metric := range spec.Metrics {
		appendSelected(metric.Field)
		if metric.Alias != nil && strings.TrimSpace(*metric.Alias) != "" {
			aliases[metric.Field] = strings.TrimSpace(*metric.Alias)
		}
	}
	if spec.Time != nil {
		if !containsString(selected, spec.Time.Field) {
			appendSelected(spec.Time.Field)
		}
		if spec.Time.Alias != nil && strings.TrimSpace(*spec.Time.Alias) != "" && aliases[spec.Time.Field] == "" {
			aliases[spec.Time.Field] = strings.TrimSpace(*spec.Time.Alias)
		}
	}
	if spec.Pivot != nil {
		for _, dimension := range append(append([]exploration.ExplorationDimensionRef{}, spec.Pivot.Rows...), spec.Pivot.Columns...) {
			appendSelected(dimension.Field)
			if dimension.Alias != nil && strings.TrimSpace(*dimension.Alias) != "" {
				aliases[dimension.Field] = strings.TrimSpace(*dimension.Alias)
			}
		}
		for _, metric := range spec.Pivot.Metrics {
			appendSelected(metric.Field)
			if metric.Alias != nil && strings.TrimSpace(*metric.Alias) != "" {
				aliases[metric.Field] = strings.TrimSpace(*metric.Alias)
			}
		}
	}
	counts := make(map[string]int, len(selected))
	for _, field := range selected {
		counts[explorerFieldName(field)]++
	}
	for _, field := range selected {
		if aliases[field] != "" {
			continue
		}
		name := explorerFieldName(field)
		if table, fieldName, found := strings.Cut(field, "."); found && counts[fieldName] > 1 {
			name = table + "__" + fieldName
		}
		aliases[field] = name
	}
	return aliases
}

func explorerVisualizationColumnFor(columns []explorerVisualizationColumn, ref string) (explorerVisualizationColumn, bool) {
	ref = strings.TrimSpace(ref)
	for _, column := range columns {
		if column.Output == ref || column.Semantic == ref {
			return column, true
		}
	}
	return explorerVisualizationColumn{}, false
}

func explorerVisualizationSelectedDimensions(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn) []explorerVisualizationColumn {
	selected := make([]explorerVisualizationColumn, 0, len(spec.Dimensions)+1)
	seen := map[string]struct{}{}
	appendField := func(ref string) {
		column, ok := explorerVisualizationColumnFor(columns, ref)
		if !ok || column.Role == visualizationir.VisualizationFieldRoleMetric {
			return
		}
		if _, exists := seen[column.Output]; exists {
			return
		}
		seen[column.Output] = struct{}{}
		selected = append(selected, column)
	}
	for _, dimension := range spec.Dimensions {
		appendField(dimension.Field)
	}
	if spec.Time != nil {
		appendField(spec.Time.Field)
	}
	return selected
}

func explorerVisualizationSelectedMetrics(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn) []explorerVisualizationColumn {
	selected := make([]explorerVisualizationColumn, 0, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		if column, ok := explorerVisualizationColumnFor(columns, metric.Field); ok && column.Role == visualizationir.VisualizationFieldRoleMetric {
			selected = append(selected, column)
		}
	}
	return selected
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
