package module

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type bootstrapAuthorizationContextKey struct{}

// BootstrapAuthorization is an opaque, request-local authorization marker
// emitted only after the strict bootstrap request checks have passed. It binds
// downstream managed-data authorization to the exact project, principal, and
// capability that were admitted before dispatch.
type BootstrapAuthorization struct {
	ProjectID   projectgraph.ResourceID
	PrincipalID string
	Capability  access.Capability
}

// withBootstrapAuthorization attaches a validated bootstrap marker to a
// request context. It intentionally remains package-private so only the
// access authorization boundary can mint the marker.
func withBootstrapAuthorization(ctx context.Context, projectID projectgraph.ResourceID, principalID string, capability access.Capability) context.Context {
	if ctx == nil || projectID.Validate() != nil || strings.TrimSpace(principalID) == "" || capability.Validate() != nil {
		return ctx
	}
	return context.WithValue(ctx, bootstrapAuthorizationContextKey{}, BootstrapAuthorization{ProjectID: projectID, PrincipalID: strings.TrimSpace(principalID), Capability: capability})
}

// BootstrapAuthorizationFromContext returns the opaque marker, if one was
// attached by the access authorization boundary.
func BootstrapAuthorizationFromContext(ctx context.Context) (BootstrapAuthorization, bool) {
	if ctx == nil {
		return BootstrapAuthorization{}, false
	}
	marker, ok := ctx.Value(bootstrapAuthorizationContextKey{}).(BootstrapAuthorization)
	if !ok || marker.ProjectID.Validate() != nil || strings.TrimSpace(marker.PrincipalID) == "" || marker.Capability.Validate() != nil {
		return BootstrapAuthorization{}, false
	}
	return marker, true
}
