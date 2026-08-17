// Package plan turns compiled refresh definitions into deterministic execution plans.
package plan

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
)

type Plan struct {
	TargetType       string
	TargetID         projectgraph.ResourceID
	SemanticModelID  projectgraph.ResourceID
	Tables           []string
	DependencyTables []string
}

func ForPipeline(definition *refreshartifact.Definition, projectID, pipelineID projectgraph.ResourceID) (Plan, error) {
	if definition == nil {
		return Plan{}, fmt.Errorf("project definition is required")
	}
	if err := projectID.Validate(); err != nil {
		return Plan{}, err
	}
	if err := pipelineID.Validate(); err != nil {
		return Plan{}, err
	}
	pipeline, ok := definition.Pipelines[pipelineID.String()]
	if !ok {
		return Plan{}, fmt.Errorf("unknown refresh pipeline %q", pipelineID)
	}
	model, ok := definition.Models[pipeline.SemanticModelID.String()]
	if !ok {
		return Plan{}, fmt.Errorf("refresh pipeline %q references unknown semantic model %q", pipelineID, pipeline.SemanticModelID)
	}
	order, err := modelTableOrder(model)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		TargetType:       "refresh_pipeline",
		TargetID:         pipelineID,
		SemanticModelID:  pipeline.SemanticModelID,
		Tables:           order,
		DependencyTables: append([]string(nil), order...),
	}, nil
}

func modelTableOrder(model *semanticmodel.Model) ([]string, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	// Semantic dataset names are aliases. Refresh execution is project-scoped
	// and therefore orders the physical authored Model tables, deduplicating
	// aliases that point at the same Model.
	physicalTables := map[string]semanticmodel.Table{}
	for _, name := range model.TableNames() {
		physical := name
		if dataset, ok := model.Datasets[name]; ok && dataset.Model != "" {
			physical = dataset.Model
		}
		table := model.Tables[name]
		table.ModelDependencies = append([]string(nil), table.ModelDependencies...)
		for index, dependency := range table.ModelDependencies {
			if dataset, ok := model.Datasets[dependency]; ok && dataset.Model != "" {
				dependency = dataset.Model
			}
			table.ModelDependencies[index] = dependency
		}
		if existing, ok := physicalTables[physical]; ok {
			// Equivalent aliases are expected to share one authored Model. Keep
			// the first deterministic projection; runtime conflict checks enforce
			// physical signature equality across semantic models.
			if len(existing.ModelDependencies) == 0 && len(table.ModelDependencies) > 0 {
				physicalTables[physical] = table
			}
			continue
		}
		physicalTables[physical] = table
	}
	temporary := map[string]bool{}
	permanent := map[string]bool{}
	order := make([]string, 0, len(model.Tables))
	var visit func(string) error
	visit = func(name string) error {
		if permanent[name] {
			return nil
		}
		if temporary[name] {
			return fmt.Errorf("model table dependency cycle includes %q", name)
		}
		table, ok := physicalTables[name]
		if !ok {
			return fmt.Errorf("unknown model table %q", name)
		}
		temporary[name] = true
		for _, dependency := range table.ModelDependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(temporary, name)
		permanent[name] = true
		order = append(order, name)
		return nil
	}
	names := make([]string, 0, len(physicalTables))
	for name := range physicalTables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
