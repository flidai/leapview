package module

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type grantRouteOperations struct {
	descriptors map[access.OperationID]access.OperationDescriptor
}

func (o grantRouteOperations) DescribeOperation(id access.OperationID) (access.OperationDescriptor, bool) {
	descriptor, ok := o.descriptors[id]
	return descriptor, ok
}

func (grantRouteOperations) CreateGrant(context.Context, access.GrantInvocation, access.GrantInput) (access.Grant, error) {
	return access.Grant{}, nil
}

func (grantRouteOperations) UpdateGrant(context.Context, access.GrantInvocation, string, string, access.GrantInput) (access.Grant, error) {
	return access.Grant{}, nil
}

func (grantRouteOperations) DeleteGrant(context.Context, access.GrantInvocation, string, string) (access.Grant, error) {
	return access.Grant{}, nil
}

func TestGrantRoutePrivilegesComeFromOperationContracts(t *testing.T) {
	operations := grantRouteOperations{descriptors: map[access.OperationID]access.OperationDescriptor{
		access.OperationCreateGrant: grantRouteDescriptor(access.OperationCreateGrant, access.PrivilegeManageGrants),
		access.OperationDeleteGrant: grantRouteDescriptor(access.OperationDeleteGrant, access.PrivilegeManageWorkspace),
	}}

	upsert, remove, err := grantRoutePrivileges(operations)
	if err != nil {
		t.Fatal(err)
	}
	if upsert != access.PrivilegeManageGrants || remove != access.PrivilegeManageWorkspace {
		t.Fatalf("route privileges = (%q, %q), want generated contract values", upsert, remove)
	}
}

func TestGrantRoutePrivilegesRejectMissingOrIncompatibleContracts(t *testing.T) {
	valid := grantRouteDescriptor(access.OperationCreateGrant, access.PrivilegeManageGrants)
	tests := map[string]grantRouteOperations{
		"missing delete": {descriptors: map[access.OperationID]access.OperationDescriptor{
			access.OperationCreateGrant: valid,
		}},
		"create not exposed to UI": {descriptors: map[access.OperationID]access.OperationDescriptor{
			access.OperationCreateGrant: func() access.OperationDescriptor {
				descriptor := valid
				descriptor.ExposedSurfaces = []access.OperationSurface{access.OperationSurfaceAPI}
				return descriptor
			}(),
			access.OperationDeleteGrant: grantRouteDescriptor(access.OperationDeleteGrant, access.PrivilegeManageGrants),
		}},
		"delete has non-workspace target": {descriptors: map[access.OperationID]access.OperationDescriptor{
			access.OperationCreateGrant: valid,
			access.OperationDeleteGrant: func() access.OperationDescriptor {
				descriptor := grantRouteDescriptor(access.OperationDeleteGrant, access.PrivilegeManageGrants)
				descriptor.Target.Type = access.SecurableDashboard
				return descriptor
			}(),
		}},
	}

	for name, operations := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := grantRoutePrivileges(operations)
			if err == nil || !strings.Contains(err.Error(), "workspace UI operation") {
				t.Fatalf("error = %v, want incompatible or missing operation contract", err)
			}
		})
	}
}

func grantRouteDescriptor(id access.OperationID, privilege access.Privilege) access.OperationDescriptor {
	return access.OperationDescriptor{
		ID: id, Kind: access.OperationKindCommand, Owner: "LeapViewAPI.Access",
		Target:    access.OperationTarget{Type: access.SecurableWorkspace, Parameter: "workspace"},
		Privilege: privilege, AuditEvent: "grant.changed",
		ExposedSurfaces: []access.OperationSurface{access.OperationSurfaceAPI, access.OperationSurfaceUI},
	}
}
