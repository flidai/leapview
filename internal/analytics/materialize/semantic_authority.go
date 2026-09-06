package materialize

import (
	"context"

	"github.com/flidai/leapview/internal/access"
)

// SemanticAccessAuthority is supplied by Access at composition. It resolves
// authenticated context, never principal IDs or attribute values in a query.
// Registry reads are activation inputs; resolution is execution-local.
type SemanticAccessAuthority interface {
	SemanticAttributeRegistry(context.Context) (access.SemanticAttributeRegistrySnapshot, error)
	ResolveSemanticAttributes(context.Context) (access.SemanticAttributeResolution, error)
}
