package http

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectview "github.com/flidai/leapview/internal/project"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerModelObject(asset projectview.DevelopAssetView, table semanticmodel.Table, columns []projectsignals.DataPreviewColumnSignal, semanticModelID, datasetID string) projectsignals.DataExplorerObjectSignal {
	datasetID = firstExplorerNonEmpty(datasetID, asset.Key, asset.ID)
	object := projectsignals.DataExplorerObjectSignal{
		Key:             explorerModelObjectKey(asset.ID, semanticModelID, datasetID),
		AssetID:         projectsignals.Optional(asset.ID),
		ResourceID:      asset.ID,
		Layer:           "model",
		SemanticModelID: projectsignals.Optional(semanticModelID),
		DatasetID:       projectsignals.Optional(datasetID),
		Title:           firstExplorerNonEmpty(asset.Title, asset.Key, asset.ID),
		Description:     projectsignals.Optional(firstExplorerNonEmpty(asset.Description, table.Description)),
		DetailHref:      projectsignals.Optional(explorerAssetDetailsHref(asset, "details")),
		Grain:           projectsignals.Optional(table.GrainEntity),
		ColumnCount:     int64(len(columns)),
		RowCountLabel:   projectsignals.Pointer("Unknown"),
		Columns:         projectsignals.OptionalSlice(columns),
	}
	return object
}

// explorerModelObjectKey identifies one browser object backed by a logical
// Model. A semantic model may expose the same Model through multiple dataset
// aliases, so the backing resource ID alone is not a sufficient selection key.
// Keep the components in canonical identity order and leave ResourceID
// untouched on the signal for query authorization and detail navigation. Bound
// components are length-prefixed because resource IDs may contain colons; the
// bracketed form also stays distinct from the unbound model fallback.
func explorerModelObjectKey(modelID, semanticModelID, datasetID string) string {
	modelID = strings.TrimSpace(modelID)
	semanticModelID = strings.TrimSpace(semanticModelID)
	datasetID = strings.TrimSpace(datasetID)
	if semanticModelID == "" {
		return "model:" + modelID
	}
	parts := []string{modelID, semanticModelID, datasetID}
	var key strings.Builder
	key.WriteString("model:[")
	for index, part := range parts {
		if index > 0 {
			key.WriteString("][")
		}
		key.WriteString(strconv.Itoa(len(part)))
		key.WriteByte(':')
		key.WriteString(part)
	}
	key.WriteByte(']')
	return key.String()
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
		if asset.Type == string(projectview.AssetTypeModel) && (asset.Key == name || strings.EqualFold(asset.Title, name)) {
			return id
		}
	}
	return ""
}

func sortedExplorerAssets(assets map[string]projectview.DevelopAssetView) []projectview.DevelopAssetView {
	out := make([]projectview.DevelopAssetView, 0, len(assets))
	for _, asset := range assets {
		if asset.Type == string(projectview.AssetTypeSource) || asset.Type == string(projectview.AssetTypeModel) {
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
	case string(projectview.AssetTypeModel):
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
