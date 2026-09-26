package access

import (
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestIsProjectClaimBootstrapBindingAcceptsCanonicalBindings(t *testing.T) {
	projectID := projectgraph.ResourceID("project_bootstrap")
	principalID := "principal-bootstrap"
	for _, test := range []struct {
		id   string
		name string
		role PermissionRole
	}{
		{id: BootstrapOwnerBindingID, name: BootstrapOwnerBindingName, role: PermissionRoleProjectAdmin},
		{id: BootstrapEditorBindingID, name: BootstrapEditorBindingName, role: PermissionRoleEditor},
		{id: BootstrapReleaseOperatorBindingID, name: BootstrapReleaseOperatorBindingName, role: PermissionRoleReleaseOperator},
	} {
		t.Run(string(test.role), func(t *testing.T) {
			binding, err := NewTypedRoleBinding(test.id, test.name, SubjectRef{Kind: SubjectKindPrincipal, ID: principalID}, test.role, projectID)
			if err != nil {
				t.Fatal(err)
			}
			if !IsProjectClaimBootstrapBinding(binding, projectID, principalID) {
				t.Fatalf("canonical bootstrap binding rejected: %#v", binding)
			}
		})
	}
}

func TestIsProjectClaimBootstrapBindingRejectsAuthorityChanges(t *testing.T) {
	projectID := projectgraph.ResourceID("project_bootstrap")
	principalID := "principal-bootstrap"
	canonical, err := NewTypedRoleBinding(BootstrapOwnerBindingID, BootstrapOwnerBindingName, SubjectRef{Kind: SubjectKindPrincipal, ID: principalID}, PermissionRoleProjectAdmin, projectID)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*RoleBinding)
	}{
		{name: "wrong subject kind", mutate: func(binding *RoleBinding) { binding.Subject = SubjectRef{Kind: SubjectKindGroup, ID: principalID} }},
		{name: "wrong subject", mutate: func(binding *RoleBinding) { binding.Subject.ID = "principal-other" }},
		{name: "legacy role", mutate: func(binding *RoleBinding) { binding.Role = ProjectRoleAdmin }},
		{name: "legacy capabilities", mutate: func(binding *RoleBinding) { binding.Capabilities = []Capability{CapabilityProjectAdmin} }},
		{name: "wrong profile", mutate: func(binding *RoleBinding) { binding.PermissionProfile = "other-profile" }},
		{name: "wrong name", mutate: func(binding *RoleBinding) { binding.Name = "Other name" }},
		{name: "wrong role", mutate: func(binding *RoleBinding) { binding.PermissionRole = PermissionRoleEditor }},
		{name: "missing permission", mutate: func(binding *RoleBinding) { binding.Permissions = binding.Permissions[:len(binding.Permissions)-1] }},
		{name: "reordered permissions", mutate: func(binding *RoleBinding) {
			binding.Permissions[0], binding.Permissions[1] = binding.Permissions[1], binding.Permissions[0]
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			binding := canonical
			binding.Permissions = ClonePermissionPairs(canonical.Permissions)
			test.mutate(&binding)
			if IsProjectClaimBootstrapBinding(binding, projectID, principalID) {
				t.Fatalf("mutated bootstrap binding accepted: %#v", binding)
			}
		})
	}
}
