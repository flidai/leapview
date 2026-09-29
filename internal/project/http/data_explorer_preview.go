package http

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
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

// SemanticAccessConsumerProvider is an optional query-executor capability used
// only for protected semantic discovery. Public models continue to use the
// ordinary DataQueryExecutor path. Implementations must return a consumer
// bound to the request principal and active serving generation.
type SemanticAccessConsumerProvider interface {
	SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
}

func dataExplorerSemanticConsumers(ctx context.Context, executor DataQueryExecutor, definition projectmanifest.ResourceManifest) map[string]*semanticquery.SemanticAccessConsumer {
	provider, ok := executor.(SemanticAccessConsumerProvider)
	if !ok || provider == nil {
		return nil
	}
	consumers := make(map[string]*semanticquery.SemanticAccessConsumer)
	modelIDs := make([]string, 0, len(definition.SemanticModels))
	for modelID := range definition.SemanticModels {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)
	for _, modelID := range modelIDs {
		model := definition.SemanticModels[modelID]
		if !semanticquery.ModelRequiresSemanticAccess(model) {
			continue
		}
		consumer, err := provider.SemanticConsumer(ctx, modelID)
		if err != nil || consumer == nil {
			continue
		}
		consumers[modelID] = consumer
	}
	return consumers
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
	if errors.Is(ctx.Err(), context.Canceled) {
		preview.Stale = true
		return preview
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
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
				preview.Stale = true
				return preview
			}
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
	semanticModelID := strings.TrimSpace(projectsignals.ValueOrZero(object.SemanticModelID))
	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(object.DatasetID))
	if object.Layer != "model" {
		return dataquery.Query{}, fmt.Errorf("data preview is not supported for %s resources", object.Layer)
	}
	if semanticModelID == "" || datasetID == "" {
		return dataquery.Query{}, fmt.Errorf("model preview target is incomplete")
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
	query := dataquery.ModelRows(semanticModelID, datasetID, columnNames, sortSpec, int(start), int(count), includeTotal)
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
		start := int64(index) * command.Count
		seq := int64(0)
		if projectsignals.ValueOrZero(command.Block) == "all" {
			start += dataExplorerFirstBlockStart(command)
			seq = command.RequestSeq
		}
		blocks[id] = projectsignals.DataPreviewBlockSignal{
			Start: start, RequestSeq: seq, ResetVersion: command.ResetVersion, Sort: command.Sort, Rows: []map[string]any{},
		}
	}
	return blocks
}

// A full window uses the same preceding/current/following block contract as
// lv-windowed-table. Empty trailing blocks still acknowledge the request.
func dataExplorerFirstBlockStart(command projectsignals.DataExplorerCommand) int64 {
	count := max(int64(1), command.Count)
	current := max(int64(0), command.Start) / count * count
	return max(int64(0), current-count)
}

func dataExplorerRequestedBlocks(command projectsignals.DataExplorerCommand) ([]int64, []string) {
	block := projectsignals.ValueOrZero(command.Block)
	if block == "all" {
		return []int64{dataExplorerFirstBlockStart(command)}, []string{"a"}
	}
	return []int64{command.Start}, []string{block}
}

func dataExplorerRemainingBlocks(command projectsignals.DataExplorerCommand, preview projectsignals.DataPreviewSignal, firstRows int64, totalKnown bool) ([]int64, []string) {
	firstStart := dataExplorerFirstBlockStart(command)
	starts := []int64{firstStart}
	blocks := []string{"a"}
	if firstRows < command.Count {
		return starts, blocks
	}
	for index := int64(1); index < int64(len(dataExplorerBlockIDs)); index++ {
		start := firstStart + index*command.Count
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

func dataExplorerSemanticResult(ctx context.Context, executor DataQueryExecutor, lowerer exploration.QueryLowerer, projectID projectgraph.ResourceID, command projectsignals.DataExploreCommand, fields []projectsignals.DataExploreFieldSignal, model *semanticmodel.Model, compiled *semanticquery.CompiledModel) (projectsignals.DataExploreCommand, projectsignals.DataExploreResultSignal) {
	command = dataExploreCommandWithCanonicalSpec(command)
	command.Spec = normalizeExplorationSpec(command.Spec)
	resultSignal := projectsignals.DataExploreResultSignal{
		Columns: []projectsignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{}, RequestSeq: command.RequestSeq,
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return command, resultSignal
	}
	if explorationSpecIsEmpty(command.Spec) {
		return command, resultSignal
	}
	if err := exploration.ValidateAgainstModel(model, &command.Spec); err != nil {
		resultSignal.Error = projectsignals.Pointer("invalid exploration command: " + err.Error())
		return command, resultSignal
	}
	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(fields))
	for _, field := range fields {
		fieldByID[field.ID] = field
	}
	for _, selected := range command.Spec.Dimensions {
		field, ok := fieldByID[selected.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			resultSignal.Error = projectsignals.Pointer(fmt.Sprintf("invalid exploration command: dimension %q is unavailable", selected.Field))
			return command, resultSignal
		}
	}
	for _, selected := range command.Spec.Metrics {
		field, ok := fieldByID[selected.Field]
		if !ok || field.Kind != "metric" || !field.Compatible {
			resultSignal.Error = projectsignals.Pointer(fmt.Sprintf("invalid exploration command: metric %q is unavailable", selected.Field))
			return command, resultSignal
		}
	}
	for index, filter := range command.Spec.Filters {
		field, ok := fieldByID[filter.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			resultSignal.Error = projectsignals.Pointer(fmt.Sprintf("invalid exploration command: filter %d field %q is unavailable", index+1, filter.Field))
			return command, resultSignal
		}
	}
	if err := validateExplorerFilterDatasets(command.Spec, fieldByID, compiled); err != nil {
		resultSignal.Error = projectsignals.Pointer("invalid exploration command: " + err.Error())
		return command, resultSignal
	}
	if len(command.Spec.Dimensions) == 0 && len(command.Spec.Metrics) == 0 && command.Spec.Time == nil {
		return command, resultSignal
	}
	if executor == nil {
		resultSignal.Error = projectsignals.Pointer("governed exploration execution is unavailable")
		return command, resultSignal
	}
	if lowerer == nil {
		resultSignal.Error = projectsignals.Pointer("governed exploration query lowering is unavailable")
		return command, resultSignal
	}
	query, err := lowerer.QueryForModel(command.Spec, model)
	if err != nil {
		resultSignal.Error = projectsignals.Pointer(err.Error())
		return command, resultSignal
	}
	modelID := strings.TrimSpace(command.Spec.ModelID)
	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(command.Spec.DatasetID))
	query = query.WithMetadata(dataquery.Metadata{
		ProjectID: projectID, Surface: dataquery.SurfaceDataExplorer, Operation: dataquery.OperationSemanticExplore,
		ObjectType: "semantic_dataset", ObjectID: modelID + ":" + datasetID,
	})
	executed, err := executor.ExecuteDataQuery(ctx, query)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return command, resultSignal
		}
		resultSignal.Error = projectsignals.Pointer(err.Error())
		return command, resultSignal
	}
	if strings.TrimSpace(executed.Error) != "" {
		if errors.Is(ctx.Err(), context.Canceled) {
			return command, resultSignal
		}
		resultSignal.Error = projectsignals.Pointer(executed.Error)
		return command, resultSignal
	}
	rows := executed.Rows
	limit := int64(command.Spec.Limit)
	truncated := int64(len(rows)) > limit
	if truncated {
		rows = rows[:limit]
	}
	state := dataExploreStateFromSpec(command.Spec)
	aliases := explorerQueryAliases(state.Dimensions, state.Metrics)
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

func explorerCommandHasMultiRootMetric(metrics []string, fields map[string]projectsignals.DataExploreFieldSignal) bool {
	for _, id := range metrics {
		field, ok := fields[id]
		if ok && field.Kind == "metric" && strings.TrimSpace(field.DatasetID) == "" {
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

func validExplorerFilters(filters []projectsignals.DataExploreFilterSignal, fields map[string]projectsignals.DataExploreFieldSignal) []projectsignals.DataExploreFilterSignal {
	out := make([]projectsignals.DataExploreFilterSignal, 0, len(filters))
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

func validExplorerSort(sortSignals []projectsignals.DataExploreSortSignal, command projectsignals.DataExploreCommand) []projectsignals.DataExploreSortSignal {
	selected := map[string]struct{}{}
	for _, field := range append(append([]string(nil), command.Dimensions...), command.Metrics...) {
		selected[field] = struct{}{}
	}
	out := make([]projectsignals.DataExploreSortSignal, 0, len(sortSignals))
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
