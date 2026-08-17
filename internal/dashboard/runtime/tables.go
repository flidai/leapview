package runtime

import (
	"context"
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
		plan.MetricFormatting = dashboarddefinition.MetricFormatting(definition.Spec, query.Metrics)
	case visualizationdefinition.QueryPivot:
		query := definition.Query.Pivot
		plan.Kind, plan.Table, plan.Rows, plan.ColumnDims, plan.Metrics = "pivot_table", query.TableID, query.Rows, query.Columns, query.Metrics
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
		column := dashboard.TableColumn{Key: key, Label: metricLabel(key, metric), Align: "right", Role: "metric", Metric: key, Format: tableMetricFormat(metric), Formatting: tableMetricFormatting(table, metricName)}
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
		Table:      table.Table,
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
	dimensions := make([]reportdef.QueryField, 0, len(table.Rows)+1)
	baseColumns := make([]dashboard.TableColumn, 0, len(table.Rows))
	for _, dimensionBinding := range table.Rows {
		dimensionName := dimensionBinding.FieldID
		dimension, _ := runtime.model.ResolveDimension(dimensionName)
		key := dimensionBinding.Alias
		dimensions = append(dimensions, fieldRef(dimensionName, key))
		column := dashboard.TableColumn{Key: key, Label: dimensionLabel(key, dimension), Role: "row_header", Format: "text"}
		baseColumns = append(baseColumns, mergeTableColumn(column, tableColumnOverride(table, dimensionBinding.Alias)))
	}
	columnDimensionName := table.ColumnDims[0].FieldID
	columnDimensionAlias := table.ColumnDims[0].Alias
	dimensions = append(dimensions, fieldRef(columnDimensionName, columnDimensionAlias))
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
	sorts := make([]reportdef.QuerySort, 0, len(dimensions))
	for _, dimension := range dimensions {
		sorts = append(sorts, reportdef.QuerySort{Field: dimension.Alias, Direction: "asc"})
	}
	rawRows, err := runtime.data.Query(ctx, reportdef.AggregateQuery{
		Table:      table.Table,
		Dimensions: dimensions,
		Metrics:    metrics,
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      dashboard.TableInteractiveRowCap + 1,
	})
	if err != nil {
		return nil, nil, false, err
	}
	normalizedRows := tableRowsFromAnalytics(rawRows)
	calculationIncomplete := len(normalizedRows) >= dashboard.TableInteractiveRowCap+1
	base, err := visualizationir.SpecificationBase(table.Definition.Spec)
	if err != nil {
		return nil, nil, false, err
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		completeness := boundedFrameCompleteness(len(normalizedRows), dashboard.TableInteractiveRowCap+1)
		normalizedRows, err = applyCalculationsToTableRecords(base, table.Definition.Query.DatasetID, normalizedRows, completeness)
		if err != nil {
			return nil, nil, false, err
		}
	}
	valueFields := crossTabValueFields(table, base, runtime.model)
	columns := append([]dashboard.TableColumn{}, baseColumns...)
	pivotKeys := map[string]string{}
	usedKeys := map[string]string{}
	columnKeys := map[string]string{}
	for _, column := range baseColumns {
		usedKeys[column.Key] = column.Key
	}
	resultByKey := map[string]map[string]any{}
	order := []string{}
	for _, raw := range normalizedRows {
		rowKeyParts := make([]string, 0, len(table.Rows))
		for _, dimension := range table.Rows {
			rowKeyParts = append(rowKeyParts, fmt.Sprint(raw[dimension.Alias]))
		}
		resultKey := strings.Join(rowKeyParts, "\x00")
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
		label := fmt.Sprint(raw[columnDimensionAlias])
		groupLabel := label
		if pivotMode {
			metric := aggregateMemberMetadata(runtime.model, table.Metrics[0].FieldID)
			groupLabel = metricLabel(table.Metrics[0].Alias, metric)
		}
		pivotKey, exists := pivotKeys[label]
		if !exists {
			pivotKey = sanitizeTableKey(label)
			pivotKeys[label] = pivotKey
		}
		for _, valueField := range valueFields {
			metricKey := valueField.key
			columnIdentity := label + "\x00" + metricKey
			columnKey, columnExists := columnKeys[columnIdentity]
			candidate := "pivot_" + pivotKey
			columnLabel := label
			if !pivotMode || len(valueFields) > 1 {
				candidate += "__" + sanitizeTableKey(metricKey)
				columnLabel = valueField.label
			}
			if !columnExists {
				columnKey = uniqueTableColumnKey(candidate, usedKeys)
				columnKeys[columnIdentity] = columnKey
				usedKeys[columnKey] = columnKey
				column := dashboard.TableColumn{
					Key:         columnKey,
					Label:       columnLabel,
					Align:       "right",
					Role:        "metric",
					Group:       groupLabel,
					Metric:      metricKey,
					ColumnValue: label,
					Format:      valueField.format,
					Formatting:  valueField.formatting,
				}
				columns = append(columns, mergeTableColumn(column, tableColumnOverride(table, metricKey)))
			}
			row[columnKey] = raw[metricKey]
		}
	}
	result := make([]map[string]any, 0, len(order))
	for _, key := range order {
		result = append(result, resultByKey[key])
	}
	sortAggregateTableRows(result, request.Sort)
	return columns, result, calculationIncomplete, nil
}

type crossTabValueField struct {
	key        string
	label      string
	format     string
	formatting []dashboard.TableFormattingRule
}

func crossTabValueFields(table tablePlan, base visualizationir.VisualizationSpecBase, model *semanticmodel.Model) []crossTabValueField {
	fields := make([]crossTabValueField, 0, len(table.Metrics))
	for _, binding := range table.Metrics {
		metric := aggregateMemberMetadata(model, binding.FieldID)
		fields = append(fields, crossTabValueField{
			key: binding.Alias, label: metricLabel(binding.Alias, metric),
			format: tableMetricFormat(metric), formatting: tableMetricFormatting(table, binding.FieldID),
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
				format: dashboardCalculationFormat(field.Format),
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
	return reportdef.RowQuery{
		Table:      table.Table,
		Dimensions: dimensions,
		Metrics:    metrics,
		Filters:    queryFilters,
		Sort:       sorts,
		Limit:      count,
		Offset:     start,
	}, nil
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
