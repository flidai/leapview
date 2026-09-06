package http

import (
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func explorerTargetAllowed(modelID string, model *semanticmodel.Model, predicate SemanticAccessPredicate, target semanticquery.SemanticAccessTarget) bool {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return true
	}
	return predicate != nil && predicate(modelID, target)
}

func explorerAnyDatasetAllowed(modelID string, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, predicate SemanticAccessPredicate) bool {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return true
	}
	if predicate == nil || compiled == nil {
		return false
	}
	for _, dataset := range compiled.DatasetNames() {
		if predicate(modelID, semanticquery.SemanticAccessTarget{Dataset: dataset}) {
			return true
		}
	}
	return false
}

func explorerDimensionNamesForField(model *semanticmodel.Model, datasetID, field string) []string {
	if model == nil {
		return nil
	}
	names := make([]string, 0)
	for name, dimension := range model.Dimensions {
		binding, ok := dimension.Bindings[datasetID]
		if !ok {
			continue
		}
		if binding.Field == datasetID+"."+field || strings.TrimPrefix(binding.Field, datasetID+".") == field {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func explorerDimensionTargets(model *semanticmodel.Model, datasetID, field string) []semanticquery.SemanticAccessTarget {
	if model == nil || strings.TrimSpace(field) == "" {
		return nil
	}
	if _, ok := model.Dimensions[field]; ok {
		if datasetID != "" {
			return []semanticquery.SemanticAccessTarget{{Dataset: datasetID, Dimension: field}}
		}
		names := make([]string, 0, len(model.Dimensions[field].Bindings))
		for dataset := range model.Dimensions[field].Bindings {
			names = append(names, dataset)
		}
		sort.Strings(names)
		targets := make([]semanticquery.SemanticAccessTarget, 0, len(names))
		for _, dataset := range names {
			targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: field})
		}
		return targets
	}
	if strings.Contains(field, ".") {
		parts := strings.SplitN(field, ".", 2)
		if len(parts) != 2 {
			return nil
		}
		names := explorerDimensionNamesForField(model, parts[0], parts[1])
		targets := make([]semanticquery.SemanticAccessTarget, 0, len(names))
		for _, name := range names {
			targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: parts[0], Dimension: name})
		}
		return targets
	}
	names := explorerDimensionNamesForField(model, datasetID, field)
	targets := make([]semanticquery.SemanticAccessTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, semanticquery.SemanticAccessTarget{Dataset: datasetID, Dimension: name})
	}
	return targets
}

func explorerFieldAllowed(modelID string, model *semanticmodel.Model, datasetID, field string, metric bool, predicate SemanticAccessPredicate) bool {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return true
	}
	if predicate == nil {
		return false
	}
	if metric {
		return predicate(modelID, semanticquery.SemanticAccessTarget{Metric: field})
	}
	targets := explorerDimensionTargets(model, datasetID, field)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !predicate(modelID, target) {
			return false
		}
	}
	return true
}

func explorerAuthorizedTableColumns(modelID string, model *semanticmodel.Model, datasetID string, columns []projectsignals.DataPreviewColumnSignal, predicate SemanticAccessPredicate) []projectsignals.DataPreviewColumnSignal {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return columns
	}
	out := make([]projectsignals.DataPreviewColumnSignal, 0, len(columns))
	for _, column := range columns {
		if explorerFieldAllowed(modelID, model, datasetID, column.Key, false, predicate) {
			out = append(out, column)
		}
	}
	return out
}

func explorerDatasetEntitiesWithAccess(modelID string, model *semanticmodel.Model, datasetID string, table semanticmodel.Table, predicate SemanticAccessPredicate) ([]projectsignals.SemanticModelGraphEntitySignal, string, []string) {
	entities, grainEntity, grainFields := explorerDatasetEntities(table)
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return entities, grainEntity, grainFields
	}
	filteredEntities := make([]projectsignals.SemanticModelGraphEntitySignal, 0, len(entities))
	for _, entity := range entities {
		allFieldsAllowed := true
		fields := make([]string, 0, len(entity.Fields))
		for _, field := range entity.Fields {
			if explorerFieldAllowed(modelID, model, datasetID, field, false, predicate) {
				fields = append(fields, field)
			} else {
				allFieldsAllowed = false
			}
		}
		entity.Fields = fields
		// Do not leave an entity/grain marker that allows a denied field to be
		// inferred from auxiliary metadata. A partially visible entity is
		// omitted as a conservative projection boundary.
		if len(fields) != 0 && allFieldsAllowed {
			if entity.Name == grainEntity {
				entity.Grain = projectsignals.Optional(true)
			}
			filteredEntities = append(filteredEntities, entity)
		}
	}
	grainVisible := false
	if grainEntity != "" {
		grainVisible = true
		for _, field := range grainFields {
			if !explorerFieldAllowed(modelID, model, datasetID, field, false, predicate) {
				grainVisible = false
				break
			}
		}
		if grainVisible {
			found := false
			for _, entity := range filteredEntities {
				if entity.Name == grainEntity {
					found = true
					break
				}
			}
			grainVisible = found
		}
	}
	if !grainVisible {
		grainEntity = ""
		grainFields = nil
	}
	filteredGrainFields := make([]string, 0, len(grainFields))
	for _, field := range grainFields {
		if explorerFieldAllowed(modelID, model, datasetID, field, false, predicate) {
			filteredGrainFields = append(filteredGrainFields, field)
		}
	}
	return filteredEntities, grainEntity, filteredGrainFields
}

func explorerFieldsWithAccess(modelID string, model *semanticmodel.Model, baseTable string, command projectsignals.DataExploreCommand, compiled *semanticquery.CompiledModel, predicate SemanticAccessPredicate) []projectsignals.DataExploreFieldSignal {
	fields := explorerFields(model, baseTable, command, compiled)
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return fields
	}
	out := make([]projectsignals.DataExploreFieldSignal, 0, len(fields))
	for _, field := range fields {
		if explorerFieldAllowed(modelID, model, field.ModelTable, field.ID, field.Kind == "metric", predicate) {
			out = append(out, field)
		}
	}
	return out
}

func sanitizeExplorerCommand(modelID string, model *semanticmodel.Model, command projectsignals.DataExploreCommand, predicate SemanticAccessPredicate) projectsignals.DataExploreCommand {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return command
	}
	filteredDimensions := make([]string, 0, len(command.Dimensions))
	for _, field := range command.Dimensions {
		if explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), field, false, predicate) {
			filteredDimensions = append(filteredDimensions, field)
		}
	}
	command.Dimensions = filteredDimensions
	filteredMetrics := make([]string, 0, len(command.Metrics))
	for _, field := range command.Metrics {
		if explorerFieldAllowed(modelID, model, "", field, true, predicate) {
			filteredMetrics = append(filteredMetrics, field)
		}
	}
	command.Metrics = filteredMetrics
	if command.ColumnWidths != nil {
		widths := make(map[string]float64, len(*command.ColumnWidths))
		dataset := projectsignals.ValueOrZero(command.DatasetID)
		for field, width := range *command.ColumnWidths {
			metric := false
			if _, ok := model.Metrics[field]; ok {
				metric = true
			}
			if explorerFieldAllowed(modelID, model, dataset, field, metric, predicate) {
				widths[field] = width
			}
		}
		if len(widths) == 0 {
			command.ColumnWidths = nil
		} else {
			command.ColumnWidths = &widths
		}
	}
	filteredFilters := make([]projectsignals.DataExploreFilterSignal, 0, len(command.Filters))
	for _, filter := range command.Filters {
		dataset := projectsignals.ValueOrZero(filter.Dataset)
		if dataset == "" {
			dataset = projectsignals.ValueOrZero(command.DatasetID)
		}
		metric := false
		if _, ok := model.Metrics[filter.Field]; ok {
			metric = true
		}
		if explorerFieldAllowed(modelID, model, dataset, filter.Field, metric, predicate) {
			filteredFilters = append(filteredFilters, filter)
		}
	}
	command.Filters = filteredFilters
	filteredSort := make([]projectsignals.DataExploreSortSignal, 0, len(command.Sort))
	for _, sortSpec := range command.Sort {
		metric := false
		if _, ok := model.Metrics[sortSpec.Field]; ok {
			metric = true
		}
		if explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), sortSpec.Field, metric, predicate) {
			filteredSort = append(filteredSort, sortSpec)
		}
	}
	command.Sort = filteredSort
	if command.Time != nil && !explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), command.Time.Field, false, predicate) {
		command.Time = nil
	}
	return command
}

func explorerCommandHasDeniedAccess(modelID string, model *semanticmodel.Model, command projectsignals.DataExploreCommand, predicate SemanticAccessPredicate) bool {
	if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
		return false
	}
	if predicate == nil {
		return true
	}
	if dataset := projectsignals.ValueOrZero(command.DatasetID); dataset != "" && !predicate(modelID, semanticquery.SemanticAccessTarget{Dataset: dataset}) {
		return true
	}
	for _, field := range command.Dimensions {
		if !explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), field, false, predicate) {
			return true
		}
	}
	for _, field := range command.Metrics {
		if !explorerFieldAllowed(modelID, model, "", field, true, predicate) {
			return true
		}
	}
	for _, filter := range command.Filters {
		dataset := projectsignals.ValueOrZero(filter.Dataset)
		if dataset == "" {
			dataset = projectsignals.ValueOrZero(command.DatasetID)
		}
		metric := false
		if _, ok := model.Metrics[filter.Field]; ok {
			metric = true
		}
		if !explorerFieldAllowed(modelID, model, dataset, filter.Field, metric, predicate) {
			return true
		}
	}
	for _, sortSpec := range command.Sort {
		metric := false
		if _, ok := model.Metrics[sortSpec.Field]; ok {
			metric = true
		}
		if !explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), sortSpec.Field, metric, predicate) {
			return true
		}
	}
	if command.Time != nil && !explorerFieldAllowed(modelID, model, projectsignals.ValueOrZero(command.DatasetID), command.Time.Field, false, predicate) {
		return true
	}
	if command.ColumnWidths != nil {
		dataset := projectsignals.ValueOrZero(command.DatasetID)
		for field := range *command.ColumnWidths {
			metric := false
			if _, ok := model.Metrics[field]; ok {
				metric = true
			}
			if !explorerFieldAllowed(modelID, model, dataset, field, metric, predicate) {
				return true
			}
		}
	}
	return false
}

func clearExplorerCommandTargets(command projectsignals.DataExploreCommand) projectsignals.DataExploreCommand {
	command.ModelID = nil
	command.DatasetID = nil
	command.ColumnWidths = nil
	command.Dimensions = []string{}
	command.Filters = []projectsignals.DataExploreFilterSignal{}
	command.Metrics = []string{}
	command.Sort = []projectsignals.DataExploreSortSignal{}
	command.Time = nil
	return command
}
