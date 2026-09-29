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

func TestAccessPolicyProjectionAndCompilationPreserveTypedExactGrant(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	resource, err := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	permission, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-demo"}
	grant := access.AuthorizationGrant{
		ID: "typed-dashboard-read", Name: "Exact dashboard reader", Subject: subject, Resource: resource,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{permission},
	}
	policy, err := AccessPolicyFromAuthorizationPolicy(access.AuthorizationPolicy{
		Scope:  access.AuthorizationPolicyScope{TargetID: "target-demo", ProjectID: projectID.String(), Environment: "production"},
		Grants: []access.AuthorizationGrant{grant},
	})
	if err != nil {
		t.Fatal(err)
	}
	projected := policy.Grants[grant.ID]
	if projected.Capability != "" || projected.Object.ID != string(resource.ID()) || projected.Object.Kind != string(resource.Kind()) ||
		projected.PermissionProfile != access.PermissionCatalogProfile || len(projected.Permissions) != 1 || projected.Permissions[0] != permission {
		t.Fatalf("typed project manifest grant = %#v", projected)
	}
	grant.Permissions[0] = access.PermissionPair{}
	if policy.Grants[grant.ID].Permissions[0] != permission {
		t.Fatal("manifest projection retained the mutable permission slice")
	}
	snapshot, err := CompileAuthorizationSnapshot(compileTestIdentity(), compileTestGraph(t), policy)
	if err != nil {
		t.Fatal(err)
	}
	compiled := snapshot.Grants()
	if len(compiled) != 1 || compiled[0].PermissionProfile != access.PermissionCatalogProfile || len(compiled[0].Permissions) != 1 || compiled[0].Permissions[0] != permission {
		t.Fatalf("compiled typed grants = %#v", compiled)
	}
	allowed, err := snapshot.AllowsTyped(subject, permission)
	if err != nil || !allowed {
		t.Fatalf("typed exact permission allowed=%v err=%v", allowed, err)
	}
	updateDashboard, err := access.NewExactPermissionPair(access.ActionDashboardUpdate, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = snapshot.AllowsTyped(subject, updateDashboard)
	if err != nil || allowed {
		t.Fatalf("ungranted dashboard update allowed=%v err=%v", allowed, err)
	}
}

func TestCompileAuthorizationSnapshotRejectsInvalidTypedGrantCombinations(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	resource, err := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-demo"}
	makePolicy := func(pair access.PermissionPair, capability string) AccessPolicy {
		return AccessPolicy{Grants: map[string]Grant{"typed-dashboard-read": {
			ID: "typed-dashboard-read", Name: "Exact dashboard reader",
			Object:  SecurableRef{Kind: string(resource.Kind()), ID: string(resource.ID())},
			Subject: Subject{Kind: string(subject.Kind), PrincipalID: subject.ID}, Capability: capability,
			PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair},
		}}}
	}

	validPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	mixed := makePolicy(validPair, string(access.CapabilityResourceRead))
	if _, err := CompileAuthorizationSnapshot(compileTestIdentity(), compileTestGraph(t), mixed); err == nil {
		t.Fatal("compiled a typed grant that also carries a legacy capability")
	}

	foreignPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectgraph.ResourceID("project_other"), resource)
	if err != nil {
		t.Fatal(err)
	}
	foreign := makePolicy(foreignPair, "")
	if _, err := CompileAuthorizationSnapshot(compileTestIdentity(), compileTestGraph(t), foreign); err == nil {
		t.Fatal("compiled a typed grant whose exact permission belongs to another project")
	}
}
