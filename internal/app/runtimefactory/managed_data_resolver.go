package runtimefactory

import (
	"context"

	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

type managedDataResolver struct {
	resolver ManagedDataSource
}

type ManagedDataSource interface {
	ResolveManagedData(context.Context, projectgraph.ServingIdentity) (manageddataresolver.Resolution, error)
}

func NewManagedDataResolver(resolver ManagedDataSource) runtimehost.ManagedDataResolver {
	if resolver == nil {
		return nil
	}
	return managedDataResolver{resolver: resolver}
}

func (r managedDataResolver) ResolveManagedDataForIdentity(ctx context.Context, identity projectgraph.ServingIdentity) (runtimehost.ManagedDataResolution, error) {
	if err := identity.Validate(); err != nil {
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
	revisions := make(map[string]string, len(resolved.Revisions))
	for connectionID, revision := range resolved.Revisions {
		revisions[connectionID.String()] = revision
	}
	return runtimehost.ManagedDataResolution{
		RevisionID: resolved.RevisionID,
		Roots:      roots,
		Revisions:  revisions,
		Lifetime:   resolved.Lifetime,
	}, nil
}
