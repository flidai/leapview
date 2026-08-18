package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type tablePlan struct {
	Definition       visualizationdefinition.Definition
	Kind             string
	Title            string
	Table            string
	Rows             []visualizationdefinition.FieldBinding
	ColumnDims       []visualizationdefinition.FieldBinding
	Metrics          []visualizationdefinition.FieldBinding
	AggregateSort    []visualizationdefinition.Sort
	Offset           int64
	Limit            int64
	Totals           *visualizationdefinition.PivotTotals
	DataColumns      []visualizationdefinition.FieldBinding
	DefaultSort      dashboard.TableSort
	Columns          []dashboard.TableColumn
	MetricFormatting map[string][]dashboard.TableFormattingRule
	Style            dashboard.TableStyle
	Interaction      dashboard.InteractionConfig
}

func newTablePlan(definition visualizationdefinition.Definition) (tablePlan, error) {
	plan := tablePlan{Definition: definition, Title: dashboarddefinition.SpecTitle(definition.Spec), Columns: dashboarddefinition.TableColumns(definition.Spec), Style: dashboard.TableStyle{}.WithDefaults()}
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil {
		return tablePlan{}, err
	}
	if len(base.Interactions) > 0 {
		plan.Interaction = compiledInteractionConfig(base.Interactions[0])
	}
	switch definition.Query.Kind {
	case visualizationdefinition.QueryDetail:
		query := definition.Query.Detail
		plan.Kind, plan.Table, plan.DataColumns = "data_table", query.TableID, query.Fields
		if len(query.DefaultSort) > 0 {
			plan.DefaultSort = dashboard.TableSort{Key: query.DefaultSort[0].FieldID, Direction: query.DefaultSort[0].Direction}
		}
	case visualizationdefinition.QueryMatrix:
		query := definition.Query.Matrix
		plan.Kind, plan.Table, plan.Rows, plan.ColumnDims, plan.Metrics = "matrix_table", query.TableID, query.Rows, query.Columns, query.Metrics
		plan.Limit = query.Limit
		plan.MetricFormatting = dashboarddefinition.MetricFormatting(definition.Spec, query.Metrics)
	case visualizationdefinition.QueryPivot:
		query := definition.Query.Pivot
		plan.Kind, plan.Table, plan.Rows, plan.ColumnDims, plan.Metrics = "pivot_table", query.TableID, query.Rows, query.Columns, query.Metrics
		plan.AggregateSort, plan.Offset, plan.Totals, plan.Limit = query.Sort, query.Offset, query.Totals, query.Limit
		plan.MetricFormatting = dashboarddefinition.MetricFormatting(definition.Spec, query.Metrics)
	default:
		return tablePlan{}, fmt.Errorf("visualization %q query kind %q is not a grid query", definition.ID, definition.Query.Kind)
	}
	return plan, nil
}

func (s *VisualizationDataService) queryAggregateTable(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, request dashboard.TableRequest, definition visualizationdefinition.Definition, filters dashboard.Filters) (dashboard.Table, error) {
	table, err := newTablePlan(definition)
	if err != nil {
		return dashboard.EmptyTable(request, err), nil
	}
	var (
		columns               []dashboard.TableColumn
		rows                  []map[string]any
		calculationIncomplete bool
		queryErr              error
	)
	switch table.Kind {
	case "matrix_table":
		columns, rows, calculationIncomplete, queryErr = s.matrixTableRows(ctx, runtime, report, table, filters, request)
	case "pivot_table":
		columns, rows, calculationIncomplete, queryErr = s.pivotTableRows(ctx, runtime, report, table, filters, request)
	default:
		queryErr = fmt.Errorf("unsupported aggregate table kind %q", table.Kind)
	}
	if queryErr != nil {
		return dashboard.EmptyTable(request, queryErr), nil
	}
	totalRows := len(rows)
	isCapped := totalRows > dashboard.TableInteractiveRowCap || calculationIncomplete
	if isCapped {
		rows = rows[:min(len(rows), dashboard.TableInteractiveRowCap)]
	}
	chunkSize := max(dashboard.TableChunkSize, len(rows))
	style := table.Style.WithDefaults()
	cardinality := dashboard.ExactCardinality(totalRows)
	if calculationIncomplete {
		cardinality = dashboard.LowerBoundCardinality(totalRows)
	}
	return dashboard.Table{
		Version:       2,
		Kind:          table.Kind,
		Title:         table.Title,
		Style:         style,
		Interaction:   table.Interaction,
		Selection:     []dashboard.InteractionSelectionEntry{},
		Columns:       columns,
		Cardinality:   cardinality,
		AvailableRows: len(rows),
		IsCapped:      isCapped,
		RowCap:        dashboard.TableInteractiveRowCap,
		ChunkSize:     chunkSize,
		RowHeight:     style.RowHeight(),
		ResetVersion:  request.ResetVersion,
		Sort:          request.Sort,
		Blocks: map[string]dashboard.TableBlock{
			"a": {Start: 0, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion, Sort: request.Sort, Rows: rows},
			"b": {Start: chunkSize, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion, Sort: request.Sort, Rows: []map[string]any{}},
			"c": {Start: chunkSize * 2, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion, Sort: request.Sort, Rows: []map[string]any{}},
		},
		LoadingBlock: "",
		Error:        "",
	}, nil
}

func (s *VisualizationDataService) matrixTableRows(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, table tablePlan, filters dashboard.Filters, request dashboard.TableRequest) ([]dashboard.TableColumn, []map[string]any, bool, error) {
	if len(table.ColumnDims) == 1 {
		return s.crossTabTableRows(ctx, runtime, report, table, filters, request, false)
	}
	columns := make([]dashboard.TableColumn, 0, len(table.Rows)+len(table.Metrics))
	dimensions := make([]reportdef.QueryField, 0, len(table.Rows))
	metrics := make([]reportdef.QueryField, 0, len(table.Metrics))
	for _, dimensionBinding := range table.Rows {
		dimensionName := dimensionBinding.FieldID
		dimension, _ := runtime.model.ResolveDimension(dimensionName)
		key := dimensionBinding.Alias
		dimensions = append(dimensions, fieldRef(dimensionName, key))
		column := dashboard.TableColumn{Key: key, Label: dimensionLabel(key, dimension), Role: "row_header", Format: "text"}
		columns = append(columns, mergeTableColumn(column, tableColumnOverride(table, dimensionBinding.Alias)))
	}
	for _, metricBinding := range table.Metrics {
		metricName := metricBinding.FieldID
		metric := aggregateMemberMetadata(runtime.model, metricName)
		key := metricBinding.Alias
		metrics = append(metrics, fieldRef(metricName, key))
		column := dashboard.TableColumn{Key: key, Label: metricLabel(key, metric), Align: "right", Role: "metric", Metric: key, Format: tableMetricFormat(metric), DataType: string(metric.DataType), Formatting: tableMetricFormatting(table, metricName)}
		columns = append(columns, mergeTableColumn(column, tableColumnOverride(table, metricBinding.Alias)))
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", request.Table)
	if err != nil {
		return nil, nil, false, err
	}
	sorts := make([]reportdef.QuerySort, 0, len(dimensions))
	for _, dimension := range dimensions {
		sorts = append(sorts, reportdef.QuerySort{Field: dimension.Alias, Direction: "asc"})
	}
	if request.Sort.Key != "" && tableHasColumn(columns, request.Sort.Key) {
		sorts = []reportdef.QuerySort{{Field: request.Sort.Key, Direction: request.Sort.Direction}}
	}
	rows, err := runtime.data.Query(ctx, reportdef.AggregateQuery{
		Dataset:    table.Table,
		Dimensions: dimensions,
		Metrics:    metrics,
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      dashboard.TableInteractiveRowCap + 1,
	})
	if err != nil {
		return nil, nil, false, err
	}
	records := tableRowsFromAnalytics(rows)
	base, err := visualizationir.SpecificationBase(table.Definition.Spec)
	if err != nil {
		return nil, nil, false, err
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		completeness := boundedFrameCompleteness(len(records), dashboard.TableInteractiveRowCap+1)
		records, err = applyCalculationsToTableRecords(base, table.Definition.Query.DatasetID, records, completeness)
		if err != nil {
			return nil, nil, false, err
		}
		columns = append(columns, visibleCalculationTableColumns(base)...)
		sortAggregateTableRows(records, request.Sort)
	}
	return columns, records, len(records) >= dashboard.TableInteractiveRowCap+1, nil
}

func visibleCalculationTableColumns(base visualizationir.VisualizationSpecBase) []dashboard.TableColumn {
	if base.Calculations == nil {
		return nil
	}
	fields := map[string]visualizationir.VisualizationField{}
	for _, dataset := range base.Datasets {
		if dataset.ID != "primary" {
			continue
		}
		for _, field := range dataset.Fields {
			fields[field.ID] = field
		}
	}
	columns := []dashboard.TableColumn{}
	for _, calculation := range *base.Calculations {
		if calculation.Dataset != "primary" || calculation.Hidden {
			continue
		}
		field := fields[calculation.ID]
		columns = append(columns, dashboard.TableColumn{
			Key: calculation.ID, Label: field.Label, Align: "right", Role: "metric", Metric: calculation.ID,
			Format: dashboardCalculationFormat(field.Format),
		})
	}
	return columns
}

func dashboardCalculationFormat(format *visualizationir.VisualizationFormat) string {
	if format == nil {
		return "decimal"
	}
	kind, err := format.Kind()
	if err != nil {
		return "decimal"
	}
	switch kind {
	case "percent", "currency", "compact", "number":
		return kind
	default:
		return "decimal"
	}
}

func (s *VisualizationDataService) pivotTableRows(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, table tablePlan, filters dashboard.Filters, request dashboard.TableRequest) ([]dashboard.TableColumn, []map[string]any, bool, error) {
	return s.crossTabTableRows(ctx, runtime, report, table, filters, request, true)
}

func (s *VisualizationDataService) crossTabTableRows(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, table tablePlan, filters dashboard.Filters, request dashboard.TableRequest, pivotMode bool) ([]dashboard.TableColumn, []map[string]any, bool, error) {
	if len(table.ColumnDims) == 0 {
		return nil, nil, false, fmt.Errorf("table %q has no pivot column dimensions", table.Definition.ID)
	}
	base, err := visualizationir.SpecificationBase(table.Definition.Spec)
	if err != nil {
		return nil, nil, false, err
	}
	rowDimensions := make([]reportdef.QueryField, 0, len(table.Rows))
	dimensions := make([]reportdef.QueryField, 0, len(table.Rows)+len(table.ColumnDims))
	baseColumns := make([]dashboard.TableColumn, 0, len(table.Rows))
	for _, dimensionBinding := range table.Rows {
		dimensionName := dimensionBinding.FieldID
		dimension, _ := runtime.model.ResolveDimension(dimensionName)
		key := dimensionBinding.Alias
		field := fieldRef(dimensionName, key)
		rowDimensions = append(rowDimensions, field)
		dimensions = append(dimensions, field)
		column := dashboard.TableColumn{Key: key, Label: dimensionLabel(key, dimension), Role: "row_header", Format: "text"}
		baseColumns = append(baseColumns, mergeTableColumn(column, tableColumnOverride(table, dimensionBinding.Alias)))
	}
	for _, columnDimension := range table.ColumnDims {
		dimensions = append(dimensions, fieldRef(columnDimension.FieldID, columnDimension.Alias))
	}
	metrics := make([]reportdef.QueryField, 0, len(table.Metrics))
	for _, metricBinding := range table.Metrics {
		metricName := metricBinding.FieldID
		key := metricBinding.Alias
		metrics = append(metrics, fieldRef(metricName, key))
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", request.Table)
	if err != nil {
		return nil, nil, false, err
	}
	sorts := make([]reportdef.QuerySort, 0, len(table.AggregateSort)+len(dimensions))
	for _, value := range table.AggregateSort {
		sorts = append(sorts, reportdef.QuerySort{Field: value.FieldID, Direction: value.Direction})
	}
	if len(sorts) == 0 {
		for _, dimension := range dimensions {
			sorts = append(sorts, reportdef.QuerySort{Field: dimension.Alias, Direction: "asc"})
		}
	}
	// Pivot windows are defined over grouped row identities, not aggregate
	// cells. First fetch only the governed row axis, then constrain the cell
	// query to those identities. This prevents a wide pivot from consuming the
	// interactive cap before the requested row window is applied.
	rowLimit := table.Limit
	if rowLimit <= 0 {
		rowLimit = dashboard.TableInteractiveRowCap
	}
	rowFetchLimit := rowLimit + 1
	if rowFetchLimit <= 0 || rowFetchLimit > int64(^uint(0)>>1) {
		return nil, nil, false, fmt.Errorf("table %q pivot window is too large", table.Definition.ID)
	}
	rowSorts := sortsForPivotDimensions(sorts, rowDimensions, metrics)
	rawAxisRows, err := runtime.data.Query(ctx, reportdef.AggregateQuery{
		Dataset:    table.Table,
		Dimensions: rowDimensions,
		Metrics:    metrics,
		Filters:    queryFilters,
		Sort:       rowSorts,
		Offset:     int(table.Offset),
		Limit:      int(rowFetchLimit),
	})
	if err != nil {
		return nil, nil, false, err
	}
	axisRows := dedupePivotAxisRows(tableRowsFromAnalytics(rawAxisRows), table.Rows)
	calculationIncomplete := len(axisRows) >= int(rowFetchLimit)
	selectedAxisRows := axisRows
	if len(selectedAxisRows) > int(rowLimit) {
		selectedAxisRows = selectedAxisRows[:int(rowLimit)]
	}
	selectedIdentities := make(map[string]struct{}, len(selectedAxisRows))
	for _, axisRow := range selectedAxisRows {
		selectedIdentities[pivotRowIdentity(axisRow, table.Rows)] = struct{}{}
	}
	cellFilters := append([]reportdef.QueryFilter(nil), queryFilters...)
	if len(selectedAxisRows) > 0 && pivotRowsCanUseTypedFilters(table.Rows) {
		groups := make([]reportdef.QueryFilterGroup, 0, len(selectedAxisRows))
		for _, axisRow := range selectedAxisRows {
			group := reportdef.QueryFilterGroup{}
			for _, dimension := range table.Rows {
				value := axisRow[dimension.Alias]
				if value == nil {
					group.Filters = append(group.Filters, reportdef.QueryFilter{Field: dimension.FieldID, Operator: "is_null"})
				} else {
					group.Filters = append(group.Filters, reportdef.QueryFilter{Field: dimension.FieldID, Operator: "equals", Values: []any{value}})
				}
			}
			groups = append(groups, group)
		}
		if len(groups) == 1 {
			cellFilters = append(cellFilters, groups[0].Filters...)
		} else {
			cellFilters = append(cellFilters, reportdef.QueryFilter{Groups: groups})
		}
	}
	normalizedRows := []map[string]any(nil)
	if len(selectedIdentities) > 0 {
		cellLimit := int(base.DataBudget.MaxRows) + 1
		if cellLimit <= 1 {
			return nil, nil, false, fmt.Errorf("table %q has invalid data budget maxRows %d", table.Definition.ID, base.DataBudget.MaxRows)
		}
		rawRows, queryErr := runtime.data.Query(ctx, reportdef.AggregateQuery{
			Dataset: table.Table, Dimensions: dimensions, Metrics: metrics,
			Filters: cellFilters, Sort: sorts, Offset: 0, Limit: cellLimit,
		})
		if queryErr != nil {
			return nil, nil, false, queryErr
		}
		normalizedRows = tableRowsFromAnalytics(rawRows)
		cellIncomplete := len(normalizedRows) >= cellLimit
		if cellIncomplete && base.DataBudget.RequiredCompleteness == visualizationir.VisualizationCompletenessComplete {
			return nil, nil, false, fmt.Errorf("table %q pivot cells exceed data budget maxRows %d", table.Definition.ID, base.DataBudget.MaxRows)
		}
		if cellIncomplete {
			calculationIncomplete = true
			normalizedRows = normalizedRows[:min(len(normalizedRows), int(base.DataBudget.MaxRows))]
		}
		filtered := normalizedRows[:0]
		for _, raw := range normalizedRows {
			if _, ok := selectedIdentities[pivotRowIdentity(raw, table.Rows)]; ok {
				filtered = append(filtered, raw)
			}
		}
		normalizedRows = filtered
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		completeness := boundedFrameCompleteness(len(axisRows), int(rowFetchLimit))
		normalizedRows, err = applyCalculationsToTableRecords(base, table.Definition.Query.DatasetID, normalizedRows, completeness)
		if err != nil {
			return nil, nil, false, err
		}
	}
	valueFields := crossTabValueFields(table, base, runtime.model)
	if table.Totals != nil && (table.Totals.Columns || table.Totals.Grand) && len(valueFields) > len(table.Metrics) {
		return nil, nil, false, fmt.Errorf("table %q exact column/grand totals do not support calculated metrics", table.Definition.ID)
	}
	var columnTotalRows, grandTotalRows []map[string]any
	if table.Totals != nil && (table.Totals.Columns || table.Totals.Grand) {
		totalDimensions := []reportdef.QueryField(nil)
		if table.Totals.Columns {
			totalDimensions = append(totalDimensions, dimensions[len(table.Rows):]...)
		}
		totalSorts := sortsForPivotDimensions(sorts, totalDimensions, metrics)
		totalLimit := int(base.DataBudget.MaxRows) + 1
		totalRows, totalErr := runtime.data.Query(ctx, reportdef.AggregateQuery{
			Dataset: table.Table, Dimensions: totalDimensions, Metrics: metrics,
			Filters: queryFilters, Sort: totalSorts, Limit: totalLimit,
		})
		if totalErr != nil {
			return nil, nil, false, totalErr
		}
		if len(totalRows) >= totalLimit {
			return nil, nil, false, fmt.Errorf("table %q pivot totals exceed data budget maxRows %d", table.Definition.ID, base.DataBudget.MaxRows)
		}
		if table.Totals.Columns {
			columnTotalRows = tableRowsFromAnalytics(totalRows)
		} else {
			grandTotalRows = tableRowsFromAnalytics(totalRows)
		}
		if table.Totals.Columns && table.Totals.Grand {
			grandRows, grandErr := runtime.data.Query(ctx, reportdef.AggregateQuery{
				Dataset: table.Table, Dimensions: nil, Metrics: metrics,
				Filters: queryFilters, Limit: 2,
			})
			if grandErr != nil {
				return nil, nil, false, grandErr
			}
			grandTotalRows = tableRowsFromAnalytics(grandRows)
		}
	}
	columns := append([]dashboard.TableColumn{}, baseColumns...)
	pivotKeys := map[string]string{}
	usedKeys := map[string]string{}
	columnKeys := map[string]string{}
	columnIdentityByKey := map[string]string{}
	for _, column := range baseColumns {
		usedKeys[column.Key] = column.Key
	}
	ensurePivotValueColumn := func(raw map[string]any, valueField crossTabValueField) string {
		columnValues := make([]any, 0, len(table.ColumnDims))
		columnLabels := make([]string, 0, len(table.ColumnDims))
		for _, columnDimension := range table.ColumnDims {
			value := raw[columnDimension.Alias]
			columnValues = append(columnValues, value)
			columnLabels = append(columnLabels, fmt.Sprint(value))
		}
		label := strings.Join(columnLabels, " / ")
		pivotIdentity := typedTupleIdentity(columnValues)
		pivotKey, exists := pivotKeys[pivotIdentity]
		if !exists {
			pivotKey = sanitizeTableKey(label)
			pivotKeys[pivotIdentity] = pivotKey
		}
		metricKey := valueField.key
		columnIdentity := typedTupleIdentity(columnValues) + "\x00" + metricKey
		if columnKey, ok := columnKeys[columnIdentity]; ok {
			return columnKey
		}
		candidate := "pivot_" + pivotKey
		columnLabel := label
		groupLabel := label
		if pivotMode {
			metric := aggregateMemberMetadata(runtime.model, valueField.key)
			groupLabel = metricLabel(valueField.key, metric)
		}
		if !pivotMode || len(valueFields) > 1 {
			candidate += "__" + sanitizeTableKey(metricKey)
			columnLabel = valueField.label
		}
		columnKey := uniqueTableColumnKey(candidate, usedKeys)
		columnKeys[columnIdentity] = columnKey
		columnIdentityByKey[columnKey] = typedTupleIdentity(columnValues)
		usedKeys[columnKey] = columnKey
		column := dashboard.TableColumn{Key: columnKey, Label: columnLabel, Align: "right", Role: "metric", Group: groupLabel, Metric: metricKey, ColumnValue: label, Format: valueField.format, DataType: string(valueField.dataType), Formatting: valueField.formatting}
		columns = append(columns, mergeTableColumn(column, tableColumnOverride(table, metricKey)))
		return columnKey
	}
	resultByKey := map[string]map[string]any{}
	order := []string{}
	for _, raw := range normalizedRows {
		rowKeyParts := make([]any, 0, len(table.Rows))
		for _, dimension := range table.Rows {
			rowKeyParts = append(rowKeyParts, raw[dimension.Alias])
		}
		resultKey := typedTupleIdentity(rowKeyParts)
		row, exists := resultByKey[resultKey]
		if !exists {
			row = map[string]any{}
			for _, dimension := range table.Rows {
				key := dimension.Alias
				row[key] = raw[key]
			}
			resultByKey[resultKey] = row
			order = append(order, resultKey)
		}
		for _, valueField := range valueFields {
			metricKey := valueField.key
			columnKey := ensurePivotValueColumn(raw, valueField)
			row[columnKey] = raw[metricKey]
		}
	}
	for _, raw := range columnTotalRows {
		for _, valueField := range valueFields {
			ensurePivotValueColumn(raw, valueField)
		}
	}
	result := make([]map[string]any, 0, len(order))
	for _, key := range order {
		result = append(result, resultByKey[key])
	}
	sortAggregateTableRows(result, request.Sort)
	if table.Totals != nil && (table.Totals.Columns || table.Totals.Grand) {
		columns, result = addPivotTotalsExact(columns, result, table, valueFields, columnTotalRows, grandTotalRows, columnIdentityByKey)
	} else if table.Totals != nil && table.Totals.Rows {
		columns, result = addPivotTotals(columns, result, table, valueFields)
	}
	return columns, result, calculationIncomplete, nil
}

func typedTupleIdentity(values []any) string {
	// Include the concrete scalar type as well as a length-prefixed JSON value;
	// otherwise numerically equal int/float values or display labels can alias
	// distinct pivot identities.
	var result strings.Builder
	for _, value := range values {
		if value == nil {
			result.WriteString("<nil>;0:")
			continue
		}
		encoded, _ := json.Marshal(value)
		result.WriteString(fmt.Sprintf("%T;%d:", value, len(encoded)))
		result.Write(encoded)
	}
	return result.String()
}

func pivotRowIdentity(row map[string]any, dimensions []visualizationdefinition.FieldBinding) string {
	values := make([]any, 0, len(dimensions))
	for _, dimension := range dimensions {
		values = append(values, row[dimension.Alias])
	}
	return typedTupleIdentity(values)
}

func pivotRowsCanUseTypedFilters(dimensions []visualizationdefinition.FieldBinding) bool {
	for _, dimension := range dimensions {
		// QueryFilter has no grain/conformed-dimension operand. Avoid applying
		// a display-grain value to the raw field; the bounded cell query below
		// then fails closed if the complete relation exceeds the data budget.
		if dimension.Grain != "" {
			return false
		}
	}
	return true
}

func dedupePivotAxisRows(rows []map[string]any, dimensions []visualizationdefinition.FieldBinding) []map[string]any {
	seen := make(map[string]struct{}, len(rows))
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		identity := pivotRowIdentity(row, dimensions)
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, row)
	}
	return result
}

func sortsForPivotDimensions(sorts []reportdef.QuerySort, dimensions []reportdef.QueryField, metrics []reportdef.QueryField) []reportdef.QuerySort {
	allowed := make(map[string]struct{}, len(dimensions)+len(metrics))
	for _, field := range dimensions {
		allowed[field.Alias] = struct{}{}
	}
	for _, field := range metrics {
		allowed[field.Alias] = struct{}{}
	}
	result := make([]reportdef.QuerySort, 0, len(sorts)+len(dimensions))
	seen := make(map[string]struct{}, len(sorts)+len(dimensions))
	for _, sort := range sorts {
		if _, ok := allowed[sort.Field]; !ok {
			continue
		}
		result = append(result, sort)
		seen[sort.Field] = struct{}{}
	}
	for _, field := range dimensions {
		if _, ok := seen[field.Alias]; ok {
			continue
		}
		result = append(result, reportdef.QuerySort{Field: field.Alias, Direction: "asc"})
	}
	return result
}

func applyPivotWindow(rows []map[string]any, offset, limit int64) []map[string]any {
	if offset < 0 {
		return nil
	}
	start := int(offset)
	if start >= len(rows) {
		return []map[string]any{}
	}
	end := len(rows)
	if limit > 0 && int64(start)+limit < int64(end) {
		end = start + int(limit)
	}
	return rows[start:end]
}

// addPivotTotals materializes the explicit pivot totals contract from the
// already governed result frame. It never issues an unbounded second query:
// row totals sum the bounded column cells, column totals append one bounded
// total row, and grand totals combine both policies deterministically.
func addPivotTotals(columns []dashboard.TableColumn, rows []map[string]any, table tablePlan, values []crossTabValueField) ([]dashboard.TableColumn, []map[string]any) {
	if len(rows) == 0 {
		return columns, rows
	}
	metricColumns := make(map[string][]dashboard.TableColumn)
	baseRows := append([]map[string]any(nil), rows...)
	for _, column := range columns {
		if column.Role == "metric" && column.ColumnValue != "" {
			metricColumns[column.Metric] = append(metricColumns[column.Metric], column)
		}
	}
	if table.Totals.Rows {
		for _, value := range values {
			keys := metricColumns[value.key]
			if len(keys) == 0 {
				continue
			}
			key := "pivot_total"
			label := "Total"
			if len(values) > 1 {
				key += "__" + sanitizeTableKey(value.key)
				label = "Total " + value.label
			}
			columns = append(columns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Total", Metric: value.key, ColumnValue: "Total", Format: value.format, DataType: string(value.dataType), Formatting: value.formatting})
			for _, row := range rows {
				total := any(nil)
				for _, column := range keys {
					total = addTableValues(total, row[column.Key])
				}
				row[key] = total
			}
		}
	}
	var totalRow map[string]any
	if table.Totals.Columns {
		totalRow = map[string]any{}
		for _, dimension := range table.Rows {
			totalRow[dimension.Alias] = "Total"
		}
		for _, column := range columns {
			if column.Role != "metric" || column.ColumnValue == "" {
				continue
			}
			var total any
			for _, row := range rows {
				total = addTableValues(total, row[column.Key])
			}
			totalRow[column.Key] = total
		}
		rows = append(rows, totalRow)
	}
	if table.Totals.Grand {
		// Grand is its own total cell. It is not an alias for column totals:
		// with no Columns policy it still produces one explicit total row.
		if totalRow == nil {
			totalRow = map[string]any{}
			for _, dimension := range table.Rows {
				totalRow[dimension.Alias] = "Total"
			}
		}
		for _, value := range values {
			key := "pivot_grand"
			label := "Grand total"
			if len(values) > 1 {
				key += "__" + sanitizeTableKey(value.key)
				label += " " + value.label
			}
			columns = append(columns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Grand total", Metric: value.key, ColumnValue: "Grand total", Format: value.format, DataType: string(value.dataType), Formatting: value.formatting})
			var grand any
			for _, column := range metricColumns[value.key] {
				for _, row := range baseRows {
					grand = addTableValues(grand, row[column.Key])
				}
			}
			totalRow[key] = grand
		}
		if table.Totals.Columns {
			// Replace the previously appended column-total row with the same
			// row enriched by grand-total cells.
			rows[len(rows)-1] = totalRow
		} else {
			rows = append(rows, totalRow)
		}
	}
	return columns, rows
}

// addPivotTotalsExact combines row-window cells with independently governed
// column/grand aggregates. Row totals are scoped to the selected row window;
// column and grand totals are scoped to the complete filtered relation.
func addPivotTotalsExact(columns []dashboard.TableColumn, rows []map[string]any, table tablePlan, values []crossTabValueField, columnTotalRows, grandTotalRows []map[string]any, columnIdentityByKey map[string]string) ([]dashboard.TableColumn, []map[string]any) {
	metricColumns := make(map[string][]dashboard.TableColumn)
	for _, column := range columns {
		if column.Role == "metric" && column.ColumnValue != "" {
			metricColumns[column.Metric] = append(metricColumns[column.Metric], column)
		}
	}
	if table.Totals.Rows {
		for _, value := range values {
			keys := metricColumns[value.key]
			if len(keys) == 0 {
				continue
			}
			key := "pivot_total"
			label := "Total"
			if len(values) > 1 {
				key += "__" + sanitizeTableKey(value.key)
				label = "Total " + value.label
			}
			columns = append(columns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Total", Metric: value.key, ColumnValue: "Total", Format: value.format, DataType: string(value.dataType), Formatting: value.formatting})
			for _, row := range rows {
				var total any
				for _, column := range keys {
					total = addTableValues(total, row[column.Key])
				}
				row[key] = total
			}
			metricColumns[value.key] = append(metricColumns[value.key], columns[len(columns)-1])
		}
	}
	if !table.Totals.Columns && !table.Totals.Grand {
		return columns, rows
	}
	columnTotals := make(map[string]map[string]any)
	for _, raw := range columnTotalRows {
		columnValues := make([]any, 0, len(table.ColumnDims))
		for _, dimension := range table.ColumnDims {
			columnValues = append(columnValues, raw[dimension.Alias])
		}
		columnTotals[typedTupleIdentity(columnValues)] = raw
	}
	grand := map[string]any{}
	if len(grandTotalRows) > 0 {
		grand = grandTotalRows[0]
	}
	totalRow := map[string]any{}
	for _, dimension := range table.Rows {
		totalRow[dimension.Alias] = "Total"
	}
	if table.Totals.Columns {
		for _, column := range columns {
			if column.Role != "metric" || column.ColumnValue == "" {
				continue
			}
			if raw, ok := columnTotals[columnIdentityByKey[column.Key]]; ok {
				totalRow[column.Key] = raw[column.Metric]
			}
		}
	}
	if table.Totals.Grand {
		for _, value := range values {
			key := "pivot_grand"
			label := "Grand total"
			if len(values) > 1 {
				key += "__" + sanitizeTableKey(value.key)
				label += " " + value.label
			}
			columns = append(columns, dashboard.TableColumn{Key: key, Label: label, Align: "right", Role: "metric", Group: "Grand total", Metric: value.key, ColumnValue: "Grand total", Format: value.format, DataType: string(value.dataType), Formatting: value.formatting})
			totalRow[key] = grand[value.key]
		}
	}
	return columns, append(rows, totalRow)
}

func addTableValues(left, right any) any {
	if right == nil {
		return left
	}
	if left == nil {
		return right
	}
	lf, lok := tableNumericValue(left)
	rf, rok := tableNumericValue(right)
	if !lok || !rok {
		return left
	}
	return lf + rf
}

func tableNumericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

type crossTabValueField struct {
	key        string
	label      string
	format     string
	formatting []dashboard.TableFormattingRule
	dataType   visualizationir.VisualizationDataType
}

func crossTabValueFields(table tablePlan, base visualizationir.VisualizationSpecBase, model *semanticmodel.Model) []crossTabValueField {
	fields := make([]crossTabValueField, 0, len(table.Metrics))
	for _, binding := range table.Metrics {
		metric := aggregateMemberMetadata(model, binding.FieldID)
		fields = append(fields, crossTabValueField{
			key: binding.Alias, label: metricLabel(binding.Alias, metric),
			format: tableMetricFormat(metric), dataType: metric.DataType, formatting: tableMetricFormatting(table, binding.FieldID),
		})
	}
	schemaFields := map[string]visualizationir.VisualizationField{}
	for _, dataset := range base.Datasets {
		if dataset.ID != table.Definition.Query.DatasetID {
			continue
		}
		for _, field := range dataset.Fields {
			schemaFields[field.ID] = field
		}
	}
	if base.Calculations != nil {
		for _, calculation := range *base.Calculations {
			if calculation.Dataset != table.Definition.Query.DatasetID || calculation.Hidden {
				continue
			}
			field := schemaFields[calculation.ID]
			label := field.Label
			if label == "" {
				label = calculation.Label
			}
			fields = append(fields, crossTabValueField{
				key: calculation.ID, label: label,
				format: dashboardCalculationFormat(field.Format), dataType: field.DataType,
			})
		}
	}
	return fields
}

func tableRowsFromAnalytics(rows reportdef.QueryRows) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		normalized := map[string]any{}
		for column, value := range row {
			normalized[column] = normalizeDBValue(value)
		}
		result = append(result, normalized)
	}
	return result
}

func tableColumnKeys(columns []dashboard.TableColumn) []string {
	keys := make([]string, len(columns))
	for i, column := range columns {
		keys[i] = column.Key
	}
	return keys
}

func tableHasColumn(columns []dashboard.TableColumn, key string) bool {
	for _, column := range columns {
		if column.Key == key {
			return true
		}
	}
	return false
}

func sortAggregateTableRows(rows []map[string]any, tableSort dashboard.TableSort) {
	if tableSort.Key == "" {
		return
	}
	direction := tableSort.Direction
	sort.SliceStable(rows, func(i, j int) bool {
		cmp := compareTableValues(rows[i][tableSort.Key], rows[j][tableSort.Key])
		if direction == "desc" {
			return cmp > 0
		}
		return cmp < 0
	})
}

func compareTableValues(a, b any) int {
	aFloat, aNumeric := numericTableValue(a)
	bFloat, bNumeric := numericTableValue(b)
	if aNumeric && bNumeric {
		switch {
		case aFloat < bFloat:
			return -1
		case aFloat > bFloat:
			return 1
		default:
			return 0
		}
	}
	aText := strings.ToLower(fmt.Sprint(a))
	bText := strings.ToLower(fmt.Sprint(b))
	switch {
	case aText < bText:
		return -1
	case aText > bText:
		return 1
	default:
		return 0
	}
}

func numericTableValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func dimensionLabel(name string, dimension semanticmodel.MetricDimension) string {
	if strings.TrimSpace(dimension.Label) != "" {
		return dimension.Label
	}
	return name
}

func tableMetricFormat(metric metricMetadata) string {
	switch metric.Format {
	case "integer", "decimal", "currency":
		return metric.Format
	default:
		return "decimal"
	}
}

func tableMetricFormatting(table tablePlan, metric string) []dashboard.TableFormattingRule {
	if len(table.MetricFormatting[metric]) == 0 {
		return nil
	}
	return append([]dashboard.TableFormattingRule{}, table.MetricFormatting[metric]...)
}

func tableColumnOverride(table tablePlan, key string) dashboard.TableColumn {
	for _, column := range table.Columns {
		if column.Key == key {
			return column
		}
	}
	return dashboard.TableColumn{}
}

func mergeTableColumn(column, override dashboard.TableColumn) dashboard.TableColumn {
	if override.Label != "" {
		column.Label = override.Label
	}
	if override.Align != "" {
		column.Align = override.Align
	}
	if override.Group != "" {
		column.Group = override.Group
	}
	if override.Width > 0 {
		column.Width = override.Width
	}
	if override.Format != "" {
		column.Format = override.Format
	}
	if len(override.Formatting) > 0 {
		column.Formatting = append([]dashboard.TableFormattingRule{}, override.Formatting...)
	}
	return column
}

func sanitizeTableKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	key := strings.Trim(builder.String(), "_")
	if key == "" {
		return "value"
	}
	return key
}

func uniqueTableColumnKey(candidate string, existing map[string]string) string {
	used := map[string]struct{}{}
	for _, key := range existing {
		used[key] = struct{}{}
	}
	key := candidate
	for i := 2; ; i++ {
		if _, ok := used[key]; !ok {
			return key
		}
		key = fmt.Sprintf("%s_%d", candidate, i)
	}
}

func (s *VisualizationDataService) tableRowRequest(ctx context.Context, runtime *modelRuntime, report *dashboarddefinition.Definition, table tablePlan, filters dashboard.Filters, request dashboard.TableRequest, start, count int) (reportdef.RowQuery, error) {
	dimensions := []reportdef.QueryField{}
	metrics := []reportdef.QueryField{}
	for _, column := range table.DataColumns {
		if _, err := runtime.model.ResolveDimension(column.FieldID); err == nil {
			dimensions = append(dimensions, fieldRef(column.FieldID, column.Alias))
			continue
		}
		metrics = append(metrics, fieldRef(column.FieldID, column.Alias))
	}
	queryFilters, err := s.filters.semanticFilters(ctx, runtime, report, filters, "visual", request.Table)
	if err != nil {
		return reportdef.RowQuery{}, err
	}
	sortKey := tableSortKey(table, request.Sort.Key)
	direction := request.Sort.Direction
	if direction == "" {
		direction = "desc"
	}
	sorts := []reportdef.QuerySort{}
	if sortKey != "" {
		sorts = append(sorts, reportdef.QuerySort{Field: sortKey, Direction: direction})
	}
	if sortKey != "order_id" && tableHasQueryAlias(table.DataColumns, "order_id") {
		sorts = append(sorts, reportdef.QuerySort{Field: "order_id", Direction: "asc"})
	}
	rowQuery := reportdef.RowQuery{
		Dataset:    table.Table,
		Dimensions: dimensions,
		Metrics:    metrics,
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      count,
		Offset:     start,
	}
	return rowQuery, nil
}

func tableSortKey(table tablePlan, key string) string {
	if key == "" {
		key = table.DefaultSort.Key
	}
	if tableHasQueryAlias(table.DataColumns, key) {
		return key
	}
	if tableHasQueryAlias(table.DataColumns, table.DefaultSort.Key) {
		return table.DefaultSort.Key
	}
	if tableHasQueryAlias(table.DataColumns, "order_id") {
		return "order_id"
	}
	if len(table.DataColumns) > 0 {
		return table.DataColumns[0].Alias
	}
	return ""
}

func tableHasQueryAlias(columns []visualizationdefinition.FieldBinding, alias string) bool {
	for _, column := range columns {
		if column.Alias == alias {
			return true
		}
	}
	return false
}
