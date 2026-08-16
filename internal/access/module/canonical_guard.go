package module

import (
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/snapshot"
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
