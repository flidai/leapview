package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// currentTargetAuthorizationFilter intersects a generation's captured roles
// with the target-owned current policy at every active lease acquisition.
// Current additions never enter the old generation; removals and changed
// authority stop taking effect as soon as the current head advances.
func currentTargetAuthorizationFilter(reader access.AuthorizationPolicyReader, targetID string, projectID projectgraph.ResourceID, environment string) func(context.Context, accesssnapshot.AuthorizationSnapshot) (accesssnapshot.AuthorizationSnapshot, error) {
	return func(ctx context.Context, captured accesssnapshot.AuthorizationSnapshot) (accesssnapshot.AuthorizationSnapshot, error) {
		if reader == nil {
			return accesssnapshot.AuthorizationSnapshot{}, errors.New("current authorization policy reader is unavailable")
		}
		identity := captured.Identity()
		// A fresh target has no claimed Project when composition runs. The
		// runtime host binds it at first activation; the lease supplies that
		// validated identity. A pre-existing claim is checked here as well.
		if (projectID != "" && identity.ProjectID != projectID) || identity.Environment != environment {
			return accesssnapshot.AuthorizationSnapshot{}, errors.New("active authorization snapshot is outside the bound project/environment")
		}
		scope := access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment}
		current, err := reader.AuthorizationPolicy(ctx, scope)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("read current target authorization policy: %w", err)
		}
		if current.Scope != scope || current.Revision <= 0 {
			return accesssnapshot.AuthorizationSnapshot{}, errors.New("current authorization policy identity is invalid")
		}
		digest, err := access.AuthorizationPolicyDigest(scope, current.RoleBindings)
		if err != nil || digest != current.Digest {
			return accesssnapshot.AuthorizationSnapshot{}, errors.New("current authorization policy digest is invalid")
		}
		return captured.RestrictToCurrentRoleBindings(current.RoleBindings)
	}
}
