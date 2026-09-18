package module

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestTargetAuthorizationManifestPolicyPreservesTypedRoleBinding(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	typed, err := access.NewTypedRoleBinding(
		"binding_viewer",
		"viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		access.PermissionRoleViewer,
		projectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.AuthorizationPolicy{
		Scope:        access.AuthorizationPolicyScope{TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production"},
		RoleBindings: []access.RoleBinding{typed},
	}
	manifest, err := targetAuthorizationManifestPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := manifest.RoleBindings[typed.ID]
	if !ok {
		t.Fatalf("typed role binding missing from manifest: %#v", manifest.RoleBindings)
	}
	if got.Role != "" || got.PermissionProfile != typed.PermissionProfile || got.PermissionRole != typed.PermissionRole {
		t.Fatalf("typed role binding projection = %#v", got)
	}
	if len(got.Permissions) != len(typed.Permissions) {
		t.Fatalf("typed permission count = %d, want %d", len(got.Permissions), len(typed.Permissions))
	}
	for i := range typed.Permissions {
		if got.Permissions[i].Key() != typed.Permissions[i].Key() {
			t.Fatalf("typed permission %d = %#v, want %#v", i, got.Permissions[i], typed.Permissions[i])
		}
	}

	canonical, err := canonicalNativeServingDocument(manifest, "access policy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(canonical, `"permissionProfile":"`+access.PermissionCatalogProfile+`"`) || !strings.Contains(canonical, `"permissionRole":"viewer"`) {
		t.Fatalf("canonical policy lost typed fields: %s", canonical)
	}
	if strings.Contains(canonical, `"role":`) {
		t.Fatalf("canonical typed policy carried a legacy role: %s", canonical)
	}
}

func TestTargetAuthorizationManifestPolicyRejectsInvalidTypedRoleBinding(t *testing.T) {
	typed := access.RoleBinding{
		ID: "binding_invalid", Name: "invalid",
		Subject:           access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		PermissionProfile: access.PermissionCatalogProfile,
		PermissionRole:    access.PermissionRoleViewer,
		Permissions:       []access.PermissionPair{},
	}
	policy := access.AuthorizationPolicy{
		Scope:        access.AuthorizationPolicyScope{TargetID: "target_demo", ProjectID: "project_demo", Environment: "production"},
		RoleBindings: []access.RoleBinding{typed},
	}
	if _, err := targetAuthorizationManifestPolicy(policy); err == nil {
		t.Fatal("accepted typed role binding with invalid exact permission set")
	}
}
