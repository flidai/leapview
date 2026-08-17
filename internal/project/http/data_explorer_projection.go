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
}

// BuildDataExplorerProjection projects authorized serving assets together
// with the active compiled project manifest.  Asset visibility is authoritative
// for every output: manifest entries that do not have a visible serving asset
// are never exposed to the browser.
func BuildDataExplorerProjection(assets []projectview.DevelopAssetView, project projectmanifest.Project, command projectsignals.DataExploreCommand) DataExplorerProjection {
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
	for _, id := range semanticIDs {
		model := project.SemanticModels[id]
		if model == nil {
			continue
		}
		modelByID[id] = model
		asset := visible[id]
		models = append(models, projectsignals.DataExploreModelSignal{
			ID:          id,
			Title:       firstExplorerNonEmpty(model.Title, model.Name, asset.Title, id),
			Description: projectsignals.Optional(firstExplorerNonEmpty(model.Description, asset.Description)),
			Datasets:    explorerDatasets(model),
		})
	}

	// A model table is keyed by its canonical model resource ID in the
	// manifest. Semantic model table names remain authored names, so resolve
	// them through NameIndex before associating browser objects with a model.
	semanticForTable := make(map[string][]string)
	for _, semanticID := range semanticIDs {
		model := modelByID[semanticID]
		for tableName := range model.Tables {
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

	selectedModelID := strings.TrimSpace(projectsignals.ValueOrZero(command.ModelID))
	selectedModelIndex := -1
	for index := range models {
		if models[index].ID == selectedModelID {
			selectedModelIndex = index
			break
		}
	}
	if selectedModelIndex < 0 && len(models) > 0 {
		selectedModelIndex = 0
	}
	result := DataExplorerProjection{Objects: objects, Models: models}
	if selectedModelIndex < 0 {
		return result
	}
	selectedModel := models[selectedModelIndex]
	result.SelectedModel = &selectedModel
	result.Datasets = append([]projectsignals.DataExploreDatasetSignal(nil), selectedModel.Datasets...)
	selectedDatasetID := strings.TrimSpace(projectsignals.ValueOrZero(command.DatasetID))
	for index := range result.Datasets {
		if result.Datasets[index].ID == selectedDatasetID {
			selected := result.Datasets[index]
			result.SelectedDataset = &selected
			break
		}
	}
	if result.SelectedDataset == nil && len(result.Datasets) > 0 {
		selected := result.Datasets[0]
		result.SelectedDataset = &selected
	}
	baseTable := ""
	if result.SelectedDataset != nil {
		baseTable = result.SelectedDataset.ID
	}
	result.Fields = explorerFields(modelByID[selectedModel.ID], baseTable, command)
	return result
}

func explorerDatasets(model *semanticmodel.Model) []projectsignals.DataExploreDatasetSignal {
	if model == nil {
		return []projectsignals.DataExploreDatasetSignal{}
	}
	names := make([]string, 0, len(model.Tables))
	for name := range model.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]projectsignals.DataExploreDatasetSignal, 0, len(names))
	for _, name := range names {
		table := model.Tables[name]
		fieldCount := len(table.Dimensions)
		for _, measure := range model.Measures {
			if !measure.Hidden && measure.Fact == name {
				fieldCount++
			}
		}
		out = append(out, projectsignals.DataExploreDatasetSignal{
			ID: name, Title: explorerLabel(name), Description: projectsignals.Optional(table.Description),
			Grain: projectsignals.Optional(table.Grain), FieldCount: int64(fieldCount),
		})
	}
	return out
}

func explorerFields(model *semanticmodel.Model, baseTable string, command projectsignals.DataExploreCommand) []projectsignals.DataExploreFieldSignal {
	if model == nil {
		return []projectsignals.DataExploreFieldSignal{}
	}
	selectedDimensions := explorerStringSet(command.Dimensions)
	selectedMeasures := explorerStringSet(command.Measures)
	tableNames := make([]string, 0, len(model.Tables))
	for name := range model.Tables {
		tableNames = append(tableNames, name)
	}
	sort.Strings(tableNames)
	out := make([]projectsignals.DataExploreFieldSignal, 0)
	for _, tableName := range tableNames {
		table := model.Tables[tableName]
		fieldNames := make([]string, 0, len(table.Dimensions))
		for name := range table.Dimensions {
			fieldNames = append(fieldNames, name)
		}
		sort.Strings(fieldNames)
		for _, fieldName := range fieldNames {
			dimension := table.Dimensions[fieldName]
			id := tableName + "." + fieldName
			fieldType := firstExplorerNonEmpty(dimension.Type, table.Columns[fieldName].Type)
			compatible, reason, path := explorerFieldCompatibility(model, baseTable, tableName)
			out = append(out, projectsignals.DataExploreFieldSignal{
				ID: id, Label: firstExplorerNonEmpty(dimension.Label, explorerLabel(fieldName)), Kind: "dimension", ModelTable: tableName,
				Description: projectsignals.Optional(dimension.Description), Type: projectsignals.Optional(fieldType), Selected: selectedDimensions[id],
				Compatible: compatible, CompatibilityReason: projectsignals.Optional(reason), RelationshipPath: projectsignals.OptionalSlice(path),
			})
		}
	}
	measureNames := make([]string, 0, len(model.Measures))
	for name, measure := range model.Measures {
		if !measure.Hidden {
			measureNames = append(measureNames, name)
		}
	}
	sort.Strings(measureNames)
	for _, name := range measureNames {
		measure := model.Measures[name]
		compatible := strings.TrimSpace(baseTable) == "" || measure.Fact == baseTable
		reason := ""
		if !compatible {
			reason = "Measure belongs to " + explorerLabel(measure.Fact) + " and cannot be combined safely with the selected fields."
		}
		out = append(out, projectsignals.DataExploreFieldSignal{
			ID: name, Label: firstExplorerNonEmpty(measure.Label, explorerLabel(name)), Kind: "measure", ModelTable: measure.Fact,
			Description: projectsignals.Optional(measure.Description), Fact: projectsignals.Optional(measure.Fact), Selected: selectedMeasures[name],
			Compatible: compatible, CompatibilityReason: projectsignals.Optional(reason),
		})
	}
	return out
}

func explorerFieldCompatibility(model *semanticmodel.Model, baseTable, table string) (bool, string, []string) {
	baseTable = strings.TrimSpace(baseTable)
	if baseTable == "" || baseTable == table {
		return true, "", nil
	}
	// Relationships are intentionally treated as a small undirected graph for
	// field presentation. Query planning remains the authority for whether a
	// governed join is executable.
	type step struct {
		table string
		path  []string
	}
	queue := []step{{table: baseTable}}
	seen := map[string]struct{}{baseTable: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, relationship := range model.Relationships {
			from, fromField, fromOK := strings.Cut(relationship.From, ".")
			to, toField, toOK := strings.Cut(relationship.To, ".")
			if !fromOK || !toOK {
				continue
			}
			next, edgeLabel := "", relationship.ID
			switch current.table {
			case from:
				next = to
			case to:
				next = from
			}
			_ = fromField
			_ = toField
			if next == "" {
				continue
			}
			path := append(append([]string(nil), current.path...), edgeLabel)
			if next == table {
				return true, "", path
			}
			if _, ok := seen[next]; ok {
				continue
			}
			seen[next] = struct{}{}
			queue = append(queue, step{table: next, path: path})
		}
	}
	return false, "Field belongs to an unrelated table and cannot be combined safely with the selected fields.", nil
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
		Grain:         projectsignals.Optional(table.Grain),
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
