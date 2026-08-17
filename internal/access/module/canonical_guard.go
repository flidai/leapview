package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// RequireCapability evaluates one already-resolved subject/resource pair
// against an immutable authorization snapshot. Resolving the lease and
// request-specific resource remains the caller's responsibility.
func RequireCapability(leased snapshot.AuthorizationSnapshot, subject access.SubjectRef, resource access.ResourceRef, capability access.Capability) error {
	allowed, err := leased.Allows(subject, resource, capability)
	if err != nil {
		return err
	}
	if !allowed {
		return access.ErrForbidden
	}
	return nil
}

// ConnectionAuthorizerFromSnapshot adapts the active serving-generation
// snapshot and identity-layer subject resolver to the narrow connection
// authorization port used by managed-data and connection catalog transports.
// Both providers are mandatory: an unavailable snapshot or subject resolver
// fails closed instead of falling back to mutable access tables.
func ConnectionAuthorizerFromSnapshot(
	snapshotProvider func(context.Context) (snapshot.AuthorizationSnapshot, error),
	subjectsProvider func(context.Context, string) ([]access.SubjectRef, error),
) func(context.Context, string, string, string, access.Capability) (bool, error) {
	return func(ctx context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
		if snapshotProvider == nil || subjectsProvider == nil {
			return false, fmt.Errorf("active authorization snapshot is unavailable")
		}
		if principalID == "" {
			return false, nil
		}
		project, err := projectgraph.NewResourceID(projectID)
		if err != nil {
			return false, err
		}
		connection, err := projectgraph.NewResourceID(connectionID)
		if err != nil {
			return false, err
		}
		leased, err := snapshotProvider(ctx)
		if err != nil {
			return false, err
		}
		if leased.Identity().ProjectID != project {
			return false, fmt.Errorf("authorization snapshot project %q does not match requested project %q", leased.Identity().ProjectID, project)
		}
		resource, err := access.NewResourceRef(connection, projectgraph.KindConnection)
		if err != nil {
			return false, err
		}
		subjects, err := subjectsProvider(ctx, principalID)
		if err != nil {
			return false, err
		}
		for _, subject := range subjects {
			if err := subject.Validate(); err != nil {
				return false, err
			}
			allowed, err := leased.Allows(subject, resource, capability)
			if err != nil {
				return false, err
			}
			if allowed {
				return true, nil
			}
		}
		return false, nil
	}
}
