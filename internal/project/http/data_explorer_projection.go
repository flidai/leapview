package http

// This file contains the browser-facing Data Explorer projection.  The
// serving graph decides which resources are visible; the compiled project
// manifest supplies the semantic model and table metadata that is deliberately
// not duplicated in the graph's project.graph.v1 payload.

import (
	"net/url"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectview "github.com/flidai/leapview/internal/project"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// DataExplorerProjection is the complete catalog/semantic payload needed to
// bootstrap the Data Explorer browser.  It is intentionally detached from the
// serving graph and manifest inputs, so a caller can safely retain the result
// for one response.
type DataExplorerProjection struct {
	Objects         []projectsignals.DataExplorerObjectSignal
	Models          []projectsignals.DataExploreModelSignal
	SelectedModel   *projectsignals.DataExploreModelSignal
	Datasets        []projectsignals.DataExploreDatasetSignal
	SelectedDataset *projectsignals.DataExploreDatasetSignal
	Fields          []projectsignals.DataExploreFieldSignal
	Command         projectsignals.DataExploreCommand
	Warnings        []string
}

// BuildDataExplorerProjection projects authorized serving assets together
// with one coherent active-generation manifest and its activation-owned
// compiled semantic bindings. Asset visibility is authoritative for every
// output: manifest entries that do not have a visible serving asset are never
// exposed to the browser.
func BuildDataExplorerProjection(assets []projectview.DevelopAssetView, project projectmanifest.Project, command projectsignals.DataExploreCommand, compiledModels map[string]*semanticquery.CompiledModel) DataExplorerProjection {
	state := dataExploreStateFromSpec(command.Spec)
	visible := make(map[string]projectview.DevelopAssetView, len(assets))
	for _, asset := range assets {
		if strings.TrimSpace(asset.ID) == "" {
			continue
		}
		visible[asset.ID] = asset
	}

	semanticIDs := make([]string, 0, len(project.SemanticModels))
	for id := range project.SemanticModels {
		if asset, ok := visible[id]; ok && asset.Type == string(projectview.AssetTypeSemanticModel) {
			semanticIDs = append(semanticIDs, id)
		}
	}
	sort.Strings(semanticIDs)

	models := make([]projectsignals.DataExploreModelSignal, 0, len(semanticIDs))
	modelByID := make(map[string]*semanticmodel.Model, len(semanticIDs))
	compiledByID := make(map[string]*semanticquery.CompiledModel, len(semanticIDs))
	bindingUnavailable := false
	for _, id := range semanticIDs {
		model := project.SemanticModels[id]
		if model == nil {
			continue
		}
		modelByID[id] = model
		asset := visible[id]
		compiled, ok := compiledModels[id]
		if !ok || compiled == nil || len(compiled.DatasetNames()) == 0 {
			bindingUnavailable = true
			compiled = &semanticquery.CompiledModel{}
		}
		compiledByID[id] = compiled
		models = append(models, projectsignals.DataExploreModelSignal{
			ID:          id,
			Title:       firstExplorerNonEmpty(model.Title, model.Name, asset.Title, id),
			Description: projectsignals.Optional(firstExplorerNonEmpty(model.Description, asset.Description)),
			Datasets:    explorerDatasets(model, compiled),
		})
	}

	// A model table is keyed by its canonical model resource ID in the
	// manifest. Semantic model table names remain authored names, so resolve
	// them through NameIndex before associating browser objects with a model.
	semanticForTable := make(map[string][]string)
	for _, semanticID := range semanticIDs {
		model := modelByID[semanticID]
		compiled := compiledByID[semanticID]
		for _, datasetTable := range explorerDatasetTableMap(model, compiled) {
			tableName := datasetTable.ModelName
			modelID := project.NameIndex.Models[tableName]
			if modelID == "" {
				modelID = explorerModelIDByName(visible, tableName)
			}
			if modelID == "" {
				continue
			}
			semanticForTable[modelID] = append(semanticForTable[modelID], semanticID)
		}
	}
	for id := range semanticForTable {
		sort.Strings(semanticForTable[id])
	}

	objects := make([]projectsignals.DataExplorerObjectSignal, 0)
	for _, asset := range sortedExplorerAssets(visible) {
		switch asset.Type {
		case string(projectview.AssetTypeSource):
			// Sources are retained in the unified catalog. The Lit browser hides
			// source groups while still allowing search and agent references to
			// inspect the canonical source resource.
			source := project.Sources[asset.ID]
			columns := explorerSourceColumns(source)
			objects = append(objects, projectsignals.DataExplorerObjectSignal{
				Key:           "source:" + asset.ID,
				AssetID:       projectsignals.Optional(asset.ID),
				ResourceID:    asset.ID,
				Layer:         "source",
				Title:         firstExplorerNonEmpty(asset.Title, asset.Key, asset.ID),
				Description:   projectsignals.Optional(firstExplorerNonEmpty(asset.Description, source.Description)),
				DetailHref:    projectsignals.Optional(explorerAssetDetailsHref(asset, "details")),
				ColumnCount:   int64(len(columns)),
				RowCountLabel: projectsignals.Pointer("Preview unavailable"),
				Columns:       projectsignals.OptionalSlice(columns),
			})
		case string(projectview.AssetTypeModelTable):
			table, ok := project.Models[asset.ID]
			if !ok {
				// A malformed or stale manifest must not make the entire catalog
				// unavailable. Keep the authorized object with graph metadata.
				table = explorerTableByName(project.Models, asset.Key)
			}
			columns := explorerTableColumns(table)
			modelIDs := semanticForTable[asset.ID]
			if len(modelIDs) == 0 {
				objects = append(objects, explorerModelTableObject(asset, table, columns, ""))
				continue
			}
			// One object per semantic model keeps field compatibility scoped to
			// the selected model while preserving a stable canonical asset ID.
			for _, semanticID := range modelIDs {
				objects = append(objects, explorerModelTableObject(asset, table, columns, semanticID))
			}
		}
	}
	sortExplorerObjects(objects)

	selectedModelID := strings.TrimSpace(projectsignals.ValueOrZero(state.ModelID))
	selectedModelIndex := -1
	for index := range models {
		if models[index].ID == selectedModelID {
			selectedModelIndex = index
			break
		}
	}
	// An empty model selection is the only case that may default to the first
	// visible model. Preserve a non-empty unavailable ID so the command path
	// can reject it instead of executing a different model's exploration.
	if selectedModelIndex < 0 && selectedModelID == "" && len(models) > 0 {
		selectedModelIndex = 0
	}
	warnings := []string(nil)
	if bindingUnavailable {
		warnings = append(warnings, "Compiled semantic dataset bindings are unavailable for the active serving generation.")
	}
	result := DataExplorerProjection{Objects: objects, Models: models, Command: command, Warnings: warnings}
	if selectedModelIndex < 0 {
		return result
	}
	selectedModel := models[selectedModelIndex]
	result.SelectedModel = &selectedModel
	state.ModelID = projectsignals.Optional(selectedModel.ID)
	result.Datasets = append([]projectsignals.DataExploreDatasetSignal(nil), selectedModel.Datasets...)
	selectedDatasetID := strings.TrimSpace(projectsignals.ValueOrZero(state.DatasetID))
	for index := range result.Datasets {
		if result.Datasets[index].ID == selectedDatasetID {
			selected := result.Datasets[index]
			result.SelectedDataset = &selected
			break
		}
	}
	// Likewise, an authored dataset ID must never be replaced by the first
	// available dataset. Returning without fields leaves the command intact for
	// the execution boundary to reject while preserving empty initialization.
	if result.SelectedDataset == nil && selectedDatasetID != "" {
		return result
	}
	if result.SelectedDataset == nil && len(result.Datasets) > 0 {
		selected := result.Datasets[0]
		result.SelectedDataset = &selected
	}
	baseTable := ""
	if result.SelectedDataset != nil {
		baseTable = result.SelectedDataset.ID
		state.DatasetID = projectsignals.Optional(baseTable)
	}
	model := modelByID[selectedModel.ID]
	compiled := compiledByID[selectedModel.ID]
	if resolvedBase, changed := resolveExplorerBase(model, baseTable, state, compiled); changed {
		previousBase := baseTable
		baseTable = resolvedBase
		state.DatasetID = projectsignals.Optional(baseTable)
		for index := range result.Datasets {
			if result.Datasets[index].ID == baseTable {
				selected := result.Datasets[index]
				result.SelectedDataset = &selected
				break
			}
		}
		result.Warnings = append(result.Warnings, "Grain changed from "+explorerLabel(previousBase)+" to "+explorerLabel(baseTable)+" to support the selected fields.")
	}
	command.Spec = explorationSpecWithState(command.Spec, state)
	result.Command = command
	result.Fields = explorerFields(model, baseTable, state, compiled)
	return result
}

func explorerDatasets(model *semanticmodel.Model, compiled *semanticquery.CompiledModel) []projectsignals.DataExploreDatasetSignal {
	if model == nil {
		return []projectsignals.DataExploreDatasetSignal{}
	}
	tables := explorerDatasetTableMap(model, compiled)
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]projectsignals.DataExploreDatasetSignal, 0, len(names))
	for _, name := range names {
		table := tables[name]
		entities, grainEntity, grainFields := explorerDatasetEntities(table)
		fieldCount := len(table.Dimensions)
		for metricName, metric := range model.Metrics {
			if metric.Hidden {
				continue
			}
			for _, root := range explorerMetricRootDatasets(model, metricName) {
				if root == name {
					fieldCount++
					break
				}
			}
		}
		out = append(out, projectsignals.DataExploreDatasetSignal{
			ID: name, Title: explorerLabel(name), Description: projectsignals.Optional(table.Description),
			Entities: entities, GrainEntity: grainEntity, GrainFields: grainFields, FieldCount: int64(fieldCount),
		})
	}
	return out
}

func explorerDatasetTableMap(model *semanticmodel.Model, compiled *semanticquery.CompiledModel) map[string]semanticmodel.Table {
	result := map[string]semanticmodel.Table{}
	if model == nil {
		return result
	}
	if compiled == nil {
		return result
	}
	for _, name := range compiled.DatasetNames() {
		dataset, _ := compiled.Dataset(name)
		result[name] = dataset.Table()
	}
	return result
}

// explorerDatasetEntities preserves the semantic entity contract in the
// browser projection. Entity names are sorted for a stable signal while each
// entity's field tuple remains in authored order, which is significant for
// composite primary and unique grains.
func explorerDatasetEntities(table semanticmodel.Table) ([]projectsignals.SemanticModelGraphEntitySignal, string, []string) {
	names := make([]string, 0, len(table.Entities))
	for name := range table.Entities {
		names = append(names, name)
	}
	sort.Strings(names)
	entities := make([]projectsignals.SemanticModelGraphEntitySignal, 0, len(names))
	grainEntity := strings.TrimSpace(table.GrainEntity)
	grainFields := []string{}
	for _, name := range names {
		entity := table.Entities[name]
		fields := append([]string(nil), entity.Fields...)
		entities = append(entities, projectsignals.SemanticModelGraphEntitySignal{
			Name: name, Type: entity.Type, Fields: fields, Grain: projectsignals.Optional(name == grainEntity),
		})
		if name == grainEntity {
			grainFields = append([]string(nil), entity.Fields...)
		}
	}
	return entities, grainEntity, grainFields
}

func explorerFields(model *semanticmodel.Model, baseTable string, command dataExploreState, compiled *semanticquery.CompiledModel) []projectsignals.DataExploreFieldSignal {
	if model == nil {
		return []projectsignals.DataExploreFieldSignal{}
	}
	selectedDimensions := explorerStringSet(command.Dimensions)
	selectedMetrics := explorerStringSet(command.Metrics)
	tables := explorerDatasetTableMap(model, compiled)
	tableNames := make([]string, 0, len(tables))
	for name := range tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	out := make([]projectsignals.DataExploreFieldSignal, 0)
	for _, tableName := range tableNames {
		table := tables[tableName]
		fieldNames := make([]string, 0, len(table.Dimensions))
		for name := range table.Dimensions {
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			dimension := table.Dimensions[fieldName]
			id := tableName + "." + fieldName
			fieldType := firstExplorerNonEmpty(string(dimension.Datatype), dimension.Type, table.Columns[fieldName].Type)
			compatible, reason, path := explorerFieldCompatibility(model, baseTable, tableName)
			rebaseDatasetID := ""
			if !compatible {
				rebaseDatasetID = explorerFieldRebase(model, command, baseTable, id, "dimension", compiled)
				if rebaseDatasetID != "" {
					reason = "Select " + firstExplorerNonEmpty(dimension.Label, explorerLabel(fieldName)) + " and change grain from " + explorerLabel(baseTable) + " to " + explorerLabel(rebaseDatasetID) + "."
				}
			}
			out = append(out, projectsignals.DataExploreFieldSignal{
				ID: id, Label: firstExplorerNonEmpty(dimension.Label, explorerLabel(fieldName)), Kind: "dimension", ModelTable: tableName,
				Description: projectsignals.Optional(dimension.Description), Type: projectsignals.Optional(fieldType), Selected: selectedDimensions[id],
				Compatible: compatible, CompatibilityReason: projectsignals.Optional(reason), RelationshipPath: projectsignals.OptionalSlice(path),
				RebaseDatasetID: projectsignals.Optional(rebaseDatasetID),
			})
		}
	}
	// Conformed dimensions are governed semantic references, not physical
	// table columns. Expose them only when activation compiled both their
	// semantic type metadata and a binding for the selected base dataset. The
	// detached binding supplies the physical owner and relationship route used
	// by the executor, while the semantic metadata supplies the logical type
	// needed by typed filters and time validation.
	semanticNames := make([]string, 0, len(model.Dimensions))
	for name := range model.Dimensions {
		semanticNames = append(semanticNames, name)
	}
	sort.Strings(semanticNames)
	for _, name := range semanticNames {
		semantic, ok := compiled.SemanticDimension(name)
		if !ok {
			continue
		}
		authored := model.Dimensions[name]
		binding, compatible := compiled.DimensionBinding(name, baseTable)
		modelTable := ""
		path := []string(nil)
		if compatible {
			modelTable = binding.Physical.Table
			for _, relationship := range binding.Path {
				path = append(path, relationship.ID)
			}
		}
		fieldType := firstExplorerNonEmpty(string(semantic.Datatype), semantic.Type, string(binding.Physical.Datatype), binding.Physical.Type)
		reason := ""
		if !compatible {
			reason = "Not available from " + explorerLabel(baseTable) + " because no compiled binding reaches this semantic dimension."
		}
		out = append(out, projectsignals.DataExploreFieldSignal{
			ID: name, Label: firstExplorerNonEmpty(authored.Label, explorerLabel(name)), Kind: "dimension", ModelTable: modelTable,
			Description: projectsignals.Optional(authored.Description), Type: projectsignals.Optional(fieldType), Selected: selectedDimensions[name],
			Compatible: compatible, CompatibilityReason: projectsignals.Optional(reason), RelationshipPath: projectsignals.OptionalSlice(path),
		})
	}
	metricNames := make([]string, 0, len(model.Metrics))
	for name, metric := range model.Metrics {
		if !metric.Hidden {
			metricNames = append(metricNames, name)
		}
	}
	sort.Strings(metricNames)
	for _, name := range metricNames {
		metric := model.Metrics[name]
		roots := explorerMetricRootDatasets(model, name)
		// Aggregate metrics have one root dataset. Derived and ratio metrics
		// may span multiple roots; keep those visible from every base and let
		// the governed planner decide whether the selected combination is safe.
		modelTable := ""
		if len(roots) == 1 {
			modelTable = roots[0]
		}
		compatible := strings.TrimSpace(baseTable) == "" || len(roots) != 1 || roots[0] == baseTable
		reason := ""
		rebaseDatasetID := ""
		if !compatible {
			rebaseDatasetID = explorerFieldRebase(model, command, baseTable, name, "metric", compiled)
			if rebaseDatasetID != "" {
				reason = "Select " + firstExplorerNonEmpty(metric.Label, explorerLabel(name)) + " and change grain from " + explorerLabel(baseTable) + " to " + explorerLabel(rebaseDatasetID) + "."
			} else {
				reason = "Metric belongs to " + explorerLabel(modelTable) + " and cannot be combined safely with the selected fields."
			}
		}
		out = append(out, projectsignals.DataExploreFieldSignal{
			ID: name, Label: firstExplorerNonEmpty(metric.Label, explorerLabel(name)), Kind: "metric", ModelTable: modelTable,
			Description: projectsignals.Optional(metric.Description), Dataset: projectsignals.Optional(modelTable), Type: projectsignals.Optional(firstExplorerNonEmpty(metric.Aggregation, metric.Type)), Selected: selectedMetrics[name],
			Compatible: compatible, CompatibilityReason: projectsignals.Optional(reason), RebaseDatasetID: projectsignals.Optional(rebaseDatasetID),
		})
	}
	return out
}

func explorerFieldCompatibility(model *semanticmodel.Model, baseTable, table string) (bool, string, []string) {
	baseTable = strings.TrimSpace(baseTable)
	if model == nil || baseTable == "" || baseTable == table {
		return true, "", nil
	}
	path, err := model.SafeRelationshipPath(baseTable, table)
	if err != nil {
		return false, "Not available from " + explorerLabel(baseTable) + " because no grain-preserving relationship path reaches " + explorerLabel(table) + ".", nil
	}
	ids := make([]string, 0, len(path))
	for _, relationship := range path {
		ids = append(ids, relationship.ID)
	}
	return true, "", ids
}

func resolveExplorerBase(model *semanticmodel.Model, currentBase string, command dataExploreState, compiled *semanticquery.CompiledModel) (string, bool) {
	currentBase = strings.TrimSpace(currentBase)
	if model == nil {
		return currentBase, false
	}
	targets, metricDatasets := explorerCommandTargets(model, command)
	if explorerBaseScore(model, currentBase, targets, metricDatasets, compiled) >= 0 {
		return currentBase, false
	}
	bestBase, bestScore, tied := "", -1, false
	for candidate := range explorerDatasetTableMap(model, compiled) {
		score := explorerBaseScore(model, candidate, targets, metricDatasets, compiled)
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

// explorerMetricRootDatasets resolves the physical dataset roots for a metric.
// Aggregate metrics have one direct root; derived and ratio metrics recurse
// through their metric dependencies. Validation rejects cycles, but the
// visiting guard keeps this browser projection defensive for an incomplete
// or stale manifest.
func explorerMetricRootDatasets(model *semanticmodel.Model, name string) []string {
	if model == nil {
		return nil
	}
	memo := map[string]map[string]struct{}{}
	visiting := map[string]bool{}
	var visit func(string) map[string]struct{}
	visit = func(metricName string) map[string]struct{} {
		if roots, ok := memo[metricName]; ok {
			return roots
		}
		if visiting[metricName] {
			return nil
		}
		metric, ok := model.Metrics[metricName]
		if !ok {
			return nil
		}
		visiting[metricName] = true
		roots := map[string]struct{}{}
		if metric.Type == "aggregate" && strings.TrimSpace(metric.Dataset) != "" {
			roots[metric.Dataset] = struct{}{}
		}
		var refs []string
		switch metric.Type {
		case "derived":
			if expression, err := semanticmodel.ParseExpression(metric.Expression); err == nil {
				refs = expression.References()
			}
		case "ratio":
			refs = []string{metric.Numerator, metric.Denominator}
		}
		for _, ref := range refs {
			for root := range visit(ref) {
				roots[root] = struct{}{}
			}
		}
		delete(visiting, metricName)
		memo[metricName] = roots
		return roots
	}
	roots := visit(strings.TrimSpace(name))
	out := make([]string, 0, len(roots))
	for root := range roots {
		out = append(out, root)
	}
	sort.Strings(out)
	return out
}

func explorerCommandTargets(model *semanticmodel.Model, command dataExploreState) ([]string, []string) {
	if model == nil {
		return nil, nil
	}
	targetSet := map[string]bool{}
	metricSet := map[string]bool{}
	addDimension := func(id string) {
		if dimension, err := model.ResolveDimension(strings.TrimSpace(id)); err == nil {
			targetSet[dimension.Table] = true
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
	for _, id := range command.Metrics {
		metric := strings.TrimSpace(id)
		if resolved, err := model.ResolveMetric(metric); err == nil && !resolved.Hidden {
			roots := explorerMetricRootDatasets(model, metric)
			// A base can be constrained safely only when a metric has one root
			// dataset. Multi-dataset derived/ratio metrics remain unconstrained here
			// so they are not misrepresented as belonging to one table.
			if len(roots) == 1 {
				metricSet[roots[0]] = true
			}
		}
	}
	targets := make([]string, 0, len(targetSet))
	for table := range targetSet {
		targets = append(targets, table)
	}
	metricDatasets := make([]string, 0, len(metricSet))
	for dataset := range metricSet {
		metricDatasets = append(metricDatasets, dataset)
	}
	sort.Strings(targets)
	sort.Strings(metricDatasets)
	return targets, metricDatasets
}

func explorerBaseScore(model *semanticmodel.Model, candidate string, targets, metricDatasets []string, compiled *semanticquery.CompiledModel) int {
	if model == nil {
		return -1
	}
	if _, ok := explorerDatasetTableMap(model, compiled)[candidate]; !ok {
		return -1
	}
	for _, dataset := range metricDatasets {
		if dataset != candidate {
			return -1
		}
	}
	score := 0
	for _, target := range targets {
		if target == candidate {
			continue
		}
		path, err := model.SafeRelationshipPath(candidate, target)
		if err != nil {
			return -1
		}
		score += len(path)
	}
	return score
}

func explorerFieldRebase(model *semanticmodel.Model, command dataExploreState, currentBase, fieldID, kind string, compiled *semanticquery.CompiledModel) string {
	hypothetical := command
	if kind == "metric" {
		hypothetical.Metrics = appendUniqueExplorerValue(hypothetical.Metrics, fieldID)
	} else {
		hypothetical.Dimensions = appendUniqueExplorerValue(hypothetical.Dimensions, fieldID)
	}
	base, changed := resolveExplorerBase(model, currentBase, hypothetical, compiled)
	if !changed {
		return ""
	}
	return base
}

func appendUniqueExplorerValue(values []string, value string) []string {
	out := append([]string(nil), values...)
	for _, current := range out {
		if current == value {
			return out
		}
	}
	return append(out, value)
}

func explorerModelTableObject(asset projectview.DevelopAssetView, table semanticmodel.Table, columns []projectsignals.DataPreviewColumnSignal, modelID string) projectsignals.DataExplorerObjectSignal {
	object := projectsignals.DataExplorerObjectSignal{
		Key:           "model_table:" + asset.ID,
		AssetID:       projectsignals.Optional(asset.ID),
		ResourceID:    asset.ID,
		Layer:         "model_table",
		ModelID:       projectsignals.Optional(modelID),
		Table:         projectsignals.Optional(firstExplorerNonEmpty(asset.Key, asset.ID)),
		Title:         firstExplorerNonEmpty(asset.Title, asset.Key, asset.ID),
		Description:   projectsignals.Optional(firstExplorerNonEmpty(asset.Description, table.Description)),
		DetailHref:    projectsignals.Optional(explorerAssetDetailsHref(asset, "details")),
		Grain:         projectsignals.Optional(table.GrainEntity),
		ColumnCount:   int64(len(columns)),
		RowCountLabel: projectsignals.Pointer("Unknown"),
		Columns:       projectsignals.OptionalSlice(columns),
	}
	return object
}

func explorerSourceColumns(source semanticmodel.Source) []projectsignals.DataPreviewColumnSignal {
	names := make([]string, 0, len(source.Fields))
	for name := range source.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	columns := make([]projectsignals.DataPreviewColumnSignal, 0, len(names))
	for _, name := range names {
		field := source.Fields[name]
		columns = append(columns, projectsignals.DataPreviewColumnSignal{Key: name, Label: firstExplorerNonEmpty(field.Name, name), Type: projectsignals.Optional(field.Type), Description: projectsignals.Optional(field.Description)})
	}
	if len(columns) == 0 {
		for _, column := range source.Schema.Columns {
			columns = append(columns, projectsignals.DataPreviewColumnSignal{Key: column.Name, Label: column.Name, Type: projectsignals.Optional(column.PhysicalType), Description: projectsignals.Optional(column.Comment), Nullable: column.Nullable, DefaultValue: projectsignals.Optional(column.Default), PrimaryKey: projectsignals.Optional(column.PrimaryKey)})
		}
	}
	return columns
}

func explorerTableColumns(table semanticmodel.Table) []projectsignals.DataPreviewColumnSignal {
	if len(table.Schema.Columns) > 0 {
		columns := make([]projectsignals.DataPreviewColumnSignal, 0, len(table.Schema.Columns))
		for _, column := range table.Schema.Columns {
			columns = append(columns, projectsignals.DataPreviewColumnSignal{Key: column.Name, Label: column.Name, Type: projectsignals.Optional(column.PhysicalType), Description: projectsignals.Optional(column.Comment), Nullable: column.Nullable, DefaultValue: projectsignals.Optional(column.Default), PrimaryKey: projectsignals.Optional(column.PrimaryKey)})
		}
		return columns
	}
	names := make([]string, 0, len(table.Columns))
	for name := range table.Columns {
		names = append(names, name)
	}
	if len(names) == 0 {
		for name := range table.Dimensions {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	columns := make([]projectsignals.DataPreviewColumnSignal, 0, len(names))
	for _, name := range names {
		column := table.Columns[name]
		if column.Name == "" {
			column.Name = name
		}
		columns = append(columns, projectsignals.DataPreviewColumnSignal{Key: name, Label: firstExplorerNonEmpty(column.Name, name), Type: projectsignals.Optional(column.Type), Description: projectsignals.Optional(column.Description)})
	}
	return columns
}

func explorerTableByName(tables map[string]semanticmodel.Table, name string) semanticmodel.Table {
	for key, table := range tables {
		if key == name || strings.EqualFold(key, name) {
			return table
		}
	}
	return semanticmodel.Table{}
}

func explorerModelIDByName(assets map[string]projectview.DevelopAssetView, name string) string {
	for id, asset := range assets {
		if asset.Type == string(projectview.AssetTypeModelTable) && (asset.Key == name || strings.EqualFold(asset.Title, name)) {
			return id
		}
	}
	return ""
}

func sortedExplorerAssets(assets map[string]projectview.DevelopAssetView) []projectview.DevelopAssetView {
	out := make([]projectview.DevelopAssetView, 0, len(assets))
	for _, asset := range assets {
		if asset.Type == string(projectview.AssetTypeSource) || asset.Type == string(projectview.AssetTypeModelTable) {
			out = append(out, asset)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		left := strings.ToLower(firstExplorerNonEmpty(out[i].Title, out[i].Key, out[i].ID))
		right := strings.ToLower(firstExplorerNonEmpty(out[j].Title, out[j].Key, out[j].ID))
		if left != right {
			return left < right
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sortExplorerObjects(objects []projectsignals.DataExplorerObjectSignal) {
	sort.SliceStable(objects, func(i, j int) bool {
		if objects[i].Layer != objects[j].Layer {
			if objects[i].Layer == "source" {
				return true
			}
			if objects[j].Layer == "source" {
				return false
			}
			return objects[i].Layer < objects[j].Layer
		}
		left := strings.ToLower(firstExplorerNonEmpty(objects[i].Title, objects[i].Key))
		right := strings.ToLower(firstExplorerNonEmpty(objects[j].Title, objects[j].Key))
		if left != right {
			return left < right
		}
		return objects[i].Key < objects[j].Key
	})
}

func explorerAssetDetailsHref(asset projectview.DevelopAssetView, section string) string {
	base := ""
	switch asset.Type {
	case string(projectview.AssetTypeSource):
		base = "/sources/"
	case string(projectview.AssetTypeModelTable):
		base = "/models/"
	default:
		return ""
	}
	return base + url.PathEscape(asset.ID) + "/" + url.PathEscape(section)
}

func explorerStringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func explorerLabel(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "_", " "))
	if value == "" {
		return "-"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func firstExplorerNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
