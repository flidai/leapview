package runtimefactory

import (
	"context"

	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type managedDataResolver struct {
	resolver  ManagedDataSource
	projectID projectgraph.ResourceID
	env       servingstate.Environment
}

type ManagedDataSource interface {
	ResolveManagedData(context.Context, projectgraph.ServingIdentity) (manageddataresolver.Resolution, error)
}

func NewManagedDataResolver(resolver ManagedDataSource, projectID projectgraph.ResourceID, environment servingstate.Environment) runtimehost.ManagedDataResolver {
	if resolver == nil {
		return nil
	}
	return managedDataResolver{resolver: resolver, projectID: projectID, env: environment}
}

func (r managedDataResolver) ResolveManagedData(ctx context.Context, id servingstate.ID) (runtimehost.ManagedDataResolution, error) {
	identity, err := projectgraph.NewServingIdentity(r.projectID, string(r.env), string(id))
	if err != nil {
		return runtimehost.ManagedDataResolution{}, err
	}
	resolved, err := r.resolver.ResolveManagedData(ctx, identity)
	if err != nil {
		return runtimehost.ManagedDataResolution{}, err
	}
	roots := make(map[string]string, len(resolved.Roots))
	for connectionID, root := range resolved.Roots {
		roots[connectionID.String()] = root
	}
	return runtimehost.ManagedDataResolution{
		RevisionID: resolved.RevisionID,
		Roots:      roots,
		Lifetime:   resolved.Lifetime,
	}, nil
}
