package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type grantRouteOperations struct{}

func (grantRouteOperations) CreateGrant(context.Context, access.GrantInvocation, access.GrantInput) (access.Grant, error) {
	return access.Grant{}, nil
}

func (grantRouteOperations) UpdateGrant(context.Context, access.GrantInvocation, string, string, access.GrantInput) (access.Grant, error) {
	return access.Grant{}, nil
}

func (grantRouteOperations) DeleteGrant(context.Context, access.GrantInvocation, string, string) (access.Grant, error) {
	return access.Grant{}, nil
}

func TestGrantRoutePrivilegesComeDirectlyFromGeneratedContracts(t *testing.T) {
	upsert, remove, err := grantRoutePrivileges(grantRouteOperations{}, access.WorkspaceCommandPrivileges{
		GrantUpsert: access.PrivilegeManageGrants, GrantDelete: access.PrivilegeManageGrants,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upsert != access.PrivilegeManageGrants || remove != access.PrivilegeManageGrants {
		t.Fatalf("route privileges = (%q, %q), want generated contract values", upsert, remove)
	}
}

func TestGrantRoutePrivilegesAllowUnconfiguredCommands(t *testing.T) {
	upsert, remove, err := grantRoutePrivileges(nil, access.WorkspaceCommandPrivileges{})
	if err != nil || upsert != "" || remove != "" {
		t.Fatalf("route privileges = (%q, %q, %v), want empty unconfigured result", upsert, remove, err)
	}
}
