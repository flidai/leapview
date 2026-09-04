package module

import (
	"context"
	"errors"
	"fmt"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

var errActiveProjectDefinitionUnavailable = errors.New("active project definition is unavailable")

// ProjectDefinitionReader exposes one coherent snapshot retained by the exact
// active serving generation. The manifest and all activation-owned semantic
// planners are collected while one runtime lease is held, so consumers cannot
// combine definitions from different generations during a cutover.
type ProjectDefinitionReader interface {
	ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error)
}

type activeProjectDefinitionReader struct {
	provider projectruntime.Provider
}

// NewActiveProjectDefinitionReader creates a lease-pinned reader for browser
// projections that require more than the portable serving graph metadata.
func NewActiveProjectDefinitionReader(provider projectruntime.Provider) ProjectDefinitionReader {
	return activeProjectDefinitionReader{provider: provider}
}

func (r activeProjectDefinitionReader) ProjectDefinitionSnapshot(ctx context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	if r.provider == nil {
		return projectmanifest.Project{}, nil, fmt.Errorf("%w: runtime provider is missing", errActiveProjectDefinitionUnavailable)
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return projectmanifest.Project{}, nil, err
	}
	defer lease.Release()
	runtime := lease.Runtime()
	manifestPort, ok := runtime.(interface {
		ProjectManifest() projectmanifest.Project
	})
	if !ok {
		return projectmanifest.Project{}, nil, fmt.Errorf("%w: active runtime has no project manifest", errActiveProjectDefinitionUnavailable)
	}
	definition := manifestPort.ProjectManifest()
	if definition.ID == "" {
		return projectmanifest.Project{}, nil, fmt.Errorf("%w: project manifest identity is empty", errActiveProjectDefinitionUnavailable)
	}
	compiled := make(map[string]*semanticquery.CompiledModel, len(definition.SemanticModels))
	if len(definition.SemanticModels) == 0 {
		return definition, compiled, nil
	}
	plannerPort, ok := runtime.(interface {
		CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
	})
	if !ok {
		return projectmanifest.Project{}, nil, fmt.Errorf("%w: active runtime has no compiled semantic models", errActiveProjectDefinitionUnavailable)
	}
	for modelID, model := range definition.SemanticModels {
		if model == nil {
			return projectmanifest.Project{}, nil, fmt.Errorf("%w: semantic model %q is nil", errActiveProjectDefinitionUnavailable, modelID)
		}
		compiledModel, available := plannerPort.CompiledSemanticModel(modelID)
		if !available || compiledModel == nil || !compiledModel.MatchesModel(model) {
			return projectmanifest.Project{}, nil, fmt.Errorf("%w: semantic model %q does not match its compiled planner", errActiveProjectDefinitionUnavailable, modelID)
		}
		compiled[modelID] = compiledModel
	}
	return definition, compiled, nil
}
