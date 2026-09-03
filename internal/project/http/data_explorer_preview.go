package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

const (
	dataExplorerDefaultLimit = int64(100)
	dataExplorerMaximumLimit = int64(1000)
	dataExplorerRowHeight    = int64(32)
)

var dataExplorerBlockIDs = []string{"a", "b", "c"}

// DataQueryExecutor is the narrow analytics port required by Data Explorer.
// The composed implementation applies the same authorization, admission, and
// audit policies as dashboard and semantic API queries.
type DataQueryExecutor interface {
	ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error)
}

func normalizeDataExplorerCommand(command projectsignals.DataExplorerCommand) projectsignals.DataExplorerCommand {
	if command.Limit <= 0 {
		command.Limit = dataExplorerDefaultLimit
	}
	if command.Limit > dataExplorerMaximumLimit {
		command.Limit = dataExplorerMaximumLimit
	}
	if command.Count <= 0 {
		command.Count = command.Limit
	}
	if command.Count > dataExplorerMaximumLimit {
		command.Count = dataExplorerMaximumLimit
	}
	if command.Offset < 0 {
		command.Offset = 0
	}
	if command.Start < 0 {
		command.Start = 0
	}
	if command.Start == 0 && command.Offset > 0 {
		command.Start = command.Offset
	}
	block := strings.TrimSpace(projectsignals.ValueOrZero(command.Block))
	if block != "a" && block != "b" && block != "c" && block != "all" {
		block = "all"
	}
	command.Block = projectsignals.Pointer(block)
	command.Sort = dataExplorerSortForColumns(command.Sort, command.VisibleColumns)
	if command.Mode == nil || strings.TrimSpace(*command.Mode) == "" {
		command.Mode = projectsignals.Pointer("browse")
	}
	if command.Explore != nil {
		command.Explore.Spec = normalizeExplorationSpec(command.Explore.Spec)
	}
	return command
}

func dataExplorerPreview(ctx context.Context, executor DataQueryExecutor, projectID projectgraph.ResourceID, object projectsignals.DataExplorerObjectSignal, command projectsignals.DataExplorerCommand) projectsignals.DataPreviewSignal {
	command = normalizeDataExplorerCommand(command)
	columns := explorerPreviewColumns(object)
	command.Sort = dataExplorerSortForObjectColumns(command.Sort, columns)
	preview := projectsignals.DataPreviewSignal{
		Columns: columns, Blocks: emptyDataExplorerBlocks(command), ChunkSize: command.Count,
		RowHeight: dataExplorerRowHeight, ResetVersion: command.ResetVersion, Sort: command.Sort,
		TotalRowLabel: object.RowCountLabel,
	}
	if executor == nil {
		preview.Error = projectsignals.Pointer("data preview execution is unavailable")
		return preview
	}

	starts, blockIDs := dataExplorerRequestedBlocks(command)
	totalKnown := false
	for index := 0; index < len(starts); index++ {
		start := starts[index]
		query, err := dataExplorerPreviewQuery(projectID, object, command, columns, start, command.Count, index == 0)
		if err != nil {
			preview.Error = projectsignals.Pointer(err.Error())
			return preview
		}
		result, err := executor.ExecuteDataQuery(ctx, query)
		if err != nil {
			preview.Error = projectsignals.Pointer(err.Error())
			return preview
		}
		if strings.TrimSpace(result.Error) != "" {
			preview.Error = projectsignals.Pointer(result.Error)
			return preview
		}
		if result.SQL != "" {
			preview.SQL = projectsignals.Pointer(result.SQL)
		}
		if result.TotalRowsKnown {
			totalKnown = true
			preview.TotalRows = int64(result.TotalRows)
			preview.AvailableRows = preview.TotalRows
			preview.TotalRowLabel = projectsignals.Pointer(fmt.Sprintf("%d", result.TotalRows))
		}
		rows := dataExplorerRows(result.Rows)
		preview.Blocks[blockIDs[index]] = projectsignals.DataPreviewBlockSignal{
			Start: start, RequestSeq: command.RequestSeq, ResetVersion: command.ResetVersion, Sort: command.Sort, Rows: rows,
		}
		if !totalKnown {
			loaded := start + int64(len(rows))
			if loaded > preview.AvailableRows {
				preview.AvailableRows = loaded
				preview.TotalRows = loaded
			}
			if int64(len(rows)) == command.Count {
				// Keep one more window reachable when the executor cannot provide a
				// total. A short following block closes the provisional range.
				preview.AvailableRows = loaded + command.Count
				preview.TotalRows = preview.AvailableRows
			}
		}
		if index == 0 && projectsignals.ValueOrZero(command.Block) == "all" {
			starts, blockIDs = dataExplorerRemainingBlocks(command, preview, int64(len(rows)), totalKnown)
		}
	}
	if !totalKnown && preview.TotalRowLabel == nil {
		preview.TotalRowLabel = projectsignals.Pointer("Unknown")
	}
	return preview
}

func dataExplorerPreviewQuery(projectID projectgraph.ResourceID, object projectsignals.DataExplorerObjectSignal, command projectsignals.DataExplorerCommand, columns []projectsignals.DataPreviewColumnSignal, start, count int64, includeTotal bool) (dataquery.Query, error) {
	modelID := strings.TrimSpace(projectsignals.ValueOrZero(object.ModelID))
	table := strings.TrimSpace(projectsignals.ValueOrZero(object.Table))
	if object.Layer != "model_table" {
		return dataquery.Query{}, fmt.Errorf("data preview is not supported for %s resources", object.Layer)
	}
	if modelID == "" || table == "" {
		return dataquery.Query{}, fmt.Errorf("model table preview target is incomplete")
	}
	columnNames := make([]string, 0, len(columns))
	for _, column := range columns {
		if key := strings.TrimSpace(column.Key); key != "" {
			columnNames = append(columnNames, key)
		}
	}
	sortSpec := []dataquery.Sort{}
	if column := strings.TrimSpace(projectsignals.ValueOrZero(command.Sort.Column)); column != "" {
		sortSpec = append(sortSpec, dataquery.Sort{Field: column, Direction: projectsignals.ValueOrZero(command.Sort.Direction)})
	}
	query := dataquery.ModelTableRows(modelID, table, columnNames, sortSpec, int(start), int(count), includeTotal)
	return query.WithMetadata(dataquery.Metadata{
		ProjectID: projectID, Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationPreviewWindow,
		ObjectType: object.Layer, ObjectID: object.ResourceID,
	}), nil
}

func explorerPreviewColumns(object projectsignals.DataExplorerObjectSignal) []projectsignals.DataPreviewColumnSignal {
	if object.Columns == nil {
		return []projectsignals.DataPreviewColumnSignal{}
	}
	return append([]projectsignals.DataPreviewColumnSignal(nil), (*object.Columns)...)
}

func dataExplorerSortForColumns(sortSignal projectsignals.DataPreviewSortSignal, visibleColumns *[]string) projectsignals.DataPreviewSortSignal {
	column := strings.TrimSpace(projectsignals.ValueOrZero(sortSignal.Column))
	direction := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(sortSignal.Direction)))
	if column == "" || (direction != "asc" && direction != "desc") {
		return projectsignals.DataPreviewSortSignal{}
	}
	if visibleColumns != nil && len(*visibleColumns) > 0 {
		for _, visible := range *visibleColumns {
			if visible == column {
				return projectsignals.DataPreviewSortSignal{Column: projectsignals.Pointer(column), Direction: projectsignals.Pointer(direction)}
			}
		}
		return projectsignals.DataPreviewSortSignal{}
	}
	return projectsignals.DataPreviewSortSignal{Column: projectsignals.Pointer(column), Direction: projectsignals.Pointer(direction)}
}

func dataExplorerSortForObjectColumns(sortSignal projectsignals.DataPreviewSortSignal, columns []projectsignals.DataPreviewColumnSignal) projectsignals.DataPreviewSortSignal {
	column := strings.TrimSpace(projectsignals.ValueOrZero(sortSignal.Column))
	if column == "" {
		return projectsignals.DataPreviewSortSignal{}
	}
	for _, candidate := range columns {
		if candidate.Key == column {
			return sortSignal
		}
	}
	return projectsignals.DataPreviewSortSignal{}
}

func emptyDataExplorerBlocks(command projectsignals.DataExplorerCommand) map[string]projectsignals.DataPreviewBlockSignal {
	blocks := make(map[string]projectsignals.DataPreviewBlockSignal, len(dataExplorerBlockIDs))
	for index, id := range dataExplorerBlockIDs {
		blocks[id] = projectsignals.DataPreviewBlockSignal{
			Start: int64(index) * command.Count, ResetVersion: command.ResetVersion, Sort: command.Sort, Rows: []map[string]any{},
		}
	}
	return blocks
}

func dataExplorerRequestedBlocks(command projectsignals.DataExplorerCommand) ([]int64, []string) {
	block := projectsignals.ValueOrZero(command.Block)
	if block == "all" {
		return []int64{command.Start}, []string{"a"}
	}
	return []int64{command.Start}, []string{block}
}

func dataExplorerRemainingBlocks(command projectsignals.DataExplorerCommand, preview projectsignals.DataPreviewSignal, firstRows int64, totalKnown bool) ([]int64, []string) {
	starts := []int64{command.Start}
	blocks := []string{"a"}
	if firstRows < command.Count {
		return starts, blocks
	}
	for index := int64(1); index < int64(len(dataExplorerBlockIDs)); index++ {
		start := command.Start + index*command.Count
		if totalKnown && start >= preview.AvailableRows {
			break
		}
		starts = append(starts, start)
		blocks = append(blocks, dataExplorerBlockIDs[index])
	}
	return starts, blocks
}

func dataExplorerRows(rows []dataquery.Row) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		copy := make(map[string]any, len(row))
		for key, value := range row {
			copy[key] = value
		}
		out = append(out, copy)
	}
	return out
}

func dataExplorerSemanticResult(ctx context.Context, executor DataQueryExecutor, projectID projectgraph.ResourceID, command projectsignals.DataExploreCommand, fields []projectsignals.DataExploreFieldSignal, model *semanticmodel.Model) (projectsignals.DataExploreCommand, projectsignals.DataExploreResultSignal) {
	spec := normalizeExplorationSpec(command.Spec)
	resultSignal := projectsignals.DataExploreResultSignal{
		Columns: []projectsignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{}, RequestSeq: command.RequestSeq,
	}
	command.Spec = spec
	if !explorationSpecIsEmpty(spec) {
		if err := exploration.ValidateShape(&spec); err != nil {
			resultSignal.Error = projectsignals.Pointer("invalid exploration command: " + err.Error())
			return command, resultSignal
		}
	}
	if spec.Pivot != nil {
		// Pivot changes the result shape and cannot be represented by the
		// existing aggregate executor. Do not silently execute the non-pivot
		// query in its place, including for a pivot-only selection.
		resultSignal.Error = projectsignals.Pointer("pivot exploration execution is not supported")
		return command, resultSignal
	}
	state := dataExploreStateFromSpec(spec)
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	state.Dimensions = validExplorerFields(state.Dimensions, "dimension", fieldByID)
	state.Metrics = validExplorerFields(state.Metrics, "metric", fieldByID)
	state.Filters = validExplorerFilters(state.Filters, fieldByID)
	state.Sort = validExplorerSort(state.Sort, state, explorationSpecSortAliases(spec))
	command.Spec = explorationSpecWithState(spec, state)
	spec = command.Spec
	if !explorationSpecIsEmpty(spec) {
		if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
			resultSignal.Error = projectsignals.Pointer("invalid exploration command: " + err.Error())
			return command, resultSignal
		}
	}
	if len(state.Dimensions) == 0 && len(state.Metrics) == 0 && spec.Time == nil {
		return command, resultSignal
	}
	if executor == nil {
		resultSignal.Error = projectsignals.Pointer("governed exploration execution is unavailable")
		return command, resultSignal
	}
	modelID := strings.TrimSpace(spec.ModelID)
	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
	clearTarget := explorerCommandHasMultiRootMetric(state.Metrics, fieldByID)
	if modelID == "" || datasetID == "" {
		resultSignal.Error = projectsignals.Pointer("semantic exploration target is incomplete")
		return command, resultSignal
	}
	aliases := explorerSpecQueryAliases(spec)
	dimensions := make([]dataquery.Field, 0, len(spec.Dimensions))
	for _, field := range spec.Dimensions {
		dimensions = append(dimensions, dataquery.Field{Field: field.Field, Alias: firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), aliases[field.Field]), Grain: string(projectsignals.ValueOrZero(field.Grain))})
	}
	metrics := make([]dataquery.Field, 0, len(spec.Metrics))
	for _, field := range spec.Metrics {
		metrics = append(metrics, dataquery.Field{Field: field.Field, Alias: firstExplorerNonEmpty(projectsignals.ValueOrZero(field.Alias), aliases[field.Field])})
	}
	filters, err := lowerExplorationFilters(spec, fieldByID)
	if err != nil {
		resultSignal.Error = projectsignals.Pointer(err.Error())
		return command, resultSignal
	}
	sortSpec := make([]dataquery.Sort, 0, len(spec.Sort))
	for _, sortSignal := range spec.Sort {
		sortSpec = append(sortSpec, dataquery.Sort{Field: sortSignal.Field, Direction: string(sortSignal.Direction)})
	}
	// A metric with multiple physical roots is not owned by the selected
	// browser dataset. Leave the target unscoped so the governed planner can
	// infer all metric datasets and validate the selected qualified dimensions.
	queryTarget := datasetID
	if clearTarget {
		queryTarget = ""
	}
	query := dataquery.SemanticAggregate(modelID, queryTarget, dimensions, metrics, filters, sortSpec, 0, int(spec.Limit)+1)
	if spec.Time != nil {
		query.Time = dataquery.Time{Field: spec.Time.Field, Grain: string(spec.Time.Grain), Alias: projectsignals.ValueOrZero(spec.Time.Alias)}
	}
	query = query.WithMetadata(dataquery.Metadata{
		ProjectID: projectID, Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationSemanticExplore,
		ObjectType: "semantic_dataset", ObjectID: modelID + ":" + datasetID,
	})
	executed, err := executor.ExecuteDataQuery(ctx, query)
	if err != nil {
		resultSignal.Error = projectsignals.Pointer(err.Error())
		return command, resultSignal
	}
	if strings.TrimSpace(executed.Error) != "" {
		resultSignal.Error = projectsignals.Pointer(executed.Error)
		return command, resultSignal
	}
	rows := executed.Rows
	truncated := int64(len(rows)) > int64(spec.Limit)
	if truncated {
		rows = rows[:spec.Limit]
	}
	labels := explorerResultLabels(fields, aliases)
	columns := make([]projectsignals.DataPreviewColumnSignal, 0, len(executed.Columns))
	for _, column := range executed.Columns {
		columns = append(columns, projectsignals.DataPreviewColumnSignal{Key: column.Name, Label: firstExplorerNonEmpty(labels[column.Name], column.Name)})
	}
	return command, projectsignals.DataExploreResultSignal{
		Columns: columns, Rows: dataExplorerRows(rows), SQL: projectsignals.Optional(executed.SQL), Plan: projectsignals.Optional(executed.PlanText),
		DurationMS: executed.DurationMS, RowsReturned: int64(len(rows)), Truncated: truncated,
		Warnings: append([]string(nil), executed.Warnings...), RequestSeq: command.RequestSeq,
	}
}

func explorerSpecQueryAliases(spec exploration.ExplorationSpec) map[string]string {
	fields := make([]string, 0, len(spec.Dimensions)+len(spec.Metrics))
	aliases := make(map[string]string, len(fields))
	for _, dimension := range spec.Dimensions {
		fields = append(fields, dimension.Field)
		if alias := strings.TrimSpace(projectsignals.ValueOrZero(dimension.Alias)); alias != "" {
			aliases[dimension.Field] = alias
		}
	}
	for _, metric := range spec.Metrics {
		fields = append(fields, metric.Field)
		if alias := strings.TrimSpace(projectsignals.ValueOrZero(metric.Alias)); alias != "" {
			aliases[metric.Field] = alias
		}
	}
	derived := explorerQueryAliases(fields, nil)
	for field, alias := range aliases {
		derived[field] = alias
	}
	return derived
}

func lowerExplorationFilters(spec exploration.ExplorationSpec, fields map[string]projectsignals.DataExploreFieldSignal) ([]dataquery.Filter, error) {
	filters := make([]dataquery.Filter, 0, len(spec.Filters)+2)
	for index, authored := range spec.Filters {
		field, ok := fields[authored.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			return nil, fmt.Errorf("filter %d field %q is unavailable", index+1, authored.Field)
		}
		dataset := strings.TrimSpace(projectsignals.ValueOrZero(authored.DatasetID))
		if rangeExpression, ok := authored.Expression.Value.(*exploration.RangeExplorationFilterExpression); ok {
			if rangeExpression.Lower == nil && rangeExpression.Upper == nil {
				return nil, fmt.Errorf("filter %d range requires a lower or upper bound", index+1)
			}
			if rangeExpression.Lower != nil {
				value, err := lowerExplorationFilterValue(rangeExpression.Lower.Value)
				if err != nil {
					return nil, fmt.Errorf("filter %d lower bound: %w", index+1, err)
				}
				op := "greater_than"
				if rangeExpression.Lower.Inclusive {
					op = "greater_than_or_equal"
				}
				filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: op, Values: []any{value}})
			}
			if rangeExpression.Upper != nil {
				value, err := lowerExplorationFilterValue(rangeExpression.Upper.Value)
				if err != nil {
					return nil, fmt.Errorf("filter %d upper bound: %w", index+1, err)
				}
				op := "less_than"
				if rangeExpression.Upper.Inclusive {
					op = "less_than_or_equal"
				}
				filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: op, Values: []any{value}})
			}
			continue
		}
		operator, values, err := lowerExplorationFilterExpression(authored.Expression)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", index+1, err)
		}
		if operator != "" {
			filters = append(filters, dataquery.Filter{Field: authored.Field, Dataset: dataset, Operator: operator, Values: values})
		}
	}
	if spec.Time != nil && spec.Time.Range != nil {
		bounds, err := lowerExplorationTimeRange(spec.Time.Field, *spec.Time.Range)
		if err != nil {
			return nil, err
		}
		filters = append(filters, bounds...)
	}
	return filters, nil
}

func lowerExplorationFilterExpression(expression exploration.ExplorationFilterExpression) (string, []any, error) {
	switch expression := expression.Value.(type) {
	case *exploration.UnfilteredExplorationFilterExpression:
		return "", nil, nil
	case *exploration.NullCheckExplorationFilterExpression:
		return string(expression.Operator), nil, nil
	case *exploration.SetExplorationFilterExpression:
		values := make([]any, 0, len(expression.Values))
		for _, value := range expression.Values {
			native, err := lowerExplorationFilterValue(value)
			if err != nil {
				return "", nil, err
			}
			values = append(values, native)
		}
		return string(expression.Operator), values, nil
	case *exploration.ComparisonExplorationFilterExpression:
		value, err := lowerExplorationFilterValue(expression.Value)
		if err != nil {
			return "", nil, err
		}
		return string(expression.Operator), []any{value}, nil
	case *exploration.RangeExplorationFilterExpression:
		return "", nil, errors.New("range expression must be lowered as two predicates")
	case *exploration.RelativePeriodExplorationFilterExpression:
		return "", nil, errors.New("relative-period filters are not supported by the exploration executor")
	case nil:
		return "", nil, errors.New("filter expression is required")
	default:
		return "", nil, fmt.Errorf("unsupported filter expression %T", expression)
	}
}

func lowerExplorationFilterValue(value exploration.ExplorationFilterValue) (any, error) {
	switch value := value.Value.(type) {
	case *exploration.StringExplorationFilterValue:
		return value.Value, nil
	case *exploration.BooleanExplorationFilterValue:
		return value.Value, nil
	case *exploration.IntegerExplorationFilterValue:
		parsed, err := strconv.ParseInt(value.Value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid integer value %q", value.Value)
		}
		return parsed, nil
	case *exploration.DecimalExplorationFilterValue:
		return json.Number(value.Value), nil
	case *exploration.DateExplorationFilterValue:
		return value.Value, nil
	case *exploration.TimestampExplorationFilterValue:
		return value.Value, nil
	case nil:
		return nil, errors.New("filter value is required")
	default:
		return nil, fmt.Errorf("unsupported filter value %T", value)
	}
}

func lowerExplorationTimeRange(field string, rangeSpec exploration.ExplorationTimeRange) ([]dataquery.Filter, error) {
	switch rangeValue := rangeSpec.Value.(type) {
	case *exploration.AbsoluteExplorationTimeRange:
		filters := make([]dataquery.Filter, 0, 2)
		if rangeValue.Lower != nil {
			value, err := lowerExplorationTemporalValue(rangeValue.Lower.Value)
			if err != nil {
				return nil, err
			}
			direction := "greater_than"
			if rangeValue.Lower.Inclusive {
				direction = "greater_than_or_equal"
			}
			filters = append(filters, dataquery.Filter{Field: field, Operator: direction, Values: []any{value}})
		}
		if rangeValue.Upper != nil {
			value, err := lowerExplorationTemporalValue(rangeValue.Upper.Value)
			if err != nil {
				return nil, err
			}
			direction := "less_than"
			if rangeValue.Upper.Inclusive {
				direction = "less_than_or_equal"
			}
			filters = append(filters, dataquery.Filter{Field: field, Operator: direction, Values: []any{value}})
		}
		return filters, nil
	case *exploration.RelativeExplorationTimeRange:
		return nil, errors.New("relative time ranges are not supported by the exploration executor")
	case nil:
		return nil, errors.New("time range variant is required")
	default:
		return nil, fmt.Errorf("unsupported time range %T", rangeValue)
	}
}

func lowerExplorationTemporalValue(value exploration.ExplorationTemporalValue) (any, error) {
	switch value := value.Value.(type) {
	case *exploration.DateExplorationTemporalValue:
		return value.Value, nil
	case *exploration.TimestampExplorationTemporalValue:
		return value.Value, nil
	case nil:
		return nil, errors.New("time bound value is required")
	default:
		return nil, fmt.Errorf("unsupported time bound value %T", value)
	}
}

func explorerCommandHasMultiRootMetric(metrics []string, fields map[string]projectsignals.DataExploreFieldSignal) bool {
	for _, id := range metrics {
		field, ok := fields[id]
		if ok && field.Kind == "metric" && strings.TrimSpace(field.ModelTable) == "" {
			return true
		}
	}
	return false
}

func validExplorerFields(values []string, kind string, fields map[string]projectsignals.DataExploreFieldSignal) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		field, ok := fields[value]
		if !ok || field.Kind != kind || !field.Compatible {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validExplorerFilters(filters []dataExploreFilter, fields map[string]projectsignals.DataExploreFieldSignal) []dataExploreFilter {
	out := make([]dataExploreFilter, 0, len(filters))
	for _, filter := range filters {
		field, ok := fields[filter.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible || strings.TrimSpace(filter.Operator) == "" {
			continue
		}
		filter.Values = append([]string(nil), filter.Values...)
		out = append(out, filter)
	}
	return out
}

func explorationSpecSortAliases(spec exploration.ExplorationSpec) map[string]struct{} {
	selected := map[string]struct{}{}
	for _, dimension := range spec.Dimensions {
		if dimension.Alias != nil && strings.TrimSpace(*dimension.Alias) != "" {
			selected[*dimension.Alias] = struct{}{}
		}
	}
	for _, metric := range spec.Metrics {
		if metric.Alias != nil && strings.TrimSpace(*metric.Alias) != "" {
			selected[*metric.Alias] = struct{}{}
		}
	}
	return selected
}

func validExplorerSort(sortSignals []dataExploreSort, command dataExploreState, aliases ...map[string]struct{}) []dataExploreSort {
	selected := map[string]struct{}{}
	for _, field := range append(append([]string(nil), command.Dimensions...), command.Metrics...) {
		selected[field] = struct{}{}
	}
	for _, aliasSet := range aliases {
		for alias := range aliasSet {
			selected[alias] = struct{}{}
		}
	}
	out := make([]dataExploreSort, 0, len(sortSignals))
	for _, sortSignal := range sortSignals {
		direction := strings.ToLower(strings.TrimSpace(sortSignal.Direction))
		if _, ok := selected[sortSignal.Field]; !ok || (direction != "asc" && direction != "desc") {
			continue
		}
		sortSignal.Direction = direction
		out = append(out, sortSignal)
	}
	return out
}

func explorerQueryAliases(dimensions, metrics []string) map[string]string {
	all := append(append([]string(nil), dimensions...), metrics...)
	counts := map[string]int{}
	for _, field := range all {
		counts[explorerFieldName(field)]++
	}
	out := make(map[string]string, len(all))
	for _, field := range all {
		table, name, found := strings.Cut(field, ".")
		if !found {
			name = field
		}
		if counts[name] > 1 && table != "" {
			name = table + "__" + name
		}
		out[field] = name
	}
	return out
}

func explorerFieldName(field string) string {
	if index := strings.LastIndex(field, "."); index >= 0 && index+1 < len(field) {
		return field[index+1:]
	}
	return field
}

func explorerResultLabels(fields []projectsignals.DataExploreFieldSignal, aliases map[string]string) map[string]string {
	out := make(map[string]string, len(aliases))
	for _, field := range fields {
		if alias := aliases[field.ID]; alias != "" {
			out[alias] = firstExplorerNonEmpty(field.Label, alias)
		}
	}
	return out
}
