package http

import (
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectview "github.com/flidai/leapview/internal/project"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type explorerSemanticAccess struct {
	datasets   map[string]bool
	dimensions map[string]map[string]bool
	metrics    map[string]bool
}

func explorerSemanticAccessForModel(model *semanticmodel.Model, compiled *semanticquery.CompiledModel, consumer *semanticquery.SemanticAccessConsumer, modelID string) (*explorerSemanticAccess, bool) {
	if model == nil || model.AccessPolicy.Empty() {
		return nil, true
	}
	if consumer == nil || consumer.ModelID() != modelID || compiled == nil || len(compiled.DatasetNames()) == 0 {
		return nil, false
	}
	planner := consumer.Planner()
	if planner == nil || planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model) {
		return nil, false
	}
	assets, err := consumer.Assets()
	if err != nil {
		return nil, false
	}
	allowed := &explorerSemanticAccess{datasets: map[string]bool{}, dimensions: map[string]map[string]bool{}, metrics: map[string]bool{}}
	for _, asset := range assets {
		switch asset.Kind {
		case "dataset":
			allowed.datasets[asset.Name] = true
		case "dimension":
			if allowed.dimensions[asset.Name] == nil {
				allowed.dimensions[asset.Name] = map[string]bool{}
			}
			allowed.dimensions[asset.Name][asset.Dataset] = true
		case "metric":
			allowed.metrics[asset.Name] = true
		}
	}
	// A valid consumer can deny every dataset. Do not advertise the model
	// when none of its governed data can be selected.
	return allowed, len(allowed.datasets) != 0
}

func explorerBoundModelObjects(asset projectview.DevelopAssetView, table semanticmodel.Table, columns []projectsignals.DataPreviewColumnSignal, bindings []explorerModelBinding, accessByID map[string]*explorerSemanticAccess, compiledByID map[string]*semanticquery.CompiledModel) []projectsignals.DataExplorerObjectSignal {
	if len(bindings) == 0 {
		return []projectsignals.DataExplorerObjectSignal{explorerModelObject(asset, table, columns, "", "")}
	}
	objects := make([]projectsignals.DataExplorerObjectSignal, 0, len(bindings))
	for _, binding := range bindings {
		bindingColumns := columns
		if access := accessByID[binding.SemanticModelID]; access != nil {
			bindingColumns = explorerAuthorizedTableColumns(table, compiledByID[binding.SemanticModelID], binding.DatasetID, access)
		}
		objects = append(objects, explorerModelObject(asset, table, bindingColumns, binding.SemanticModelID, binding.DatasetID))
	}
	return objects
}

func explorerBindingWarnings(unavailable bool) []string {
	if !unavailable {
		return nil
	}
	return []string{"Compiled semantic dataset bindings are unavailable for the active serving generation."}
}

func (access *explorerSemanticAccess) allowsDataset(name string) bool {
	return access == nil || access.datasets[name]
}

func (access *explorerSemanticAccess) allowsDimension(name, dataset, field string) bool {
	if access == nil {
		return true
	}
	return access.dimensions[name][dataset]
}

func (access *explorerSemanticAccess) allowsMetric(name string) bool {
	return access == nil || access.metrics[name]
}

// allowsPhysicalField maps an authorized semantic dimension back to the
// physical field displayed in a dataset's schema. A protected projection must
// never fall back to exposing every authored table column: a physical column
// is visible only when every matching semantic binding is authorized.
func (access *explorerSemanticAccess) allowsPhysicalField(compiled *semanticquery.CompiledModel, dataset, field string) bool {
	if access == nil {
		return true
	}
	qualified := dataset + "." + field
	matched := false
	for _, name := range compiled.SemanticDimensionNames() {
		binding, ok := compiled.DimensionBinding(name, dataset)
		if !ok {
			continue
		}
		physical := strings.TrimSpace(binding.Physical.Field)
		if physical != qualified && physical != field && strings.TrimSpace(binding.Physical.Name) != field {
			continue
		}
		matched = true
		if !access.allowsDimension(name, dataset, physical) {
			return false
		}
	}
	return matched
}

func explorerDatasetEntitiesAuthorized(table semanticmodel.Table, compiled *semanticquery.CompiledModel, dataset string, access *explorerSemanticAccess) ([]projectsignals.SemanticModelGraphEntitySignal, string, []string) {
	entities := make([]projectsignals.SemanticModelGraphEntitySignal, 0, len(table.Entities))
	names := make([]string, 0, len(table.Entities))
	for name := range table.Entities {
		names = append(names, name)
	}
	sort.Strings(names)
	grainEntity := strings.TrimSpace(table.GrainEntity)
	grainFields := []string{}
	for _, name := range names {
		entity := table.Entities[name]
		fields := make([]string, 0, len(entity.Fields))
		allVisible := true
		for _, field := range entity.Fields {
			if access.allowsPhysicalField(compiled, dataset, field) {
				fields = append(fields, field)
				continue
			}
			allVisible = false
		}
		if !allVisible || len(fields) == 0 {
			continue
		}
		entities = append(entities, projectsignals.SemanticModelGraphEntitySignal{
			Name: name, Type: entity.Type, Fields: fields, Grain: projectsignals.Optional(name == grainEntity),
		})
		if name == grainEntity {
			grainFields = append([]string(nil), fields...)
		}
	}
	if len(grainFields) == 0 {
		grainEntity = ""
	}
	return entities, grainEntity, grainFields
}

func explorerAuthorizedTableColumns(table semanticmodel.Table, compiled *semanticquery.CompiledModel, dataset string, access *explorerSemanticAccess) []projectsignals.DataPreviewColumnSignal {
	columns := explorerTableColumns(table)
	if access == nil {
		return columns
	}
	out := make([]projectsignals.DataPreviewColumnSignal, 0, len(columns))
	for _, column := range columns {
		if access.allowsPhysicalField(compiled, dataset, column.Key) {
			out = append(out, column)
		}
	}
	return out
}
