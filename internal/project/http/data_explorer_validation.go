package http

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// validateExplorerFilterDatasets constrains explicit filter scopes to the
// datasets that actually participate in this query. The planner also checks
// these bindings, but doing it here prevents an unauthorized or unrelated
// scope from reaching the executor and makes the HTTP contract deterministic.
func validateExplorerFilterDatasets(spec exploration.ExplorationSpec, fields map[string]projectsignals.DataExploreFieldSignal, compiled *semanticquery.CompiledModel) error {
	state := dataExploreStateFromSpec(spec)
	participating := map[string]bool{}
	if explorerCommandHasMultiRootMetric(state.Metrics, fields) {
		for _, metric := range state.Metrics {
			compiledMetric, ok := compiled.Metric(metric)
			if !ok {
				continue
			}
			for _, root := range compiledMetric.RootDatasets {
				participating[root] = true
			}
		}
	} else if target := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID)); target != "" {
		participating[target] = true
	}
	for index, authored := range spec.Filters {
		dataset := strings.TrimSpace(projectsignals.ValueOrZero(authored.DatasetID))
		if dataset == "" {
			continue
		}
		if compiled == nil {
			return fmt.Errorf("filter %d cannot be validated because the active compiled semantic model is unavailable", index+1)
		}
		if _, ok := compiled.Dataset(dataset); !ok {
			return fmt.Errorf("filter %d dataset %q is unavailable", index+1, dataset)
		}
		if !participating[dataset] {
			return fmt.Errorf("filter %d dataset %q does not participate in the exploration", index+1, dataset)
		}
		field, ok := fields[authored.Field]
		if !ok || field.Kind != "dimension" || !field.Compatible {
			return fmt.Errorf("filter %d field %q is unavailable", index+1, authored.Field)
		}
		if _, ok := compiled.FieldBinding(dataset, authored.Field); ok {
			continue
		}
		if _, ok := compiled.DimensionBinding(authored.Field, dataset); !ok {
			return fmt.Errorf("filter %d field %q cannot be reached from dataset %q", index+1, authored.Field, dataset)
		}
	}
	return nil
}
