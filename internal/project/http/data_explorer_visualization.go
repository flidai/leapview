package http

// This file is the deliberately small bridge between the governed Data
// Explorer result and the renderer-independent visualization IR.  It does
// not execute a query or select a renderer-owned protocol: all rows and
// metadata come from the already-authorized result signal.

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

const (
	dataExplorerTableViewID       = "table"
	dataExplorerKPIViewID         = "kpi"
	dataExplorerLineViewID        = "line"
	dataExplorerBarViewID         = "bar"
	dataExplorerDonutViewID       = "donut"
	dataExplorerPivotViewID       = "pivot"
	dataExplorerDatasetIDFallback = "result"
	dataExplorerPivotMaxColumns   = 64
	dataExplorerPivotMaxCells     = 4096
)

// DataExplorerVisualizationProjection contains only existing Visualization
// Envelope values. The table is always attempted and is the safe default.
type DataExplorerVisualizationProjection struct {
	Views           map[string]visualizationir.VisualizationEnvelope
	RecommendedView string
	DefaultView     string
	Warnings        []string
}

type explorerVisualizationColumn struct {
	Output   string
	Semantic string
	Dataset  string
	Label    string
	Role     visualizationir.VisualizationFieldRole
	DataType visualizationir.VisualizationDataType
	Temporal bool
	Grain    string
	Format   *visualizationir.VisualizationFormat
}

// ProjectDataExplorerViews lowers one canonical exploration result into
// validated IR envelopes. It is intentionally non-panicking: malformed or
// unsupported authored visualization settings only add a bounded warning and
// leave the table view selected.
func ProjectDataExplorerViews(spec exploration.ExplorationSpec, result projectsignals.DataExploreResultSignal, fields []projectsignals.DataExploreFieldSignal) DataExplorerVisualizationProjection {
	projection := DataExplorerVisualizationProjection{
		Views:           map[string]visualizationir.VisualizationEnvelope{},
		RecommendedView: dataExplorerTableViewID,
		DefaultView:     dataExplorerTableViewID,
	}
	columns, warnings := explorerVisualizationColumns(spec, result, fields)
	projection.Warnings = append(projection.Warnings, warnings...)
	if len(columns) == 0 {
		projection.Warnings = append(projection.Warnings, "visualization projection has no result columns; table view is unavailable")
		return projection
	}
	// The execution boundary normally truncates before projection. Keep this
	// boundary safe for callers that provide a result directly: never emit a
	// complete-looking pivot (or materialize rows beyond its configured window).
	if limit := explorerEffectiveLimit(spec); limit > 0 && int64(len(result.Rows)) > limit {
		projection.Warnings = append(projection.Warnings, fmt.Sprintf("result exceeds effective row budget %d; truncating visualization frame", limit))
		result.Rows = append([]map[string]any(nil), result.Rows[:limit]...)
		result.Truncated = true
	}

	modelID := strings.TrimSpace(spec.ModelID)
	if modelID == "" {
		modelID = "exploration"
	}
	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
	if datasetID == "" {
		datasetID = dataExplorerDatasetIDFallback
	}
	base := explorerVisualizationBase(spec, columns, result, result.Truncated)
	frame := explorerVisualizationFrame(result, columns)

	if envelope, err := explorerTableEnvelope(spec, base, frame, modelID, datasetID, result, columns); err == nil {
		projection.Views[dataExplorerTableViewID] = envelope
	} else {
		projection.Warnings = append(projection.Warnings, "table visualization projection failed: "+err.Error())
		return projection
	}

	viewID, candidate, warning := explorerVisualizationCandidate(spec, result, columns, base, frame, modelID, datasetID)
	if warning != "" {
		projection.Warnings = append(projection.Warnings, warning)
	}
	if candidate != nil {
		projection.Views[viewID] = *candidate
		projection.RecommendedView = viewID
	}
	return projection
}

func explorerVisualizationBase(spec exploration.ExplorationSpec, columns []explorerVisualizationColumn, result projectsignals.DataExploreResultSignal, truncated bool) visualizationir.VisualizationSpecBase {
	title := "Explore"
	var authoredBase *exploration.ExplorationVisualizationConfigBase
	if base, err := spec.Visualization.Base(); err == nil && base != nil {
		authoredBase = base
	}
	if authoredBase != nil && strings.TrimSpace(projectsignals.ValueOrZero(authoredBase.Title)) != "" {
		title = strings.TrimSpace(projectsignals.ValueOrZero(authoredBase.Title))
	} else if dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID)); dataset != "" {
		title = "Explore " + dataset
	}
	maxRows := explorerEffectiveLimit(spec)
	if maxRows <= 0 {
		maxRows = int64(len(result.Rows))
	}
	if maxRows < 1 {
		maxRows = 1
	}
	// A truncated result cannot promise complete data to the IR validator.
	required := visualizationir.VisualizationCompletenessComplete
	if truncated {
		required = visualizationir.VisualizationCompletenessTruncated
	}
	interactions := explorerVisualizationInteractions(columns)
	var subtitle *string
	if authoredBase != nil {
		subtitle = authoredBase.Subtitle
	}
	return visualizationir.VisualizationSpecBase{
		Kind:          title,
		Title:         title,
		Subtitle:      subtitle,
		Datasets:      []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: explorerIRFields(columns)}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: maxRows, RequiredCompleteness: required},
		Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: "Governed Data Explorer result"},
		Interactions:  interactions,
	}
}

func explorerIRFields(columns []explorerVisualizationColumn) []visualizationir.VisualizationField {
	fields := make([]visualizationir.VisualizationField, 0, len(columns))
	for _, column := range columns {
		field := visualizationir.VisualizationField{ID: column.Output, Role: column.Role, DataType: column.DataType, Nullable: true, Label: column.Label}
		if column.Semantic != "" && column.Semantic != column.Output {
			semantic := column.Semantic
			field.SourceRef = &semantic
		}
		if column.Temporal {
			field.Time = &visualizationir.VisualizationTemporalMetadata{Grain: column.Grain, Timezone: "UTC", Calendar: "gregorian", WeekStart: visualizationir.VisualizationWeekStartMonday, Meaning: visualizationir.VisualizationTemporalMeaningBucket}
		}
		field.Format = column.Format
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
		targetDataset := column.Dataset
		mapping := visualizationir.VisualizationInteractionMapping{
			Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: column.Output}, TargetFieldID: column.Semantic,
		}
		if targetDataset != "" {
			mapping.TargetDatasetID = &targetDataset
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
	// The existing interaction command path consumes selection interactions;
	// the stable ID and semantic target preserve the drill lineage without
	// inventing a renderer-specific drill protocol.
	return []visualizationir.VisualizationInteraction{{ID: "explore-drill", Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: mappings, Targets: []visualizationir.VisualizationInteractionTarget{}, Mode: mode, RequiresStableIdentity: false}}
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
