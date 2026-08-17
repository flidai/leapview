package module

import (
	"context"
	"errors"

	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

var errActiveProjectDefinitionUnavailable = errors.New("active project definition is unavailable")

// ProjectDefinitionReader exposes the complete compiled definition retained
// by the exact active serving generation. Consumers use this module-owned port
// instead of depending on project runtime or manifest implementation packages.
type ProjectDefinitionReader interface {
	ProjectDefinition(context.Context) (projectmanifest.Project, error)
}

type activeProjectDefinitionReader struct {
	provider projectruntime.Provider
}

// NewActiveProjectDefinitionReader creates a lease-pinned reader for browser
// projections that require more than the portable serving graph metadata.
func NewActiveProjectDefinitionReader(provider projectruntime.Provider) ProjectDefinitionReader {
	return activeProjectDefinitionReader{provider: provider}
}

func (r activeProjectDefinitionReader) ProjectDefinition(ctx context.Context) (projectmanifest.Project, error) {
	if r.provider == nil {
		return projectmanifest.Project{}, errActiveProjectDefinitionUnavailable
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return projectmanifest.Project{}, err
	}
	defer lease.Release()
	port, ok := lease.Runtime().(interface {
		ProjectManifest() projectmanifest.Project
	})
	if !ok {
		return projectmanifest.Project{}, errActiveProjectDefinitionUnavailable
	}
	definition := port.ProjectManifest()
	if definition.ID == "" {
		return projectmanifest.Project{}, errActiveProjectDefinitionUnavailable
	}
	return definition, nil
}
