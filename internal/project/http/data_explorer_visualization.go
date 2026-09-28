package http

import (
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

const (
	dataExplorerTableViewID     = "table"
	dataExplorerChartViewID     = "chart"
	dataExplorerPivotViewID     = "pivot"
	dataExplorerVisualizationDB = "primary"
	dataExplorerPivotMaxColumns = 64
	dataExplorerPivotMaxCells   = 4096
)

type explorerVisualizationColumn struct {
	Output   string
	Semantic string
	Dataset  string
	Label    string
	Role     visualizationir.VisualizationFieldRole
	DataType visualizationir.VisualizationDataType
	Temporal bool
	Grain    string
}

// ProjectDataExplorerViews projects one successful governed result into
// validated shared visualization envelopes. It never executes queries. The
// table is always the recommended view when a safe projection is available;
// chart and pivot views are added only when their inputs satisfy the current
// IR and runtime contracts.
func ProjectDataExplorerViews(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) (map[string]visualizationir.VisualizationEnvelope, string, []string) {
	views := map[string]visualizationir.VisualizationEnvelope{}
	warnings := []string{}
	if result.RequestSeq < 0 {
		return views, dataExplorerTableViewID, []string{"visualization views require a valid governed result sequence"}
	}
	if result.Error != nil && strings.TrimSpace(*result.Error) != "" {
		return views, dataExplorerTableViewID, []string{"visualization views require a successful governed result"}
	}

	columns, columnWarning := explorerVisualizationColumns(spec, result, fields)
	if columnWarning != "" {
		warnings = append(warnings, columnWarning)
	}
	if len(columns) == 0 {
		return views, dataExplorerTableViewID, append(warnings, "visualization projection has no usable result columns")
	}

	limit := explorerVisualizationLimit(spec)
	if limit > 0 && int64(len(result.Rows)) > limit {
		result.Rows = append([]map[string]any(nil), result.Rows[:limit]...)
		result.Truncated = true
		warnings = append(warnings, "visualization frame was bounded to the exploration row limit")
	}
	if result.Rows == nil {
		result.Rows = []map[string]any{}
	}

	base := explorerVisualizationBase(spec, columns, result)
	frame := explorerVisualizationFrame(result, columns)
	table, err := explorerTableEnvelope(spec, base, frame, result, columns)
	if err != nil {
		return views, dataExplorerTableViewID, append(warnings, "table visualization projection failed validation")
	}
	views[dataExplorerTableViewID] = table

	if chart, ok, warning := explorerChartEnvelope(spec, base, frame, result, columns); ok {
		views[dataExplorerChartViewID] = chart
	} else if warning != "" {
		warnings = append(warnings, warning)
	}

	if pivot, ok, warning := explorerPivotEnvelope(spec, base, result, columns); ok {
		views[dataExplorerPivotViewID] = pivot
	} else if warning != "" {
		warnings = append(warnings, warning)
	}
	return views, dataExplorerTableViewID, boundedExplorerVisualizationWarnings(warnings)
}

func explorerVisualizationLimit(spec exploration.ExplorationSpec) int64 {
	if spec.Limit <= 0 || spec.Limit > 1000 {
		return 100
	}
	return int64(spec.Limit)
}

func explorerVisualizationBase(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn, result projectsignals.DataExploreResultSignal) visualizationir.VisualizationSpecBase {
	title := "Explore"
	var authoredBase *exploration.ExplorationVisualizationConfigBase
	if spec.Visualization != nil {
		if value, err := spec.Visualization.Base(); err == nil {
			authoredBase = value
		}
	}
	if authoredBase != nil && authoredBase.Title != nil && strings.TrimSpace(*authoredBase.Title) != "" {
		title = strings.TrimSpace(*authoredBase.Title)
	} else if dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID)); dataset != "" {
		title = "Explore " + dataset
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if result.Truncated {
		completeness = visualizationir.VisualizationCompletenessTruncated
	}
	return visualizationir.VisualizationSpecBase{
		Title:         title,
		Datasets:      []visualizationir.VisualizationDatasetSchema{{ID: dataExplorerVisualizationDB, Fields: explorerIRFields(columns)}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: explorerVisualizationLimit(spec), RequiredCompleteness: completeness},
		Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: "Governed Data Explorer result"},
		Interactions:  explorerVisualizationInteractions(columns),
	}
}

func explorerIRFields(columns []explorerVisualizationColumn) []visualizationir.VisualizationField {
	fields := make([]visualizationir.VisualizationField, 0, len(columns))
	for _, column := range columns {
		field := visualizationir.VisualizationField{ID: column.Output, Role: column.Role, DataType: column.DataType, Nullable: true, Label: column.Label}
		if column.Semantic != "" && column.Semantic != column.Output {
			field.SourceRef = &column.Semantic
		}
		if column.Temporal {
			grain := column.Grain
			if grain == "" {
				grain = "day"
			}
			field.Time = &visualizationir.VisualizationTemporalMetadata{Grain: grain, Timezone: "UTC", Calendar: "gregorian", WeekStart: visualizationir.VisualizationWeekStartMonday, Meaning: visualizationir.VisualizationTemporalMeaningBucket}
		}
		fields = append(fields, field)
	}
	return fields
}

func explorerVisualizationInteractions(columns []explorerVisualizationColumn) []visualizationir.VisualizationInteraction {
	mappings := make([]visualizationir.VisualizationInteractionMapping, 0, len(columns))
	for _, column := range columns {
		if column.Role != visualizationir.VisualizationFieldRoleDimension || column.Semantic == "" {
			continue
		}
		dataset := column.Dataset
		mapping := visualizationir.VisualizationInteractionMapping{Source: visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: column.Output}, TargetFieldID: column.Semantic}
		if dataset != "" {
			mapping.TargetDatasetID = &dataset
		}
		if column.Grain != "" {
			grain := column.Grain
			mapping.Grain = &grain
		}
		mappings = append(mappings, mapping)
	}
	if len(mappings) == 0 {
		return []visualizationir.VisualizationInteraction{}
	}
	mode := visualizationir.VisualizationSelectionModeMultiple
	if len(mappings) == 1 {
		mode = visualizationir.VisualizationSelectionModeSingle
	}
	return []visualizationir.VisualizationInteraction{{ID: "explore-drill", Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: mappings, Targets: []visualizationir.VisualizationInteractionTarget{}, Mode: mode}}
}

func explorerVisualizationFrame(result projectsignals.DataExploreResultSignal, columns []explorerVisualizationColumn) visualizationruntime.Frame {
	rows := make([][]any, len(result.Rows))
	for rowIndex, record := range result.Rows {
		rows[rowIndex] = make([]any, len(columns))
		for columnIndex, column := range columns {
			rows[rowIndex][columnIndex] = record[column.Output]
		}
	}
	columnNames := make([]string, len(columns))
	for index, column := range columns {
		columnNames[index] = column.Output
	}
	completeness := visualizationir.VisualizationCompletenessComplete
	if result.Truncated {
		completeness = visualizationir.VisualizationCompletenessTruncated
	}
	return visualizationruntime.Frame{Columns: columnNames, Rows: rows, Completeness: completeness}
}

func explorerVisualizationDataRevision(result projectsignals.DataExploreResultSignal) int64 {
	return result.RequestSeq
}

func explorerVisualizationGeneration(result projectsignals.DataExploreResultSignal) int64 {
	return result.RequestSeq
}

func explorerVisualizationDefinition(id string, spec visualizationir.VisualizationSpec, query visualizationdefinition.QueryBinding) (visualizationdefinition.Definition, error) {
	return visualizationdefinition.New(id, spec, query)
}

func boundedExplorerVisualizationWarnings(warnings []string) []string {
	if len(warnings) > 8 {
		warnings = warnings[:8]
	}
	return warnings
}
