package http

import (
	"fmt"
	"reflect"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerTableEnvelope(spec exploration.ExplorationSpec, base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, modelID, datasetID string, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn) (visualizationir.VisualizationEnvelope, error) {
	base.Kind = "table"
	selectedColumns := sourceColumns
	if spec.Table != nil && spec.Table.Columns != nil && len(*spec.Table.Columns) > 0 {
		selectedColumns = make([]explorerVisualizationColumn, 0, len(*spec.Table.Columns))
		for _, authored := range *spec.Table.Columns {
			column, ok := explorerColumnFor(sourceColumns, authored.Field)
			if !ok {
				return visualizationir.VisualizationEnvelope{}, fmt.Errorf("table field %q is not present in the governed result", authored.Field)
			}
			if authored.Label != nil && strings.TrimSpace(*authored.Label) != "" {
				column.Label = strings.TrimSpace(*authored.Label)
			}
			selectedColumns = append(selectedColumns, column)
		}
	}
	columns := make([]visualizationir.TableVisualizationColumn, 0, len(selectedColumns))
	queryFields := make([]visualizationdefinition.FieldBinding, 0, len(selectedColumns))
	for index := range selectedColumns {
		source := selectedColumns[index]
		column := visualizationir.TableVisualizationColumn{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: source.Output}, Label: source.Label, Formatting: []visualizationir.TableVisualizationFormattingRule{}}
		if spec.Table != nil && spec.Table.Columns != nil && index < len(*spec.Table.Columns) {
			authored := (*spec.Table.Columns)[index]
			if authored.Width != nil {
				width := int64(*authored.Width)
				column.Width = &width
			}
			selectedColumns[index].Format = explorerVisualizationFormat(authored.Format)
		}
		columns = append(columns, column)
		queryFields = append(queryFields, visualizationdefinition.FieldBinding{FieldID: source.Semantic, Alias: source.Output, Grain: source.Grain})
	}
	rowHeight, striped, showHeader := int64(32), false, true
	if spec.Table != nil {
		if spec.Table.RowHeight != nil {
			rowHeight = int64(*spec.Table.RowHeight)
		} else if spec.Table.Density != nil {
			switch *spec.Table.Density {
			case exploration.ExplorationTableDensityCompact:
				rowHeight = 28
			case exploration.ExplorationTableDensityComfortable:
				rowHeight = 36
			}
		}
		if spec.Table.Striped != nil {
			striped = *spec.Table.Striped
		}
		if spec.Table.ShowHeader != nil {
			showHeader = *spec.Table.ShowHeader
		}
	}
	base.Datasets = []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: explorerIRFields(selectedColumns)}}
	base.Interactions = explorerVisualizationInteractions(selectedColumns)
	visualSpec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: columns, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: rowHeight, Striped: striped, ShowHeader: showHeader}}}
	query := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: modelID, DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: datasetID, Fields: queryFields, Limit: base.DataBudget.MaxRows}}
	definition, err := visualizationdefinition.New("table", visualSpec, query)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	tableSort := explorerDashboardTableSort(spec.Sort, selectedColumns)
	table := explorerDashboardTable(base, frame, result, selectedColumns, rowHeight, tableSort)
	return visualizationruntime.WindowEnvelopeFromDefinition(definition, table, explorerDataRevision(result), explorerGeneration(result))
}

func explorerDashboardTableSort(sorts []exploration.ExplorationSort, columns []explorerVisualizationColumn) dashboard.TableSort {
	if len(columns) == 0 {
		return dashboard.TableSort{}
	}
	result := dashboard.TableSort{Key: columns[0].Output, Direction: "asc"}
	if len(sorts) == 0 {
		return result
	}
	column, ok := explorerColumnFor(columns, sorts[0].Field)
	if !ok {
		return result
	}
	result.Key = column.Output
	if sorts[0].Direction == exploration.ExplorationSortDirectionDesc {
		result.Direction = "desc"
	}
	return result
}

func explorerDashboardTable(base visualizationir.VisualizationSpecBase, frame visualizationruntime.Frame, result projectsignals.DataExploreResultSignal, sourceColumns []explorerVisualizationColumn, rowHeight int64, tableSort dashboard.TableSort) dashboard.Table {
	columns := make([]dashboard.TableColumn, 0, len(sourceColumns))
	for _, source := range sourceColumns {
		role := "dimension"
		if source.Role == visualizationir.VisualizationFieldRoleMetric {
			role = "metric"
		}
		columns = append(columns, dashboard.TableColumn{Key: source.Output, Label: source.Label, Role: role, DataType: string(source.DataType)})
	}
	rows := make([]map[string]any, len(result.Rows))
	for rowIndex, source := range result.Rows {
		row := make(map[string]any, len(columns))
		for _, column := range columns {
			row[column.Key] = source[column.Key]
		}
		rows[rowIndex] = row
	}
	cardinality := dashboard.ExactCardinality(len(rows))
	if result.Truncated {
		cardinality = dashboard.LowerBoundCardinality(len(rows))
	}
	block := dashboard.TableBlock{Start: 0, RequestSeq: int(result.RequestSeq), Rows: rows, Sort: tableSort}
	return dashboard.Table{Kind: "table", Title: base.Title, Columns: columns, Cardinality: cardinality, AvailableRows: len(rows), IsCapped: result.Truncated, RowCap: int(base.DataBudget.MaxRows), ChunkSize: explorerMaterializedChunkSize(len(rows)), RowHeight: int(rowHeight), Blocks: map[string]dashboard.TableBlock{"a": block}, Sort: block.Sort, ResetVersion: 0}
}

func explorerMaterializedChunkSize(rowCount int) int {
	if rowCount < 1 {
		return 1
	}
	return rowCount
}

func explorerVisualizationFormat(format *exploration.ExplorationVisualizationFormat) *visualizationir.VisualizationFormat {
	value, _ := explorerVisualizationFormatChecked(format)
	return value
}

func explorerVisualizationFormatChecked(format *exploration.ExplorationVisualizationFormat) (*visualizationir.VisualizationFormat, error) {
	if format == nil || format.Value == nil {
		if format == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("format variant is required")
	}
	variantValue := reflect.ValueOf(format.Value)
	if variantValue.Kind() == reflect.Ptr && variantValue.IsNil() {
		return nil, fmt.Errorf("format variant is nil")
	}
	var value visualizationir.VisualizationFormatVariant
	switch variant := format.Value.(type) {
	case *exploration.ExplorationNumberVisualizationFormat:
		value = &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.ExplorationCurrencyVisualizationFormat:
		value = &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: variant.Currency, MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.ExplorationPercentVisualizationFormat:
		value = &visualizationir.PercentVisualizationFormat{Kind: "percent", MinimumFractionDigits: variant.MinimumFractionDigits, MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.ExplorationCompactVisualizationFormat:
		value = &visualizationir.CompactVisualizationFormat{Kind: "compact", MaximumFractionDigits: variant.MaximumFractionDigits}
	case *exploration.ExplorationDurationVisualizationFormat:
		value = &visualizationir.DurationVisualizationFormat{Kind: "duration", Unit: variant.Unit}
	case *exploration.ExplorationTemporalVisualizationFormat:
		value = &visualizationir.TemporalVisualizationFormat{Kind: "temporal", DateStyle: variant.DateStyle, TimeStyle: variant.TimeStyle}
	default:
		return nil, fmt.Errorf("unsupported format variant %T", variant)
	}
	return &visualizationir.VisualizationFormat{Value: value}, nil
}

func explorerDataRevision(result projectsignals.DataExploreResultSignal) int64 {
	if result.RequestSeq > 0 {
		return result.RequestSeq
	}
	return 1
}

func explorerGeneration(result projectsignals.DataExploreResultSignal) int64 {
	if result.RequestSeq > 0 {
		return result.RequestSeq
	}
	return 0
}
