package module

import (
	"context"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type bootstrapAuthorizationContextKey struct{}

type publicationApprovalBootstrapAuthorizationContextKey struct{}

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

// PublicationApprovalBootstrapAuthorization is an approval-specific,
// request-local attenuation marker. It is emitted only for a reviewer bearer
// credential that passed the fresh-target approval ingress; downstream approval
// authorization must still load and evaluate the requested generation's
// immutable authorization snapshot. The marker deliberately cannot authorize
// any other operation or capability.
type PublicationApprovalBootstrapAuthorization struct {
	ProjectID   projectgraph.ResourceID
	PrincipalID string
	Capability  access.Capability
}

// withPublicationApprovalBootstrapAuthorization attaches the fixed
// PROJECT_ADMIN approval marker after the APIGen bootstrap gate has admitted
// the exact operation. Keeping minting private prevents downstream callers
// from manufacturing this attenuation.
func withPublicationApprovalBootstrapAuthorization(ctx context.Context, projectID projectgraph.ResourceID, principalID string) context.Context {
	if ctx == nil || projectID.Validate() != nil || strings.TrimSpace(principalID) == "" {
		return ctx
	}
	return context.WithValue(ctx, publicationApprovalBootstrapAuthorizationContextKey{}, PublicationApprovalBootstrapAuthorization{
		ProjectID: projectID, PrincipalID: strings.TrimSpace(principalID), Capability: access.CapabilityProjectAdmin,
	})
}

// PublicationApprovalBootstrapAuthorizationFromContext returns the approval
// marker, if one was attached by the fresh-target APIGen authorization
// boundary. It is exported for the downstream deployment approval adapter;
// callers must not treat it as sufficient authorization without rechecking
// the immutable candidate authorization snapshot.
func PublicationApprovalBootstrapAuthorizationFromContext(ctx context.Context) (PublicationApprovalBootstrapAuthorization, bool) {
	if ctx == nil {
		return PublicationApprovalBootstrapAuthorization{}, false
	}
	marker, ok := ctx.Value(publicationApprovalBootstrapAuthorizationContextKey{}).(PublicationApprovalBootstrapAuthorization)
	if !ok || marker.ProjectID.Validate() != nil || strings.TrimSpace(marker.PrincipalID) == "" || marker.Capability != access.CapabilityProjectAdmin {
		return PublicationApprovalBootstrapAuthorization{}, false
	}
	return marker, true
}
