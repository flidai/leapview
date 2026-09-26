package module

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func typedAuthorizationPolicyJSON(t *testing.T, projectID projectgraph.ResourceID) (string, access.RoleBinding) {
	t.Helper()
	binding, err := access.NewTypedRoleBinding(
		"binding-viewer",
		"Viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		access.PermissionRoleViewer,
		projectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(projectmanifest.AccessPolicy{RoleBindings: map[string]projectmanifest.RoleBinding{
		binding.ID: {
			ID:                binding.ID,
			Name:              binding.Name,
			Subject:           projectmanifest.Subject{Kind: "principal", PrincipalID: binding.Subject.ID},
			PermissionProfile: binding.PermissionProfile,
			Permissions:       binding.Permissions,
			PermissionRole:    binding.PermissionRole,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded), binding
}

func TestDecodeAuthorizationRoleBindingsJSONPreservesTypedAssignment(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	encoded, want := typedAuthorizationPolicyJSON(t, projectID)

	got, migratable, err := DecodeAuthorizationRoleBindingsJSON(encoded, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !migratable || len(got) != 1 {
		t.Fatalf("decoded typed policy = %#v, migratable=%v; want one binding", got, migratable)
	}
	if got[0].ID != want.ID || got[0].Name != want.Name || got[0].Subject != want.Subject ||
		got[0].PermissionProfile != want.PermissionProfile || got[0].PermissionRole != want.PermissionRole ||
		got[0].Role != "" || got[0].Capabilities != nil {
		t.Fatalf("decoded typed binding = %#v, want %#v without legacy role fields", got[0], want)
	}
	if len(got[0].Permissions) != len(want.Permissions) {
		t.Fatalf("decoded permission count = %d, want %d", len(got[0].Permissions), len(want.Permissions))
	}
	for i := range want.Permissions {
		if got[0].Permissions[i].Key() != want.Permissions[i].Key() {
			t.Fatalf("decoded permission %d = %#v, want %#v", i, got[0].Permissions[i], want.Permissions[i])
		}
	}
}

func TestDecodeAuthorizationRoleBindingsJSONRequiresExactTypedProjectExpansion(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	encoded, binding := typedAuthorizationPolicyJSON(t, projectID)

	if _, _, err := DecodeAuthorizationRoleBindingsJSON(encoded, projectgraph.ResourceID("other_project")); err == nil {
		t.Fatal("accepted typed policy expanded for a different project")
	}

	binding.Permissions[0].Action = access.ActionDashboardUpdate
	corrupt, err := json.Marshal(projectmanifest.AccessPolicy{RoleBindings: map[string]projectmanifest.RoleBinding{
		binding.ID: {
			ID: binding.ID, Name: binding.Name,
			Subject:           projectmanifest.Subject{Kind: "principal", PrincipalID: binding.Subject.ID},
			PermissionProfile: binding.PermissionProfile,
			Permissions:       binding.Permissions,
			PermissionRole:    binding.PermissionRole,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeAuthorizationRoleBindingsJSON(string(corrupt), projectID); err == nil {
		t.Fatal("accepted typed policy with a non-canonical role expansion")
	}
}

func TestDecodeAuthorizationRoleBindingsJSONRetainsLegacyRoleBindings(t *testing.T) {
	const encoded = `{"roleBindings":{"binding":{"id":"binding","name":"Viewer","role":"viewer","subject":{"kind":"principal","principalId":"alice"}}}}`
	bindings, migratable, err := DecodeAuthorizationRoleBindingsJSON(encoded, projectgraph.ResourceID("project_demo"))
	if err != nil {
		t.Fatal(err)
	}
	if !migratable || len(bindings) != 1 || bindings[0].Role != access.ProjectRoleViewer ||
		len(bindings[0].Capabilities) != len(access.ProjectRoleCapabilities(access.ProjectRoleViewer)) {
		t.Fatalf("decoded legacy bindings = %#v, migratable=%v", bindings, migratable)
	}
}
