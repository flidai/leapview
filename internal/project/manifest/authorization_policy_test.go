package manifest

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAccessPolicyFromAuthorizationPolicyPreservesTypedAndLegacyAuthority(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	typed, err := access.NewTypedRoleBinding(
		"typed-viewer",
		"Typed viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-demo"},
		access.PermissionRoleViewer,
		projectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	legacy := access.RoleBinding{
		ID: "legacy-admin", Name: "Legacy admin",
		Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group-admins"},
		Role:    access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin),
	}
	policy, err := AccessPolicyFromAuthorizationPolicy(access.AuthorizationPolicy{
		Scope:        access.AuthorizationPolicyScope{TargetID: "target-demo", ProjectID: projectID.String(), Environment: "production"},
		RoleBindings: []access.RoleBinding{typed, legacy},
	})
	if err != nil {
		t.Fatal(err)
	}

	gotTyped := policy.RoleBindings[typed.ID]
	if gotTyped.Role != "" || gotTyped.PermissionProfile != typed.PermissionProfile || gotTyped.PermissionRole != typed.PermissionRole {
		t.Fatalf("typed projection = %#v", gotTyped)
	}
	if len(gotTyped.Permissions) != len(typed.Permissions) {
		t.Fatalf("typed permissions = %d, want %d", len(gotTyped.Permissions), len(typed.Permissions))
	}
	for i := range typed.Permissions {
		if gotTyped.Permissions[i].Key() != typed.Permissions[i].Key() {
			t.Fatalf("typed permission %d = %#v, want %#v", i, gotTyped.Permissions[i], typed.Permissions[i])
		}
	}
	gotLegacy := policy.RoleBindings[legacy.ID]
	if gotLegacy.Role != string(access.ProjectRoleAdmin) || gotLegacy.Subject.Group != legacy.Subject.ID {
		t.Fatalf("legacy projection = %#v", gotLegacy)
	}

	typed.Permissions[0] = access.PermissionPair{}
	if policy.RoleBindings[typed.ID].Permissions[0] == (access.PermissionPair{}) {
		t.Fatal("projection retained mutable permission-pair storage")
	}
}

func TestAccessPolicyFromAuthorizationPolicyRejectsWrongProjectExpansion(t *testing.T) {
	typed, err := access.NewTypedRoleBinding(
		"typed-viewer",
		"Typed viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-demo"},
		access.PermissionRoleViewer,
		projectgraph.ResourceID("project_other"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AccessPolicyFromAuthorizationPolicy(access.AuthorizationPolicy{
		Scope:        access.AuthorizationPolicyScope{TargetID: "target-demo", ProjectID: "project_demo", Environment: "production"},
		RoleBindings: []access.RoleBinding{typed},
	})
	if err == nil {
		t.Fatal("accepted typed role expansion for a different project")
	}
}

func TestTargetGrantPolicyAllowsOnlySpecifiedDashboard(t *testing.T) {
	project := compileTestGraph(t)
	identity := compileTestIdentity()
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "shared-demo"}
	dashboard, _ := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	policy, err := AccessPolicyFromAuthorizationPolicy(access.AuthorizationPolicy{Scope: access.AuthorizationPolicyScope{TargetID: "target-demo", ProjectID: "project_demo", Environment: "production"}, Grants: []access.AuthorizationGrant{{ID: "read-dashboard", Subject: subject, Resource: dashboard, Capability: access.CapabilityResourceRead}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := CompileAuthorizationSnapshot(identity, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if grants := snapshot.Grants(); len(grants) != 1 || grants[0].Canonical.Capability() != access.CapabilityResourceRead || grants[0].Canonical.Subject() != subject {
		t.Fatalf("compiled grants = %#v", grants)
	}
	bad := policy.Grants["read-dashboard"]
	bad.Object.ID = "dashboard_missing"
	policy.Grants["read-dashboard"] = bad
	if _, err := CompileAuthorizationSnapshot(identity, project, policy); err == nil {
		t.Fatal("unknown dashboard admitted")
	}
}
