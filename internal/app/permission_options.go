package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
)

type permissionOptionAuthority interface {
	AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error)
	IsPlatformAdmin(context.Context, string) (bool, error)
}

// currentEffectivePermissionOptions combines the two independent durable
// authority planes used by token issuance. Project and resource permissions
// come from the immutable serving snapshot; instance permissions come from
// the durable platform role and remain available without an active project.
func currentEffectivePermissionOptions(
	ctx context.Context,
	authority permissionOptionAuthority,
	principalID string,
	instanceID string,
	snapshot func(context.Context) (accesssnapshot.AuthorizationSnapshot, error),
) ([]access.PermissionPair, error) {
	platformAdmin, err := authority.IsPlatformAdmin(ctx, principalID)
	if err != nil {
		return nil, err
	}
	instanceOptions := []access.PermissionPair{}
	if platformAdmin {
		instanceOptions, err = access.InstancePermissionOptions(instanceID)
		if err != nil {
			return nil, err
		}
	}
	projectSnapshot, err := snapshot(ctx)
	if err != nil {
		if platformAdmin {
			return instanceOptions, nil
		}
		return nil, err
	}
	subjects, err := authority.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return nil, err
	}
	projectOptions, err := projectSnapshot.EffectiveTypedPermissionOptions(subjects)
	if err != nil {
		return nil, err
	}
	return append(projectOptions, instanceOptions...), nil
}
