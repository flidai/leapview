package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/workspace"
	"github.com/flidai/leapview/internal/workspace/assetnav"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

func (h Handler) globalDataExplorerState(r *nethttp.Request, command uisignals.DataExplorerCommand) (uisignals.DataExplorerPageSignal, uisignals.DataExplorerSignal, error) {
	return h.globalDataExplorerStateWithCurrent(r, command, nil)
}

func (h Handler) DataExplorerState(r *nethttp.Request, command uisignals.DataExplorerCommand) (uisignals.DataExplorerPageSignal, uisignals.DataExplorerSignal, error) {
	return h.globalDataExplorerState(r, command)
}

func (h Handler) globalDataExplorerStateWithCurrent(r *nethttp.Request, command uisignals.DataExplorerCommand, current *uisignals.DataExplorerSignal) (uisignals.DataExplorerPageSignal, uisignals.DataExplorerSignal, error) {
	command = normalizeDataExplorerCommand(command)
	workspaces, err := h.workspaceList(r)
	if err != nil {
		return uisignals.DataExplorerPageSignal{}, uisignals.DataExplorerSignal{}, err
	}
	sort.SliceStable(workspaces, func(i, j int) bool {
		left := strings.ToLower(firstNonEmpty(workspaces[i].Title, workspaces[i].ID))
		right := strings.ToLower(firstNonEmpty(workspaces[j].Title, workspaces[j].ID))
		if left != right {
			return left < right
		}
		return workspaces[i].ID < workspaces[j].ID
	})
	environment := string(h.environment(r))
	objects := []uisignals.DataExplorerObjectSignal{}
	warnings := []string{}
	for _, workspace := range workspaces {
		metrics, ok := h.metricsForWorkspace(workspace.ID)
		if !ok || metrics == nil {
			warnings = append(warnings, fmt.Sprintf("Workspace %q metrics are not configured.", workspace.ID))
			continue
		}
		assets, edges, err := h.workspaceAssetsAndEdgesForData(r.Context(), workspace.ID, environment)
		if err != nil {
			fallback := dataExplorerObjectsFromMetrics(workspace.ID, firstNonEmpty(workspace.Title, workspace.ID), metrics)
			if len(fallback) == 0 {
				warnings = append(warnings, fmt.Sprintf("Workspace %q assets are unavailable: %v", workspace.ID, err))
			}
			objects = append(objects, fallback...)
			continue
		}
		workspaceObjects, objectWarnings := dataExplorerObjects(workspace.ID, firstNonEmpty(workspace.Title, workspace.ID), metrics, assets, edges)
		objects = append(objects, workspaceObjects...)
		warnings = append(warnings, objectWarnings...)
	}
	selected, selectionWarnings := selectGlobalDataExplorerObject(objects, uisignals.ValueOrZero(command.WorkspaceID), uisignals.ValueOrZero(command.ObjectKey))
	warnings = append(warnings, selectionWarnings...)
	if selected != nil {
		command.WorkspaceID = uisignals.Optional(selected.WorkspaceID)
		command.ObjectKey = uisignals.Optional(selected.Key)
		command.Sort = dataPreviewSortForColumns(uisignals.ValueOrZero(selected.Columns), command.Sort)
	}
	exploreWorkspaceID := firstNonEmpty(uisignals.ValueOrZero(command.Explore.WorkspaceID), uisignals.ValueOrZero(command.WorkspaceID))
	if exploreWorkspaceID == "" && len(workspaces) > 0 {
		exploreWorkspaceID = workspaces[0].ID
	}
	exploreCommand := normalizeDataExploreCommand(*command.Explore, exploreWorkspaceID)
	command.Explore = &exploreCommand
	explore := h.dataExploreState(r.Context(), exploreCommand)
	command.Explore = &explore.Command
	if uisignals.ValueOrZero(command.Mode) == "explore" {
		if queryObject := dataExploreObjectForDataset(objects, explore.Command); queryObject != nil {
			selected = queryObject
			command.WorkspaceID = uisignals.Optional(queryObject.WorkspaceID)
			command.ObjectKey = uisignals.Optional(queryObject.Key)
		}
	}
	explorer := uisignals.DataExplorerSignal{
		Objects:             objects,
		SelectedWorkspaceID: command.WorkspaceID,
		SelectedKey:         command.ObjectKey,
		Command:             command,
		Explore:             explore,
		Warnings:            uisignals.OptionalSlice(warnings),
		Preview: uisignals.DataPreviewSignal{
			Columns:       []uisignals.DataPreviewColumnSignal{},
			TotalRows:     0,
			AvailableRows: 0,
			ChunkSize:     command.Count,
			RowHeight:     dataExplorerRowHeight,
			ResetVersion:  command.ResetVersion,
			Blocks:        emptyDataPreviewBlocks(int(command.Count), command.Sort, int(command.ResetVersion)),
			Sort:          command.Sort,
		},
	}
	if selected != nil {
		copy := *selected
		explorer.SelectedObject = &copy
		if uisignals.ValueOrZero(command.Mode) == "explore" {
			// Browse state stays available when the user returns, but Explore does
			// not spend a second query on the hidden row preview.
		} else if metrics, ok := h.metricsForWorkspace(copy.WorkspaceID); ok && metrics != nil {
			explorer.Preview = h.dataPreview(r.Context(), metrics, copy, command, current)
		} else {
			explorer.Preview.Error = uisignals.Pointer(fmt.Sprintf("workspace %q metrics are not configured", copy.WorkspaceID))
		}
	}
	page := uisignals.DataExplorerPageSignal{
		Kind:                uisignals.RouteData,
		Title:               "Data Explorer",
		Description:         uisignals.Pointer("Browse model data, select fields, and build governed result tables."),
		WorkspaceID:         command.WorkspaceID,
		SelectedWorkspaceID: command.WorkspaceID,
		SelectedObject:      command.ObjectKey,
		Workspaces:          dataExplorerWorkspaceSignals(workspaces, objects, uisignals.ValueOrZero(command.WorkspaceID)),
		Tabs: []uisignals.WorkspaceTabSignal{
			{ID: "all", Label: "All", Href: "/data", Active: true},
		},
	}
	return page, explorer, nil
}

func (h Handler) dataExploreState(ctx context.Context, command uisignals.DataExploreCommand) uisignals.DataExploreSignal {
	result := uisignals.DataExploreResultSignal{
		Columns: []uisignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{},
		RequestSeq: command.RequestSeq,
	}
	state := uisignals.DataExploreSignal{
		Command: command, Models: []uisignals.DataExploreModelSignal{}, Datasets: []uisignals.DataExploreDatasetSignal{},
		Fields: []uisignals.DataExploreFieldSignal{}, Result: result,
	}
	workspaceID := uisignals.ValueOrZero(command.WorkspaceID)
	metrics, ok := h.metricsForWorkspace(workspaceID)
	if !ok || metrics == nil {
		state.Result.Error = uisignals.Pointer(fmt.Sprintf("workspace %q metrics are not configured", workspaceID))
		return state
	}
	models := append([]navigationModel(nil), dataExploreNavigationModels(metrics)...)
	projections := map[string]DataExplorerModel{}
	for _, summary := range models {
		model, ok := metrics.DataExplorerModel(summary.ID)
		if !ok {
			continue
		}
		projections[summary.ID] = model
		datasets := dataExploreDatasets(model)
		state.Models = append(state.Models, uisignals.DataExploreModelSignal{
			ID: summary.ID, Title: firstNonEmpty(summary.Title, model.Title, summary.ID),
			Description: uisignals.Optional(firstNonEmpty(summary.Description, model.Description)), Datasets: datasets,
		})
	}
	if len(state.Models) == 0 {
		state.Result.Error = uisignals.Pointer("No semantic models are available in this workspace.")
		return state
	}
	modelID := uisignals.ValueOrZero(command.ModelID)
	selectedModelIndex := -1
	for index := range state.Models {
		if state.Models[index].ID == modelID {
			selectedModelIndex = index
			break
		}
	}
	if selectedModelIndex < 0 {
		selectedModelIndex = 0
		modelID = state.Models[0].ID
	}
	selectedModel := state.Models[selectedModelIndex]
	state.SelectedModel = &selectedModel
	state.Datasets = append([]uisignals.DataExploreDatasetSignal(nil), selectedModel.Datasets...)
	command.ModelID = uisignals.Optional(modelID)
	model := projections[modelID]
	if len(state.Datasets) == 0 {
		state.Command = command
		state.Result.Error = uisignals.Pointer("This semantic model has no explorable datasets.")
		return state
	}
	datasetID := uisignals.ValueOrZero(command.DatasetID)
	selectedDatasetIndex := -1
	for index := range state.Datasets {
		if state.Datasets[index].ID == datasetID {
			selectedDatasetIndex = index
			break
		}
	}
	if selectedDatasetIndex < 0 {
		selectedDatasetIndex = 0
		datasetID = state.Datasets[0].ID
	}
	selectedDataset := state.Datasets[selectedDatasetIndex]
	command.DatasetID = uisignals.Optional(datasetID)
	rebaseWarning := ""
	if resolvedDatasetID, changed := resolveDataExploreBase(model, datasetID, command); changed {
		rebaseWarning = fmt.Sprintf("Grain changed from %s to %s to support the selected fields.", dataExploreLabel(datasetID), dataExploreLabel(resolvedDatasetID))
		datasetID = resolvedDatasetID
		command.DatasetID = uisignals.Optional(datasetID)
		for index := range state.Datasets {
			if state.Datasets[index].ID == datasetID {
				selectedDataset = state.Datasets[index]
				break
			}
		}
	}
	state.SelectedDataset = &selectedDataset

	state.Fields = dataExploreFields(model, command, datasetID)
	validDimensions, validMeasures := dataExploreFieldSets(state.Fields)
	command.Dimensions = validDataExploreSelection(command.Dimensions, validDimensions)
	command.Measures = validDataExploreSelection(command.Measures, validMeasures)
	command.Filters = validDataExploreFilters(command.Filters, validDimensions)
	command.Sort = validDataExploreSort(command.Sort, command.Dimensions, command.Measures)
	state.Command = command
	if len(command.Dimensions) == 0 && len(command.Measures) == 0 && command.Time == nil {
		if rebaseWarning != "" {
			state.Result.Warnings = append(state.Result.Warnings, rebaseWarning)
		}
		return state
	}

	request := DataExploreRequest{
		WorkspaceID: workspaceID, ModelID: modelID, DatasetID: datasetID,
		Dimensions: append([]string(nil), command.Dimensions...), Measures: append([]string(nil), command.Measures...),
		Limit: int(command.Limit),
	}
	if command.Time != nil {
		request.Time = DataExploreTime{Field: command.Time.Field, Grain: command.Time.Grain, Alias: uisignals.ValueOrZero(command.Time.Alias)}
	}
	for _, filter := range command.Filters {
		request.Filters = append(request.Filters, DataExploreFilter{
			Field: filter.Field, Fact: uisignals.ValueOrZero(filter.Fact), Operator: filter.Operator,
			Values: append([]string(nil), filter.Values...),
		})
	}
	for _, sortSpec := range command.Sort {
		request.Sort = append(request.Sort, DataExploreSort{Field: sortSpec.Field, Direction: sortSpec.Direction})
	}
	executed, err := metrics.ExecuteDataExplore(ctx, request)
	if err != nil {
		state.Result.Error = uisignals.Pointer(err.Error())
		state.Result.Warnings = appendDataExploreWarning(state.Result.Warnings, rebaseWarning)
		return state
	}
	labels := dataExploreResultLabels(state.Fields, command)
	columns := make([]uisignals.DataPreviewColumnSignal, 0, len(executed.Columns))
	for _, column := range executed.Columns {
		columns = append(columns, uisignals.DataPreviewColumnSignal{Key: column, Label: firstNonEmpty(labels[column], column)})
	}
	state.Result = uisignals.DataExploreResultSignal{
		Columns: columns, Rows: executed.Rows, SQL: uisignals.Optional(executed.SQL), Plan: uisignals.Optional(executed.Plan),
		DurationMS: executed.DurationMS, RowsReturned: int64(executed.RowsReturned), Truncated: executed.Truncated,
		Warnings: appendDataExploreWarning(executed.Warnings, rebaseWarning), RequestSeq: command.RequestSeq,
	}
	return state
}

type navigationModel struct {
	ID          string
	Title       string
	Description string
}

func dataExploreNavigationModels(metrics Metrics) []navigationModel {
	models := make([]navigationModel, 0, len(metrics.Catalog().Models))
	for _, model := range metrics.Catalog().Models {
		models = append(models, navigationModel{ID: model.ID, Title: model.Title, Description: model.Description})
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(models[i].Title, models[i].ID)) < strings.ToLower(firstNonEmpty(models[j].Title, models[j].ID))
	})
	return models
}

func dataExploreDatasets(model DataExplorerModel) []uisignals.DataExploreDatasetSignal {
	ids := make([]string, 0, len(model.Tables))
	for id := range model.Tables {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]uisignals.DataExploreDatasetSignal, 0, len(ids))
	for _, id := range ids {
		table := model.Tables[id]
		fieldCount := len(table.Dimensions)
		for _, measure := range model.Measures {
			if !measure.Hidden && measure.Fact == id {
				fieldCount++
			}
		}
		out = append(out, uisignals.DataExploreDatasetSignal{
			ID: id, Title: dataExploreLabel(id), Description: uisignals.Optional(table.Description),
			Grain: uisignals.Optional(table.Grain), FieldCount: int64(fieldCount),
		})
	}
	return out
}

func dataExploreFields(model DataExplorerModel, command uisignals.DataExploreCommand, baseTable string) []uisignals.DataExploreFieldSignal {
	selectedDimensions := stringSet(command.Dimensions)
	selectedMeasures := stringSet(command.Measures)
	tables := make([]string, 0, len(model.Tables))
	for table := range model.Tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	out := []uisignals.DataExploreFieldSignal{}
	for _, tableID := range tables {
		table := model.Tables[tableID]
		fields := make([]string, 0, len(table.Dimensions))
		for field := range table.Dimensions {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, name := range fields {
			dimension := table.Dimensions[name]
			id := tableID + "." + name
			compatible, reason, path := dataExploreFieldCompatibility(model, baseTable, tableID)
			rebaseDatasetID := ""
			if !compatible {
				rebaseDatasetID = dataExploreFieldRebase(model, command, baseTable, id, "dimension")
				if rebaseDatasetID != "" {
					reason = fmt.Sprintf("Select %s and change grain from %s to %s.", firstNonEmpty(dimension.Label, dataExploreLabel(name)), dataExploreLabel(baseTable), dataExploreLabel(rebaseDatasetID))
				}
			}
			out = append(out, uisignals.DataExploreFieldSignal{
				ID: id, Label: firstNonEmpty(dimension.Label, dataExploreLabel(name)), Kind: "dimension", ModelTable: tableID,
				Description: uisignals.Optional(dimension.Description), Type: uisignals.Optional(dimension.Type),
				Selected: selectedDimensions[id], Compatible: compatible,
				CompatibilityReason: uisignals.Optional(reason), RelationshipPath: uisignals.OptionalSlice(path),
				RebaseDatasetID: uisignals.Optional(rebaseDatasetID),
			})
		}
	}
	measureIDs := make([]string, 0, len(model.Measures))
	for id, measure := range model.Measures {
		if !measure.Hidden {
			measureIDs = append(measureIDs, id)
		}
	}
	sort.Strings(measureIDs)
	for _, id := range measureIDs {
		measure := model.Measures[id]
		compatible := measure.Fact == baseTable
		reason := ""
		rebaseDatasetID := ""
		if !compatible {
			rebaseDatasetID = dataExploreFieldRebase(model, command, baseTable, id, "measure")
			if rebaseDatasetID != "" {
				reason = fmt.Sprintf("Select %s and change grain from %s to %s.", firstNonEmpty(measure.Label, dataExploreLabel(id)), dataExploreLabel(baseTable), dataExploreLabel(rebaseDatasetID))
			} else {
				reason = fmt.Sprintf("Measure belongs to %s and cannot be combined safely with the selected fields.", dataExploreLabel(measure.Fact))
			}
		}
		out = append(out, uisignals.DataExploreFieldSignal{
			ID: id, Label: firstNonEmpty(measure.Label, dataExploreLabel(id)), Kind: "measure", ModelTable: measure.Fact,
			Description: uisignals.Optional(measure.Description), Fact: uisignals.Optional(measure.Fact),
			Type: uisignals.Optional(measure.Type), Selected: selectedMeasures[id], Compatible: compatible,
			CompatibilityReason: uisignals.Optional(reason), RebaseDatasetID: uisignals.Optional(rebaseDatasetID),
		})
	}
	return out
}

func resolveDataExploreBase(model DataExplorerModel, currentBase string, command uisignals.DataExploreCommand) (string, bool) {
	currentBase = strings.TrimSpace(currentBase)
	targets, measureFacts := dataExploreCommandTargets(model, command)
	if dataExploreBaseScore(model, currentBase, targets, measureFacts) >= 0 {
		return currentBase, false
	}
	bestBase, bestScore, tied := "", -1, false
	for candidate := range model.Tables {
		score := dataExploreBaseScore(model, candidate, targets, measureFacts)
		if score < 0 {
			continue
		}
		switch {
		case bestScore < 0 || score < bestScore:
			bestBase, bestScore, tied = candidate, score, false
		case score == bestScore:
			tied = true
		}
	}
	if bestBase == "" || tied || bestBase == currentBase {
		return currentBase, false
	}
	return bestBase, true
}

func dataExploreCommandTargets(model DataExplorerModel, command uisignals.DataExploreCommand) ([]string, []string) {
	targetSet := map[string]bool{}
	measureSet := map[string]bool{}
	addDimension := func(id string) {
		table, field := keyParts(strings.TrimSpace(id))
		if dimensions, ok := model.Tables[table]; ok {
			if _, ok := dimensions.Dimensions[field]; ok {
				targetSet[table] = true
			}
		}
	}
	for _, id := range command.Dimensions {
		addDimension(id)
	}
	for _, filter := range command.Filters {
		addDimension(filter.Field)
	}
	if command.Time != nil {
		addDimension(command.Time.Field)
	}
	for _, id := range command.Measures {
		if measure, ok := model.Measures[strings.TrimSpace(id)]; ok && !measure.Hidden {
			measureSet[measure.Fact] = true
		}
	}
	targets := make([]string, 0, len(targetSet))
	for table := range targetSet {
		targets = append(targets, table)
	}
	measureFacts := make([]string, 0, len(measureSet))
	for fact := range measureSet {
		measureFacts = append(measureFacts, fact)
	}
	sort.Strings(targets)
	sort.Strings(measureFacts)
	return targets, measureFacts
}

func dataExploreBaseScore(model DataExplorerModel, candidate string, targets, measureFacts []string) int {
	if _, ok := model.Tables[candidate]; !ok {
		return -1
	}
	for _, fact := range measureFacts {
		if fact != candidate {
			return -1
		}
	}
	score := 0
	for _, target := range targets {
		if target == candidate {
			continue
		}
		paths := dataExploreSafeRelationshipPaths(model, candidate, target)
		if len(paths) != 1 {
			return -1
		}
		score += len(paths[0])
	}
	return score
}

func dataExploreFieldRebase(model DataExplorerModel, command uisignals.DataExploreCommand, currentBase, fieldID, kind string) string {
	hypothetical := command
	if kind == "measure" {
		hypothetical.Measures = appendUniqueDataExploreValue(hypothetical.Measures, fieldID)
	} else {
		hypothetical.Dimensions = appendUniqueDataExploreValue(hypothetical.Dimensions, fieldID)
	}
	base, changed := resolveDataExploreBase(model, currentBase, hypothetical)
	if !changed {
		return ""
	}
	return base
}

func appendUniqueDataExploreValue(values []string, value string) []string {
	out := append([]string(nil), values...)
	for _, current := range out {
		if current == value {
			return out
		}
	}
	return append(out, value)
}

func appendDataExploreWarning(warnings []string, warning string) []string {
	out := append([]string(nil), warnings...)
	if strings.TrimSpace(warning) != "" {
		out = append(out, warning)
	}
	return out
}

func dataExploreObjectForDataset(objects []uisignals.DataExplorerObjectSignal, command uisignals.DataExploreCommand) *uisignals.DataExplorerObjectSignal {
	workspaceID := uisignals.ValueOrZero(command.WorkspaceID)
	modelID := uisignals.ValueOrZero(command.ModelID)
	datasetID := uisignals.ValueOrZero(command.DatasetID)
	for index := range objects {
		object := &objects[index]
		if object.WorkspaceID == workspaceID && object.Layer == "model_table" && uisignals.ValueOrZero(object.ModelID) == modelID && uisignals.ValueOrZero(object.Table) == datasetID {
			return object
		}
	}
	return nil
}

func dataExploreFieldCompatibility(model DataExplorerModel, baseTable, targetTable string) (bool, string, []string) {
	baseTable = strings.TrimSpace(baseTable)
	targetTable = strings.TrimSpace(targetTable)
	if baseTable == targetTable {
		return true, "", []string{}
	}
	paths := dataExploreSafeRelationshipPaths(model, baseTable, targetTable)
	switch len(paths) {
	case 0:
		return false, fmt.Sprintf("Not available from %s because no grain-preserving relationship path reaches %s.", dataExploreLabel(baseTable), dataExploreLabel(targetTable)), nil
	case 1:
		return true, "", paths[0]
	default:
		return false, fmt.Sprintf("Not available from %s because more than one relationship path reaches %s.", dataExploreLabel(baseTable), dataExploreLabel(targetTable)), nil
	}
}

func dataExploreSafeRelationshipPaths(model DataExplorerModel, baseTable, targetTable string) [][]string {
	type candidate struct {
		table   string
		path    []string
		visited map[string]bool
	}
	matches := [][]string{}
	var walk func(candidate)
	walk = func(current candidate) {
		if len(matches) > 1 {
			return
		}
		edges := dataExploreSafeEdgesFrom(model, current.table)
		for _, edge := range edges {
			if current.visited[edge.table] {
				continue
			}
			path := append(append([]string{}, current.path...), edge.relationship.ID)
			if edge.table == targetTable {
				matches = append(matches, path)
				continue
			}
			visited := map[string]bool{}
			for table, value := range current.visited {
				visited[table] = value
			}
			visited[edge.table] = true
			walk(candidate{table: edge.table, path: path, visited: visited})
		}
	}
	walk(candidate{table: baseTable, visited: map[string]bool{baseTable: true}})
	return matches
}

type dataExploreRelationshipEdge struct {
	table        string
	relationship DataExplorerRelationship
}

func dataExploreSafeEdgesFrom(model DataExplorerModel, table string) []dataExploreRelationshipEdge {
	edges := []dataExploreRelationshipEdge{}
	for _, relationship := range model.Relationships {
		fromTable, _ := keyParts(relationship.From)
		toTable, _ := keyParts(relationship.To)
		if fromTable == table && (relationship.Cardinality == "many_to_one" || relationship.Cardinality == "one_to_one") {
			edges = append(edges, dataExploreRelationshipEdge{table: toTable, relationship: relationship})
		} else if toTable == table && relationship.Cardinality == "one_to_one" {
			edges = append(edges, dataExploreRelationshipEdge{table: fromTable, relationship: relationship})
		}
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].table != edges[j].table {
			return edges[i].table < edges[j].table
		}
		return edges[i].relationship.ID < edges[j].relationship.ID
	})
	return edges
}

func dataExploreFieldSets(fields []uisignals.DataExploreFieldSignal) (map[string]bool, map[string]bool) {
	dimensions, measures := map[string]bool{}, map[string]bool{}
	for _, field := range fields {
		if !field.Compatible {
			continue
		}
		if field.Kind == "measure" {
			measures[field.ID] = true
		} else {
			dimensions[field.ID] = true
		}
	}
	return dimensions, measures
}

func validDataExploreSelection(values []string, valid map[string]bool) []string {
	out := []string{}
	for _, value := range uniqueDataExploreValues(values) {
		if valid[value] {
			out = append(out, value)
		}
	}
	return out
}

func validDataExploreFilters(filters []uisignals.DataExploreFilterSignal, dimensions map[string]bool) []uisignals.DataExploreFilterSignal {
	allowedOperators := map[string]bool{
		"equals": true, "in": true, "contains": true, "not_contains": true, "starts_with": true,
		"greater_than_or_equal": true, "less_than": true, "is_null": true, "is_not_null": true,
	}
	out := []uisignals.DataExploreFilterSignal{}
	for _, filter := range filters {
		filter.Field = strings.TrimSpace(filter.Field)
		filter.Operator = strings.ToLower(strings.TrimSpace(filter.Operator))
		if !dimensions[filter.Field] || !allowedOperators[filter.Operator] {
			continue
		}
		filter.Values = uniqueDataExploreValues(filter.Values)
		if filter.Operator != "is_null" && filter.Operator != "is_not_null" && len(filter.Values) == 0 {
			continue
		}
		out = append(out, filter)
	}
	return out
}

func validDataExploreSort(sortSpec []uisignals.DataExploreSortSignal, dimensions, measures []string) []uisignals.DataExploreSortSignal {
	selected := stringSet(append(append([]string(nil), dimensions...), measures...))
	out := []uisignals.DataExploreSortSignal{}
	for _, sort := range sortSpec {
		sort.Field = strings.TrimSpace(sort.Field)
		sort.Direction = strings.ToLower(strings.TrimSpace(sort.Direction))
		if !selected[sort.Field] || (sort.Direction != "asc" && sort.Direction != "desc") {
			continue
		}
		out = append(out, sort)
	}
	return out
}

func dataExploreResultLabels(fields []uisignals.DataExploreFieldSignal, command uisignals.DataExploreCommand) map[string]string {
	labels := map[string]string{}
	selected := append(append([]string(nil), command.Dimensions...), command.Measures...)
	fieldLabels := map[string]string{}
	lastCounts := map[string]int{}
	for _, id := range selected {
		last := id
		if index := strings.LastIndex(id, "."); index >= 0 {
			last = id[index+1:]
		}
		lastCounts[last]++
	}
	for _, field := range fields {
		fieldLabels[field.ID] = field.Label
	}
	for _, id := range selected {
		last, table := id, ""
		if index := strings.LastIndex(id, "."); index >= 0 {
			table, last = id[:index], id[index+1:]
		}
		alias := last
		if lastCounts[last] > 1 && table != "" {
			alias = table + "__" + last
		}
		labels[alias] = firstNonEmpty(fieldLabels[id], dataExploreLabel(last))
	}
	return labels
}

func dataExploreLabel(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "_", " ")
	if value == "" {
		return "-"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func dataExplorerObjects(workspaceID, workspaceTitle string, metrics Metrics, assets []workspace.AssetView, edges []workspace.AssetEdgeView) ([]uisignals.DataExplorerObjectSignal, []string) {
	out := []uisignals.DataExplorerObjectSignal{}
	warnings := []string{}
	for _, asset := range assets {
		modelID, name := keyParts(asset.Key)
		switch asset.Type {
		case string(workspace.AssetTypeModelTable):
			model, _ := metrics.DataExplorerModel(modelID)
			table := DataExplorerTable{}
			if model.Tables != nil {
				table = model.Tables[name]
			}
			columns := dataColumnsFromTable(table)
			out = append(out, uisignals.DataExplorerObjectSignal{
				Key:            dataObjectKey("model_table", asset.ID),
				WorkspaceID:    workspaceID,
				WorkspaceTitle: uisignals.Optional(workspaceTitle),
				AssetID:        uisignals.Optional(asset.ID),
				Layer:          "model_table",
				ModelID:        uisignals.Optional(modelID),
				Table:          uisignals.Optional(name),
				Title:          asset.Title,
				Description:    uisignals.Optional(firstNonEmpty(asset.Description, table.Description)),
				DetailHref:     uisignals.Optional(assetnav.CanonicalAssetSectionHref(workspaceID, asset, "details", edges)),
				Grain:          uisignals.Optional(table.Grain),
				ColumnCount:    int64(len(columns)),
				RowCountLabel:  uisignals.Pointer("Unknown"),
				Columns:        uisignals.OptionalSlice(columns),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Layer != out[j].Layer {
			return dataLayerRank(out[i].Layer) < dataLayerRank(out[j].Layer)
		}
		if uisignals.ValueOrZero(out[i].ModelID) != uisignals.ValueOrZero(out[j].ModelID) {
			return uisignals.ValueOrZero(out[i].ModelID) < uisignals.ValueOrZero(out[j].ModelID)
		}
		return out[i].Title < out[j].Title
	})
	return out, warnings
}

func dataExplorerSourceForAsset(metrics Metrics, sourceKey string) (string, string, DataExplorerSource, bool) {
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKey == "" || metrics == nil {
		return "", "", DataExplorerSource{}, false
	}
	for _, modelSummary := range metrics.Catalog().Models {
		model, ok := metrics.DataExplorerModel(modelSummary.ID)
		if !ok {
			continue
		}
		source, ok := dataExplorerSourceInModel(model, sourceKey)
		if ok {
			return modelSummary.ID, sourceKey, source, true
		}
	}
	modelID, name := keyParts(sourceKey)
	if modelID == "" || name == "" {
		return "", "", DataExplorerSource{}, false
	}
	model, ok := metrics.DataExplorerModel(modelID)
	if !ok {
		return "", "", DataExplorerSource{}, false
	}
	source, ok := model.Sources[name]
	if !ok {
		return "", "", DataExplorerSource{}, false
	}
	return modelID, name, source, true
}

func dataExplorerSourceInModel(model DataExplorerModel, sourceKey string) (DataExplorerSource, bool) {
	if source, ok := model.Sources[sourceKey]; ok {
		return source, true
	}
	if source, ok := model.Sources[dataExplorerLocalSourceName(sourceKey)]; ok {
		return source, true
	}
	return DataExplorerSource{}, false
}

func dataExplorerLocalSourceName(sourceID string) string {
	var builder strings.Builder
	for index, char := range sourceID {
		valid := char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9'
		if valid {
			builder.WriteRune(char)
			continue
		}
		builder.WriteByte('_')
	}
	out := builder.String()
	if out == "" || out[0] >= '0' && out[0] <= '9' {
		out = "source_" + out
	}
	return out
}

func dataExplorerObjectsFromMetrics(workspaceID, workspaceTitle string, metrics Metrics) []uisignals.DataExplorerObjectSignal {
	out := []uisignals.DataExplorerObjectSignal{}
	for _, modelSummary := range metrics.Catalog().Models {
		model, ok := metrics.DataExplorerModel(modelSummary.ID)
		if !ok {
			continue
		}
		tableNames := make([]string, 0, len(model.Tables))
		for name := range model.Tables {
			tableNames = append(tableNames, name)
		}
		sort.Strings(tableNames)
		for _, name := range tableNames {
			table := model.Tables[name]
			assetID := "model_table:" + modelSummary.ID + "." + name
			columns := dataColumnsFromTable(table)
			out = append(out, uisignals.DataExplorerObjectSignal{
				Key:            dataObjectKey("model_table", assetID),
				WorkspaceID:    workspaceID,
				WorkspaceTitle: uisignals.Optional(workspaceTitle),
				AssetID:        uisignals.Optional(assetID),
				Layer:          "model_table",
				ModelID:        uisignals.Optional(modelSummary.ID),
				Table:          uisignals.Optional(name),
				Title:          name,
				Description:    uisignals.Optional(table.Description),
				Grain:          uisignals.Optional(table.Grain),
				ColumnCount:    int64(len(columns)),
				RowCountLabel:  uisignals.Pointer("Unknown"),
				Columns:        uisignals.OptionalSlice(columns),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Layer != out[j].Layer {
			return dataLayerRank(out[i].Layer) < dataLayerRank(out[j].Layer)
		}
		if uisignals.ValueOrZero(out[i].ModelID) != uisignals.ValueOrZero(out[j].ModelID) {
			return uisignals.ValueOrZero(out[i].ModelID) < uisignals.ValueOrZero(out[j].ModelID)
		}
		return out[i].Title < out[j].Title
	})
	return out
}

func dataExplorerWorkspaceSignals(workspaces []workspace.WorkspaceView, objects []uisignals.DataExplorerObjectSignal, activeWorkspaceID string) []uisignals.DataExplorerWorkspaceSignal {
	counts := map[string]int{}
	for _, object := range objects {
		counts[object.WorkspaceID]++
	}
	out := make([]uisignals.DataExplorerWorkspaceSignal, 0, len(workspaces))
	for _, workspace := range workspaces {
		out = append(out, uisignals.DataExplorerWorkspaceSignal{
			ID:          workspace.ID,
			Title:       firstNonEmpty(workspace.Title, workspace.ID),
			Href:        "/data?workspace=" + url.QueryEscape(workspace.ID),
			ObjectCount: int64(counts[workspace.ID]),
			Active:      workspace.ID == activeWorkspaceID,
		})
	}
	return out
}

func selectGlobalDataExplorerObject(objects []uisignals.DataExplorerObjectSignal, workspaceID, key string) (*uisignals.DataExplorerObjectSignal, []string) {
	workspaceID = strings.TrimSpace(workspaceID)
	key = strings.TrimSpace(key)
	warnings := []string{}
	if workspaceID != "" && key != "" {
		for i := range objects {
			if objects[i].WorkspaceID == workspaceID && dataExplorerObjectMatchesKey(objects[i], key) {
				return &objects[i], warnings
			}
		}
		warnings = append(warnings, fmt.Sprintf("Data object %q was not found in workspace %q.", key, workspaceID))
	}
	if workspaceID != "" {
		for i := range objects {
			if objects[i].WorkspaceID == workspaceID {
				return &objects[i], warnings
			}
		}
		warnings = append(warnings, fmt.Sprintf("Workspace %q has no inspectable data objects.", workspaceID))
	}
	if key != "" {
		for i := range objects {
			if dataExplorerObjectMatchesKey(objects[i], key) {
				return &objects[i], warnings
			}
		}
		warnings = append(warnings, fmt.Sprintf("Data object %q was not found.", key))
	}
	if len(objects) == 0 {
		return nil, warnings
	}
	return &objects[0], warnings
}

func dataExplorerObjectMatchesKey(object uisignals.DataExplorerObjectSignal, key string) bool {
	if object.Key == key || uisignals.ValueOrZero(object.AssetID) == key {
		return true
	}
	if object.Layer == "source" && dataObjectKey("source", uisignals.ValueOrZero(object.AssetID)) == key {
		return true
	}
	return false
}

func (h Handler) dataPreview(ctx context.Context, metrics Metrics, object uisignals.DataExplorerObjectSignal, command uisignals.DataExplorerCommand, current *uisignals.DataExplorerSignal) uisignals.DataPreviewSignal {
	preview := uisignals.DataPreviewSignal{
		Columns:       uisignals.ValueOrZero(object.Columns),
		TotalRows:     0,
		AvailableRows: 0,
		ChunkSize:     command.Count,
		RowHeight:     dataExplorerRowHeight,
		ResetVersion:  command.ResetVersion,
		Blocks:        emptyDataPreviewBlocks(int(command.Count), command.Sort, int(command.ResetVersion)),
		TotalRowLabel: object.RowCountLabel,
		Sort:          command.Sort,
	}
	if object.Layer == "source" {
		return preview
	}
	if totals, ok := reusableDataPreviewTotals(current, object, command); ok {
		preview.TotalRows = totals.TotalRows
		preview.AvailableRows = totals.AvailableRows
		preview.TotalRowLabel = totals.TotalRowLabel
	} else {
		total, err := h.countDataPreview(ctx, metrics, object)
		if err != nil {
			preview.Error = uisignals.Pointer(err.Error())
			return preview
		}
		preview.TotalRowLabel = uisignals.Optional(total)
		preview.TotalRows = int64(dataPreviewTotalRows(total))
		preview.AvailableRows = preview.TotalRows
	}
	if preview.TotalRows == 0 && uisignals.ValueOrZero(preview.TotalRowLabel) != "0" {
		preview.TotalRows = command.Start + command.Count*int64(len(dataExplorerBlockIDs))
		preview.AvailableRows = preview.TotalRows
	}
	blockStarts := []int{int(command.Start)}
	blockIDs := []string{uisignals.ValueOrZero(command.Block)}
	if uisignals.ValueOrZero(command.Block) == "all" {
		blockStarts = dataPreviewBlockStarts(int(command.Start), int(command.Count), int(preview.AvailableRows))
		blockIDs = dataExplorerBlockIDs[:len(blockStarts)]
	}
	for index, blockID := range blockIDs {
		start := blockStarts[index]
		rows, sqlText, err := h.previewRows(ctx, metrics, object, command, start, int(command.Count))
		if sqlText != "" {
			preview.SQL = uisignals.Optional(sqlText)
		}
		if err != nil {
			preview.Error = uisignals.Pointer(err.Error())
			return preview
		}
		if preview.AvailableRows == 0 && len(rows) > 0 {
			preview.AvailableRows = int64(start + len(rows))
			preview.TotalRows = preview.AvailableRows
		}
		preview.Blocks[blockID] = uisignals.DataPreviewBlockSignal{
			Start:        int64(start),
			RequestSeq:   command.RequestSeq,
			ResetVersion: command.ResetVersion,
			Sort:         command.Sort,
			Rows:         rows,
		}
	}
	return preview
}

func reusableDataPreviewTotals(current *uisignals.DataExplorerSignal, object uisignals.DataExplorerObjectSignal, command uisignals.DataExplorerCommand) (uisignals.DataPreviewSignal, bool) {
	if current == nil || current.SelectedObject == nil {
		return uisignals.DataPreviewSignal{}, false
	}
	if current.SelectedObject.WorkspaceID != object.WorkspaceID || current.SelectedObject.Key != object.Key {
		return uisignals.DataPreviewSignal{}, false
	}
	if current.Preview.ResetVersion != command.ResetVersion || current.Preview.ChunkSize != command.Count || !dataPreviewSortEqual(current.Preview.Sort, command.Sort) {
		return uisignals.DataPreviewSignal{}, false
	}
	if current.Preview.TotalRows <= 0 && current.Preview.AvailableRows <= 0 && dataPreviewTotalRows(uisignals.ValueOrZero(current.Preview.TotalRowLabel)) <= 0 {
		return uisignals.DataPreviewSignal{}, false
	}
	return current.Preview, true
}

func dataPreviewSortEqual(left, right uisignals.DataPreviewSortSignal) bool {
	return uisignals.ValueOrZero(left.Column) == uisignals.ValueOrZero(right.Column) &&
		uisignals.ValueOrZero(left.Direction) == uisignals.ValueOrZero(right.Direction)
}

func emptyDataPreviewBlocks(count int, sort uisignals.DataPreviewSortSignal, resetVersion int) map[string]uisignals.DataPreviewBlockSignal {
	if count <= 0 {
		count = dataExplorerDefaultLimit
	}
	return map[string]uisignals.DataPreviewBlockSignal{
		"a": {Start: 0, ResetVersion: int64(resetVersion), Sort: sort, Rows: []map[string]any{}},
		"b": {Start: int64(count), ResetVersion: int64(resetVersion), Sort: sort, Rows: []map[string]any{}},
		"c": {Start: int64(count * 2), ResetVersion: int64(resetVersion), Sort: sort, Rows: []map[string]any{}},
	}
}

func EmptyDataPreviewBlocks(count int, sort uisignals.DataPreviewSortSignal, resetVersion int) map[string]uisignals.DataPreviewBlockSignal {
	return emptyDataPreviewBlocks(count, sort, resetVersion)
}

func dataPreviewBlockStarts(start, count, availableRows int) []int {
	if count <= 0 {
		count = dataExplorerDefaultLimit
	}
	current := max(0, (start/count)*count)
	starts := []int{}
	if current <= 0 {
		starts = []int{0, count, count * 2}
	} else {
		starts = []int{max(0, current-count), current, current + count}
	}
	out := []int{}
	for _, candidate := range starts {
		if candidate < availableRows {
			out = append(out, candidate)
		}
	}
	return out
}

func dataPreviewTotalRows(label string) int {
	normalized := strings.ReplaceAll(strings.TrimSpace(label), ",", "")
	total, err := strconv.Atoi(normalized)
	if err != nil || total < 0 {
		return 0
	}
	return total
}

func dataPreviewCanceled(preview uisignals.DataPreviewSignal) bool {
	message := strings.ToLower(uisignals.ValueOrZero(preview.Error))
	return strings.Contains(message, "context canceled") ||
		strings.Contains(message, "context cancelled") ||
		strings.Contains(message, "interrupt")
}

func (h Handler) countDataPreview(ctx context.Context, metrics Metrics, object uisignals.DataExplorerObjectSignal) (string, error) {
	switch object.Layer {
	case "model_table":
		result, err := metrics.ExecuteDataPreview(ctx, dataPreviewQuery(object, uisignals.DataExplorerCommand{}, 0, 1, true))
		if err != nil {
			return "Unknown", err
		}
		if !result.TotalRowsKnown {
			return "Unknown", nil
		}
		return strconv.Itoa(result.TotalRows), nil
	case "semantic_view":
		return firstNonEmpty(uisignals.ValueOrZero(object.RowCountLabel), "Unknown"), nil
	default:
		return "Unknown", fmt.Errorf("unsupported data layer %q", object.Layer)
	}
}

func (h Handler) previewRows(ctx context.Context, metrics Metrics, object uisignals.DataExplorerObjectSignal, command uisignals.DataExplorerCommand, start, count int) ([]map[string]any, string, error) {
	result, err := metrics.ExecuteDataPreview(ctx, dataPreviewQuery(object, command, start, count, false))
	if err != nil {
		return nil, "", err
	}
	return result.Rows, result.SQL, nil
}

func dataPreviewColumnKeys(columns []uisignals.DataPreviewColumnSignal) []string {
	keys := make([]string, 0, len(columns))
	for _, column := range columns {
		if strings.TrimSpace(column.Key) != "" {
			keys = append(keys, column.Key)
		}
	}
	return keys
}

func dataPreviewQuery(object uisignals.DataExplorerObjectSignal, command uisignals.DataExplorerCommand, start, count int, includeTotal bool) DataPreviewRequest {
	return DataPreviewRequest{
		WorkspaceID:  object.WorkspaceID,
		ObjectKey:    object.Key,
		Layer:        object.Layer,
		ModelID:      uisignals.ValueOrZero(object.ModelID),
		Table:        uisignals.ValueOrZero(object.Table),
		Columns:      dataPreviewColumnKeys(uisignals.ValueOrZero(object.Columns)),
		SortColumn:   uisignals.ValueOrZero(command.Sort.Column),
		Direction:    uisignals.ValueOrZero(command.Sort.Direction),
		Offset:       start,
		Limit:        count,
		IncludeTotal: includeTotal,
	}
}

func dataPreviewSortForColumns(columns []uisignals.DataPreviewColumnSignal, sort uisignals.DataPreviewSortSignal) uisignals.DataPreviewSortSignal {
	if uisignals.ValueOrZero(sort.Column) == "" || !dataColumnExists(columns, uisignals.ValueOrZero(sort.Column)) {
		return uisignals.DataPreviewSortSignal{}
	}
	if uisignals.ValueOrZero(sort.Direction) != "asc" && uisignals.ValueOrZero(sort.Direction) != "desc" {
		return uisignals.DataPreviewSortSignal{}
	}
	return sort
}

func dataColumnsFromSource(source DataExplorerSource) []uisignals.DataPreviewColumnSignal {
	if len(source.Columns) > 0 {
		return dataColumnsFromSchema(source.Columns)
	}
	names := make([]string, 0, len(source.Fields))
	for name := range source.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]uisignals.DataPreviewColumnSignal, 0, len(names))
	for _, name := range names {
		field := source.Fields[name]
		out = append(out, uisignals.DataPreviewColumnSignal{Key: name, Label: name, Type: uisignals.Optional(field.Type), Description: uisignals.Optional(field.Description)})
	}
	return out
}

func dataColumnsFromTable(table DataExplorerTable) []uisignals.DataPreviewColumnSignal {
	if len(table.Schema) > 0 {
		return dataColumnsFromSchema(table.Schema)
	}
	names := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]uisignals.DataPreviewColumnSignal, 0, len(names))
	for _, name := range names {
		column := table.Columns[name]
		out = append(out, uisignals.DataPreviewColumnSignal{Key: name, Label: firstNonEmpty(column.Name, name), Type: uisignals.Optional(column.Type), Description: uisignals.Optional(column.Description)})
	}
	return out
}

func dataColumnsFromSchema(columns []DataExplorerColumn) []uisignals.DataPreviewColumnSignal {
	out := make([]uisignals.DataPreviewColumnSignal, 0, len(columns))
	sorted := append([]DataExplorerColumn{}, columns...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Ordinal < sorted[j].Ordinal
	})
	for _, column := range sorted {
		out = append(out, uisignals.DataPreviewColumnSignal{
			Key: column.Name, Label: column.Name, Type: uisignals.Optional(column.PhysicalType),
			Description: uisignals.Optional(column.Comment), Nullable: column.Nullable,
			DefaultValue: uisignals.Optional(column.Default), PrimaryKey: uisignals.Optional(column.PrimaryKey),
		})
	}
	return out
}

func dataColumnExists(columns []uisignals.DataPreviewColumnSignal, key string) bool {
	for _, column := range columns {
		if column.Key == key {
			return true
		}
	}
	return false
}

func dataObjectKey(layer, id string) string {
	return layer + ":" + id
}

func dataLayerRank(layer string) int {
	switch layer {
	case "source":
		return 0
	case "model_table":
		return 1
	case "semantic_view":
		return 2
	default:
		return 10
	}
}

func keyParts(key string) (string, string) {
	left, right, ok := strings.Cut(key, ".")
	if !ok {
		return "", key
	}
	return left, right
}
