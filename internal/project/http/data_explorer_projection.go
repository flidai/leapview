package http

// This file contains the browser-facing Data Explorer projection.  The
// serving graph decides which resources are visible; the compiled project
// manifest supplies the semantic model and model metadata that is deliberately
// not duplicated in the graph's project.graph.v1 payload.

import (
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
	Objects               []projectsignals.DataExplorerObjectSignal
	SemanticModels        []projectsignals.DataExploreSemanticModelSignal
	SelectedSemanticModel *projectsignals.DataExploreSemanticModelSignal
	Datasets              []projectsignals.DataExploreDatasetSignal
	SelectedDataset       *projectsignals.DataExploreDatasetSignal
	Fields                []projectsignals.DataExploreFieldSignal
	Command               projectsignals.DataExploreCommand
	Warnings              []string
}

type explorerModelBinding struct {
	SemanticModelID string
	DatasetID       string
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

	semanticModels := make([]projectsignals.DataExploreSemanticModelSignal, 0, len(semanticIDs))
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
		semanticModels = append(semanticModels, projectsignals.DataExploreSemanticModelSignal{
			ID:          id,
			Title:       firstExplorerNonEmpty(model.Title, model.Name, asset.Title, id),
			Description: projectsignals.Optional(firstExplorerNonEmpty(model.Description, asset.Description)),
			Datasets:    explorerDatasets(model, compiled),
		})
	}

	// A model is keyed by its canonical model resource ID in the
	// manifest. Semantic dataset names remain authored names, so resolve
	// them through NameIndex before associating browser objects with a model.
	bindingsByModelID := make(map[string][]explorerModelBinding)
	for _, semanticID := range semanticIDs {
		model := modelByID[semanticID]
		compiled := compiledByID[semanticID]
		for datasetID, datasetTable := range explorerDatasetTableMap(model, compiled) {
			tableName := datasetTable.ModelName
			modelID := project.NameIndex.Models[tableName]
			if modelID == "" {
				modelID = explorerModelIDByName(visible, tableName)
			}
			if modelID == "" {
				continue
			}
			bindingsByModelID[modelID] = append(bindingsByModelID[modelID], explorerModelBinding{
				SemanticModelID: semanticID,
				DatasetID:       datasetID,
			})
		}
	}
	for id := range bindingsByModelID {
		sort.Slice(bindingsByModelID[id], func(i, j int) bool {
			left, right := bindingsByModelID[id][i], bindingsByModelID[id][j]
			if left.SemanticModelID != right.SemanticModelID {
				return left.SemanticModelID < right.SemanticModelID
			}
			return left.DatasetID < right.DatasetID
		})
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
		case string(projectview.AssetTypeModel):
			table, ok := project.Models[asset.ID]
			if !ok {
				// A malformed or stale manifest must not make the entire catalog
				// unavailable. Keep the authorized object with graph metadata.
				table = explorerTableByName(project.Models, asset.Key)
			}
			columns := explorerTableColumns(table)
			bindings := bindingsByModelID[asset.ID]
			if len(bindings) == 0 {
				objects = append(objects, explorerModelObject(asset, table, columns, "", ""))
				continue
			}
			// One object per semantic dataset binding keeps field compatibility
			// scoped to the selected model and dataset. The object key carries
			// the full binding identity; ResourceID remains the backing logical
			// Model resource for governed preview execution and detail links.
			for _, binding := range bindings {
				objects = append(objects, explorerModelObject(asset, table, columns, binding.SemanticModelID, binding.DatasetID))
			}
		}
	}
	sortExplorerObjects(objects)

	selectedSemanticModelID := strings.TrimSpace(projectsignals.ValueOrZero(state.ModelID))
	selectedSemanticModelIndex := -1
	for index := range semanticModels {
		if semanticModels[index].ID == selectedSemanticModelID {
			selectedSemanticModelIndex = index
			break
		}
	}
	// An empty model selection is the only case that may default to the first
	// visible model. Preserve a non-empty unavailable ID so the command path
	// can reject it instead of executing a different model's exploration.
	if selectedSemanticModelIndex < 0 && selectedSemanticModelID == "" && len(semanticModels) > 0 {
		selectedSemanticModelIndex = 0
	}
	warnings := []string(nil)
	if bindingUnavailable {
		warnings = append(warnings, "Compiled semantic dataset bindings are unavailable for the active serving generation.")
	}
	result := DataExplorerProjection{Objects: objects, SemanticModels: semanticModels, Command: command, Warnings: warnings}
	if selectedSemanticModelIndex < 0 {
		return result
	}
	selectedSemanticModel := semanticModels[selectedSemanticModelIndex]
	result.SelectedSemanticModel = &selectedSemanticModel
	state.ModelID = projectsignals.Optional(selectedSemanticModel.ID)
	result.Datasets = append([]projectsignals.DataExploreDatasetSignal(nil), selectedSemanticModel.Datasets...)
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
	model := modelByID[selectedSemanticModel.ID]
	compiled := compiledByID[selectedSemanticModel.ID]
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
				ID: id, Label: firstExplorerNonEmpty(dimension.Label, explorerLabel(fieldName)), Kind: "dimension", DatasetID: tableName,
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
		datasetID := ""
		path := []string(nil)
		if compatible {
			datasetID = binding.Physical.Table
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
			ID: name, Label: firstExplorerNonEmpty(authored.Label, explorerLabel(name)), Kind: "dimension", DatasetID: datasetID,
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
		datasetID := ""
		if len(roots) == 1 {
			datasetID = roots[0]
		}
		compatible := strings.TrimSpace(baseTable) == "" || len(roots) != 1 || roots[0] == baseTable
		reason := ""
		rebaseDatasetID := ""
		if !compatible {
			rebaseDatasetID = explorerFieldRebase(model, command, baseTable, name, "metric", compiled)
			if rebaseDatasetID != "" {
				reason = "Select " + firstExplorerNonEmpty(metric.Label, explorerLabel(name)) + " and change grain from " + explorerLabel(baseTable) + " to " + explorerLabel(rebaseDatasetID) + "."
			} else {
				reason = "Metric belongs to " + explorerLabel(datasetID) + " and cannot be combined safely with the selected fields."
			}
		}
		out = append(out, projectsignals.DataExploreFieldSignal{
			ID: name, Label: firstExplorerNonEmpty(metric.Label, explorerLabel(name)), Kind: "metric", DatasetID: datasetID,
			Description: projectsignals.Optional(metric.Description), Type: projectsignals.Optional(firstExplorerNonEmpty(metric.Aggregation, metric.Type)), Selected: selectedMetrics[name],
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
