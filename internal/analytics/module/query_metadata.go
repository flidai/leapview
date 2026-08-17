package module

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// WithAgentQueryMetadata attaches the governed metadata shared by agent
// semantic queries. Keeping the data-query implementation behind this module
// surface prevents process composition from depending on analytics internals.
func WithAgentQueryMetadata(ctx context.Context, projectID projectgraph.ResourceID, principalID string) context.Context {
	return dataquery.WithMetadata(ctx, dataquery.Metadata{
		ProjectID:   projectID,
		Surface:     dataquery.SurfaceAgent,
		Operation:   dataquery.OperationAgentQuery,
		PrincipalID: principalID,
	})
}
