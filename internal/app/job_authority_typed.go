package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
)

// authorizeCurrentTypedPermission checks an exact queued action-target pair
// against the principal's permissions in the current serving snapshot.
// Caller and delegated jobs deliberately share this path so a legacy
// capability cannot keep work alive after its typed assignment is revoked.
func authorizeCurrentTypedPermission(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	pair access.PermissionPair,
	environment string,
) (bool, error) {
	if accessModule == nil || runtimeHost == nil || strings.TrimSpace(principalID) == "" {
		return false, fmt.Errorf("queued workload authorization modules are required")
	}
	if err := pair.Validate(); err != nil {
		return false, err
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return false, err
	}
	if lease == nil {
		return false, fmt.Errorf("runtime host returned a nil lease")
	}
	defer lease.Release()
	if lease.Identity().ProjectID != pair.Target.ProjectID || lease.Identity().Environment != environment {
		return false, nil
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return false, fmt.Errorf("active runtime lease does not expose authorization snapshot")
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if snapshot.Identity() != lease.Identity() {
		return false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return false, err
	}
	subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	permissions, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, err
	}
	return access.PermissionSetAllows(permissions, pair), nil
}
