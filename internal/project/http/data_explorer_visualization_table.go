package http

import (
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerTableEnvelope(spec exploration.ExplorationSpec, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn) (visualizationir.VisualizationEnvelope, error) {
	selected, authored := explorerVisualizationTableColumns(spec, sourceColumns)
	if len(selected) == 0 {
		return visualizationir.VisualizationEnvelope{}, errExplorerNoTableColumns
	}
	base.Kind = "table"
	base.Datasets = []visualizationir.VisualizationDatasetSchema{{ID: dataExplorerVisualizationDB, Fields: explorerIRFields(selected)}}
	base.Interactions = explorerVisualizationInteractions(selected)
	columns := make([]visualizationir.TableVisualizationColumn, 0, len(selected))
	fields := make([]visualizationdefinition.FieldBinding, 0, len(selected))
	dashboardColumns := make([]dashboard.TableColumn, 0, len(selected))
	for index, source := range selected {
		label := source.Label
		width := (*int64)(nil)
		if authored != nil && index < len(authored) {
			if authored[index].Label != nil && strings.TrimSpace(*authored[index].Label) != "" {
				label = strings.TrimSpace(*authored[index].Label)
			}
			if authored[index].Width != nil {
				converted := int64(*authored[index].Width)
				width = &converted
			}
		}
		columns = append(columns, visualizationir.TableVisualizationColumn{Field: visualizationir.VisualizationFieldRef{Dataset: dataExplorerVisualizationDB, Field: source.Output}, Label: label, Width: width, Formatting: []visualizationir.TableVisualizationFormattingRule{}})
		fields = append(fields, visualizationdefinition.FieldBinding{FieldID: source.Semantic, Alias: source.Output, Grain: source.Grain})
		role := "dimension"
		align := ""
		if source.Role == visualizationir.VisualizationFieldRoleMetric {
			role, align = "metric", "right"
		}
		dashboardColumns = append(dashboardColumns, dashboard.TableColumn{Key: source.Output, Label: label, Role: role, Align: align, DataType: string(source.DataType)})
	}
	rowHeight, striped, showHeader := int64(32), false, true
	if table := spec.Table; table != nil {
		if table.RowHeight != nil {
			rowHeight = int64(*table.RowHeight)
		} else if table.Density != nil {
			switch *table.Density {
			case exploration.ExplorationTableDensityCompact:
				rowHeight = 28
			case exploration.ExplorationTableDensityComfortable:
				rowHeight = 36
			}
		}
		if table.Striped != nil {
			striped = *table.Striped
		}
		if table.ShowHeader != nil {
			showHeader = *table.ShowHeader
		}
	}
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{
		VisualizationSpecBase: base, Kind: "table", Columns: columns,
		Presentation: visualizationir.GridVisualizationPresentation{RowHeight: rowHeight, Striped: striped, ShowHeader: showHeader},
	}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow,
		ModelID: explorerVisualizationModelID(spec), DatasetID: dataExplorerVisualizationDB,
		Detail: &visualizationdefinition.DetailQueryBinding{TableID: explorerVisualizationDatasetID(spec), Fields: fields, Limit: base.DataBudget.MaxRows},
	}
	definition, err := explorerVisualizationDefinition(dataExplorerTableViewID, visualSpec, query)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}

	rows := make([]map[string]any, len(result.Rows))
	for rowIndex, source := range result.Rows {
		row := make(map[string]any, len(selected))
		for _, column := range selected {
			row[column.Output] = source[column.Output]
		}
		rows[rowIndex] = row
	}
	rowCount := len(rows)
	sort := dashboard.TableSort{Key: selected[0].Output, Direction: "asc"}
	for _, authoredSort := range spec.Sort {
		if column, ok := explorerVisualizationColumnFor(selected, authoredSort.Field); ok {
			sort.Key = column.Output
			if authoredSort.Direction == exploration.ExplorationSortDirectionDesc {
				sort.Direction = "desc"
			}
			break
		}
	}
	cardinality := dashboard.ExactCardinality(rowCount)
	if result.Truncated {
		cardinality = dashboard.LowerBoundCardinality(rowCount)
	}
	requestSeq := result.RequestSeq
	if requestSeq < 0 {
		requestSeq = 0
	}
	chunkSize := max(1, rowCount)
	table := dashboard.Table{
		Kind: "table", Title: base.Title, Columns: dashboardColumns, Cardinality: cardinality,
		AvailableRows: rowCount, IsCapped: result.Truncated, RowCap: int(base.DataBudget.MaxRows), ChunkSize: chunkSize,
		RowHeight: int(rowHeight), ResetVersion: 0, Sort: sort,
		Blocks: map[string]dashboard.TableBlock{"a": {Start: 0, RequestSeq: int(requestSeq), ResetVersion: 0, Sort: sort, Rows: rows}},
	}
	return visualizationruntime.WindowEnvelopeFromDefinition(definition, table, explorerVisualizationDataRevision(result), explorerVisualizationGeneration(result))
}

var errExplorerNoTableColumns = errorExplorerNoTableColumns{}

type errorExplorerNoTableColumns struct{}

func (errorExplorerNoTableColumns) Error() string { return "visualization table has no columns" }

func explorerVisualizationTableColumns(spec exploration.ExplorationSpec, source []explorerVisualizationColumn) ([]explorerVisualizationColumn, []exploration.ExplorationTableColumn) {
	var requested []exploration.ExplorationTableColumn
	if spec.Table != nil && spec.Table.Columns != nil && len(*spec.Table.Columns) > 0 {
		requested = *spec.Table.Columns
	} else if spec.Visualization != nil {
		if table, ok := spec.Visualization.Value.(*exploration.TableExplorationVisualization); ok && table != nil {
			for _, column := range table.Columns {
				requested = append(requested, exploration.ExplorationTableColumn{Field: column.Field})
			}
		}
	}
	if len(requested) == 0 {
		return append([]explorerVisualizationColumn(nil), source...), nil
	}
	selected := make([]explorerVisualizationColumn, 0, len(requested))
	for _, item := range requested {
		column, ok := explorerVisualizationColumnFor(source, item.Field)
		if !ok {
			// A stale display preference cannot widen the governed result. Keep
			// the complete result projection visible and let the caller report a
			// table-level validation warning if necessary.
			return append([]explorerVisualizationColumn(nil), source...), nil
		}
		selected = append(selected, column)
	}
	return selected, requested
}

func explorerVisualizationModelID(spec exploration.ExplorationSpec) string {
	if strings.TrimSpace(spec.ModelID) != "" {
		return strings.TrimSpace(spec.ModelID)
	}
	return "exploration"
}

func explorerVisualizationDatasetID(spec exploration.ExplorationSpec) string {
	if dataset := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID)); dataset != "" {
		return dataset
	}
	return "result"
}
