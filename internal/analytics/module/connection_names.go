package module

import (
	"context"
	"maps"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// connectionNameResolver adapts authored SQL aliases to the identity-only
// resolver selected for this runtime. Resource IDs are never inferred from
// names, and a missing mapping never falls back to another runtime's evidence.
func connectionNameResolver(resolver analyticsruntime.ConnectionResolver, ids map[string]string) analyticsruntime.ConnectionResolver {
	if resolver == nil {
		return nil
	}
	for name, id := range ids {
		if name == "" || name != strings.TrimSpace(name) || projectgraph.ResourceID(id).Validate() != nil {
			return unavailableConnectionResolver{}
		}
	}
	return &compiledConnectionNameResolver{resolver: resolver, ids: maps.Clone(ids)}
}

type compiledConnectionNameResolver struct {
	resolver analyticsruntime.ConnectionResolver
	ids      map[string]string
}

func (r *compiledConnectionNameResolver) Resolve(ctx context.Context, name string, connection semanticmodel.Connection) (semanticmodel.Connection, error) {
	id, ok := r.ids[name]
	if !ok {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	return r.resolver.Resolve(ctx, id, connection)
}
