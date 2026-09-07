package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

// BoundProjectDefinitionReader is the optional stronger browser boundary. It
// returns the definition, compiled models, serving identity, and authorization
// control revision captured by the same active lease.
type BoundProjectDefinitionReader interface {
	ProjectDefinitionSnapshotBound(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, projectgraph.ServingIdentity, access.AuthorizationControlRevision, error)
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
		return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return projectmanifest.Project{}, nil, err
	}
	defer lease.Release()
	definition, compiled, err := projectDefinitionFromLease(lease)
	if err != nil {
		return projectmanifest.Project{}, nil, err
	}
	return definition, compiled, nil
}

func (r activeProjectDefinitionReader) ProjectDefinitionSnapshotBound(ctx context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, projectgraph.ServingIdentity, access.AuthorizationControlRevision, error) {
	if r.provider == nil {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, errActiveProjectDefinitionUnavailable
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, err
	}
	defer lease.Release()
	authorizationLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, errActiveProjectDefinitionUnavailable
	}
	identity := lease.Identity()
	snapshot := authorizationLease.AuthorizationSnapshot()
	if identity.Validate() != nil || snapshot.Identity() != identity || snapshot.ValidateBound() != nil {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, errActiveProjectDefinitionUnavailable
	}
	control := snapshot.AuthorizationControlRevision()
	if control.Validate() != nil || control.ProjectID != identity.ProjectID.String() {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, errActiveProjectDefinitionUnavailable
	}
	definition, compiled, err := projectDefinitionFromLease(lease)
	if err != nil {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, err
	}
	if definition.ID != identity.ProjectID.String() {
		return projectmanifest.Project{}, nil, projectgraph.ServingIdentity{}, access.AuthorizationControlRevision{}, errActiveProjectDefinitionUnavailable
	}
	return definition, compiled, identity, control, nil
}

func projectDefinitionFromLease(lease projectruntime.Lease) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	if lease == nil {
		return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
	}
	runtime := lease.Runtime()
	manifestPort, ok := runtime.(interface {
		ProjectManifest() projectmanifest.Project
	})
	if !ok {
		return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
	}
	definition := manifestPort.ProjectManifest()
	if definition.ID == "" {
		return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
	}
	compiled := make(map[string]*semanticquery.CompiledModel, len(definition.SemanticModels))
	if len(definition.SemanticModels) == 0 {
		return definition, compiled, nil
	}
	plannerPort, ok := runtime.(interface {
		CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
	})
	if !ok {
		return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
	}
	for modelID, model := range definition.SemanticModels {
		if model == nil {
			return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
		}
		compiledModel, available := plannerPort.CompiledSemanticModel(modelID)
		if !available || compiledModel == nil || !compiledModel.MatchesModel(model) {
			return projectmanifest.Project{}, nil, errActiveProjectDefinitionUnavailable
		}
		compiled[modelID] = compiledModel
	}
	return definition, compiled, nil
}
