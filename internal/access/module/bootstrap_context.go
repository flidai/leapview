package module

import (
	"context"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type bootstrapAuthorizationContextKey struct{}

type publicationApprovalBootstrapAuthorizationContextKey struct{}

type accessTransitionApprovalAuthorizationContextKey struct{}

type managedDataStagingAuthorizationContextKey struct{}

// BootstrapAuthorization is an opaque, request-local authorization marker
// emitted only after the strict bootstrap request checks have passed. It binds
// downstream authorization to the exact project, principal, and capability
// that were admitted before dispatch.
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

// ManagedDataStagingAuthorization is minted only after the access boundary
// proves that the requested connection is absent from the active predecessor
// graph (or that no generation is active) and validates the exact project
// claim plus scoped authoring credential. Binding the connection prevents the
// marker from authorizing another managed-data target downstream.
type ManagedDataStagingAuthorization struct {
	ProjectID    projectgraph.ResourceID
	ConnectionID projectgraph.ResourceID
	PrincipalID  string
	Capability   access.Capability
}

func withManagedDataStagingAuthorization(ctx context.Context, projectID, connectionID projectgraph.ResourceID, principalID string, capability access.Capability) context.Context {
	if ctx == nil || projectID.Validate() != nil || connectionID.Validate() != nil || strings.TrimSpace(principalID) == "" || capability.Validate() != nil {
		return ctx
	}
	return context.WithValue(ctx, managedDataStagingAuthorizationContextKey{}, ManagedDataStagingAuthorization{
		ProjectID: projectID, ConnectionID: connectionID, PrincipalID: strings.TrimSpace(principalID), Capability: capability,
	})
}

// ManagedDataStagingAuthorizationFromContext returns the exact request-local
// staging marker for the managed-data handler authorization adapter.
func ManagedDataStagingAuthorizationFromContext(ctx context.Context) (ManagedDataStagingAuthorization, bool) {
	if ctx == nil {
		return ManagedDataStagingAuthorization{}, false
	}
	marker, ok := ctx.Value(managedDataStagingAuthorizationContextKey{}).(ManagedDataStagingAuthorization)
	if !ok || marker.ProjectID.Validate() != nil || marker.ConnectionID.Validate() != nil || strings.TrimSpace(marker.PrincipalID) == "" || marker.Capability.Validate() != nil {
		return ManagedDataStagingAuthorization{}, false
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

// AccessTransitionApprovalAuthorization is a one-invocation approval scope for
// the admitted legacy-to-typed maintenance transition. It binds the operation
// to the predecessor generation, the exact newly captured generation and its
// immutable typed-policy snapshot, plus distinct publisher/reviewer identities.
// It cannot authorize requests outside publication approval.
type AccessTransitionApprovalAuthorization struct {
	TargetID                   string
	ProjectID                  projectgraph.ResourceID
	Environment                string
	ExpectedActiveGenerationID string
	CandidateID                string
	CandidateGenerationID      string
	PublicationID              string
	PublisherPrincipalID       string
	ReviewerPrincipalID        string
	IntentDigest               string
	CandidateSnapshotDigest    string
}

// WithAccessTransitionApprovalAuthorization attaches the narrowly scoped
// offline transition evidence. The app composition calls it only after the
// host maintenance fence, typed policy CAS and native candidate plan have
// each been revalidated. Normal HTTP authorization does not call this helper.
func WithAccessTransitionApprovalAuthorization(ctx context.Context, marker AccessTransitionApprovalAuthorization) (context.Context, error) {
	if ctx == nil || strings.TrimSpace(marker.TargetID) == "" || marker.ProjectID.Validate() != nil || strings.TrimSpace(marker.Environment) == "" ||
		strings.TrimSpace(marker.ExpectedActiveGenerationID) == "" || strings.TrimSpace(marker.CandidateID) == "" || strings.TrimSpace(marker.CandidateGenerationID) == "" || strings.TrimSpace(marker.PublicationID) == "" || marker.ExpectedActiveGenerationID == marker.CandidateGenerationID ||
		strings.TrimSpace(marker.PublisherPrincipalID) == "" || strings.TrimSpace(marker.ReviewerPrincipalID) == "" || marker.PublisherPrincipalID == marker.ReviewerPrincipalID ||
		!isCanonicalSHA256Digest(marker.IntentDigest) || !isCanonicalSHA256Digest(marker.CandidateSnapshotDigest) {
		return ctx, falseAccessTransitionApproval()
	}
	return context.WithValue(ctx, accessTransitionApprovalAuthorizationContextKey{}, marker), nil
}

// AccessTransitionApprovalAuthorizationFromContext returns a valid, exact
// transition approval scope, if one was attached by offline composition.
func AccessTransitionApprovalAuthorizationFromContext(ctx context.Context) (AccessTransitionApprovalAuthorization, bool) {
	if ctx == nil {
		return AccessTransitionApprovalAuthorization{}, false
	}
	marker, ok := ctx.Value(accessTransitionApprovalAuthorizationContextKey{}).(AccessTransitionApprovalAuthorization)
	if !ok || strings.TrimSpace(marker.TargetID) == "" || marker.ProjectID.Validate() != nil || strings.TrimSpace(marker.Environment) == "" ||
		strings.TrimSpace(marker.ExpectedActiveGenerationID) == "" || strings.TrimSpace(marker.CandidateID) == "" || strings.TrimSpace(marker.CandidateGenerationID) == "" || strings.TrimSpace(marker.PublicationID) == "" || marker.ExpectedActiveGenerationID == marker.CandidateGenerationID ||
		strings.TrimSpace(marker.PublisherPrincipalID) == "" || strings.TrimSpace(marker.ReviewerPrincipalID) == "" || marker.PublisherPrincipalID == marker.ReviewerPrincipalID ||
		!isCanonicalSHA256Digest(marker.IntentDigest) || !isCanonicalSHA256Digest(marker.CandidateSnapshotDigest) {
		return AccessTransitionApprovalAuthorization{}, false
	}
	return marker, true
}

func falseAccessTransitionApproval() error {
	return errors.New("invalid offline access-transition approval scope")
}

func isCanonicalSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
