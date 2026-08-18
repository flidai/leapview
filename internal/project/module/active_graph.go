package module

import (
	"context"
	"fmt"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ServingStateGraphReader reads a graph for an exact serving-state identity.
// It deliberately does not expose an active-pointer lookup so browser assets
// remain pinned to the runtime lease that served the request.
type ServingStateGraphReader interface {
	ServingStateGraph(context.Context, projectgraph.ResourceID, string, servingstate.ID) (servingstate.AssetGraph, bool, error)
}

type activeServingStateGraphReader struct {
	provider projectruntime.Provider
	graphs   ServingStateGraphReader
}

// NewActiveServingStateGraphReader binds browser graph reads to the exact
// generation held by the active runtime lease. The durable graph repository
// remains read-only; the runtime lease supplies the authoritative generation
// identity instead of a legacy active-scope pointer.
func NewActiveServingStateGraphReader(provider projectruntime.Provider, graphs ServingStateGraphReader) interface {
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
} {
	return activeServingStateGraphReader{provider: provider, graphs: graphs}
}

func (r activeServingStateGraphReader) ActiveServingStateGraph(ctx context.Context, projectID projectgraph.ResourceID, environment string) (servingstate.AssetGraph, bool, error) {
	if r.provider == nil || r.graphs == nil {
		return servingstate.AssetGraph{}, false, fmt.Errorf("active serving graph runtime is unavailable")
	}
	lease, err := r.provider.Acquire(ctx)
	if err != nil {
		return servingstate.AssetGraph{}, false, err
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return servingstate.AssetGraph{}, false, fmt.Errorf("active serving graph identity is invalid: %w", err)
	}
	if identity.ProjectID != projectID || identity.Environment != environment {
		return servingstate.AssetGraph{}, false, fmt.Errorf("active serving graph scope %q/%q does not match requested %q/%q", identity.ProjectID, identity.Environment, projectID, environment)
	}
	return r.graphs.ServingStateGraph(ctx, projectID, environment, servingstate.ID(identity.GenerationID))
}
