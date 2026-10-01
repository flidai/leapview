package access

import (
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestExpandPermissionRoleIsProfilePinnedAndExact(t *testing.T) {
	pairs, err := ExpandPermissionRole(PermissionRoleViewer, graph.ResourceID("project_demo"))
	require.NoError(t, err)
	require.NotEmpty(t, pairs)
	require.Equal(t, PermissionCatalogProfile, pairs[0].Profile)
	require.NoError(t, ValidateTypedPermissionSet(PermissionCatalogProfile, pairs))

	binding, err := NewTypedRoleBinding("binding", "viewer", SubjectRef{Kind: SubjectKindPrincipal, ID: "alice"}, PermissionRoleViewer, graph.ResourceID("project_demo"))
	require.NoError(t, err)
	require.Equal(t, PermissionRoleViewer, binding.PermissionRole)
	require.Nil(t, binding.Capabilities)
	require.NoError(t, ValidateTypedRoleBindingForProject(binding, graph.ResourceID("project_demo")))

	binding.Permissions[0].Action = ActionDashboardUpdate
	require.Error(t, ValidateTypedRoleBindingForProject(binding, graph.ResourceID("project_demo")))
}

func TestTypedRoleBindingDoesNotAcceptLegacyRoleOrCapabilities(t *testing.T) {
	binding := RoleBinding{ID: "binding", Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "alice"}, Role: ProjectRoleViewer,
		PermissionRole: PermissionRoleViewer, PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{}}
	require.Error(t, ValidateTypedRoleBinding(binding))
	binding.Role = ""
	binding.Capabilities = []Capability{CapabilityResourceRead}
	require.Error(t, ValidateTypedRoleBinding(binding))
}

func TestAuthorizationPolicyDigestUsesTypedPermissionRoleKey(t *testing.T) {
	scope := AuthorizationPolicyScope{TargetID: "target", ProjectID: "project_demo", Environment: "production"}
	subject := SubjectRef{Kind: SubjectKindPrincipal, ID: "alice"}
	viewer, err := NewTypedRoleBinding("viewer-a", "viewer", subject, PermissionRoleViewer, graph.ResourceID(scope.ProjectID))
	require.NoError(t, err)
	duplicate := viewer
	duplicate.ID = "viewer-b"
	_, err = AuthorizationPolicyDigest(scope, []RoleBinding{viewer, duplicate})
	require.Error(t, err, "two typed bindings for the same subject and PermissionRole must be rejected")

	explorer, err := NewTypedRoleBinding("explorer", "explorer", subject, PermissionRoleExplorer, graph.ResourceID(scope.ProjectID))
	require.NoError(t, err)
	_, err = AuthorizationPolicyDigest(scope, []RoleBinding{viewer, explorer})
	require.NoError(t, err, "different typed PermissionRoles must have distinct digest keys")
}
