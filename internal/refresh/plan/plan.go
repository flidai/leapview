// Package plan turns compiled refresh definitions into deterministic execution plans.
package plan

import (
	"fmt"

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
		table, ok := model.Tables[name]
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
	for _, name := range model.TableNames() {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
