package runtimefactory

import (
	"context"

	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
)

type managedDataResolver struct {
	resolver ManagedDataSource
}

type ManagedDataSource interface {
	ResolveManagedData(context.Context, servingstate.ID) (manageddataresolver.Resolution, error)
}

func NewManagedDataResolver(resolver ManagedDataSource) runtimehost.ManagedDataResolver {
	if resolver == nil {
		return nil
	}
	return managedDataResolver{resolver: resolver}
}

func (r managedDataResolver) ResolveManagedData(ctx context.Context, id servingstate.ID) (runtimehost.ManagedDataResolution, error) {
	resolved, err := r.resolver.ResolveManagedData(ctx, id)
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
