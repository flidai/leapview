package manifest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

func compileTestGraph(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
		{ID: "model_orders", Kind: graph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func compileTestIdentity() graph.ServingIdentity {
	return graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_7"}
}

func TestCompileAuthorizationSnapshotBindsExactIdentityAndGraph(t *testing.T) {
	project := compileTestGraph(t)
	policy := AccessPolicy{Grants: map[string]Grant{
		"reader": {ID: "grant_reader", Name: "reader", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "RESOURCE_READ"},
	}}
	snapshot, err := CompileAuthorizationSnapshot(compileTestIdentity(), project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Identity(); got != compileTestIdentity() {
		t.Fatalf("snapshot identity = %#v", got)
	}
	if got := snapshot.Grants(); len(got) != 1 || got[0].Canonical.Resource().ID() != "dashboard_main" {
		t.Fatalf("snapshot grants = %#v", got)
	}
	for _, bad := range []graph.ServingIdentity{
		{ProjectID: "project_demo", Environment: "", GenerationID: "generation_7"},
		{ProjectID: "project_demo", Environment: "production", GenerationID: ""},
	} {
		if _, err := CompileAuthorizationSnapshot(bad, project, policy); err == nil {
			t.Fatalf("accepted invalid or mismatched identity %#v", bad)
		}
	}
	staging := graph.ServingIdentity{ProjectID: "project_demo", Environment: "staging", GenerationID: "generation_8"}
	stagingSnapshot, err := CompileAuthorizationSnapshot(staging, project, policy)
	if err != nil {
		t.Fatalf("compile same graph for a second exact serving identity: %v", err)
	}
	if got := stagingSnapshot.Identity(); got != staging {
		t.Fatalf("second snapshot identity = %#v, want %#v", got, staging)
	}
}

func TestCompileAuthorizationSnapshotRejectsKindCapabilityAndImplicitRoles(t *testing.T) {
	project := compileTestGraph(t)
	invalid := AccessPolicy{Grants: map[string]Grant{
		"invalid": {ID: "grant_invalid", Object: SecurableRef{ID: "model_orders", Kind: "model"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "RESOURCE_PUBLISH"},
	}}
	if _, err := CompileAuthorizationSnapshot(compileTestIdentity(), project, invalid); !errors.Is(err, access.ErrCapabilityNotAllowed) {
		t.Fatalf("invalid capability error = %v", err)
	}
	role := AccessPolicy{RoleBindings: map[string]RoleBinding{
		"admin": {ID: "binding_admin", Role: "admin", Subject: Subject{Kind: "principal", PrincipalID: "alice"}},
	}}
	snapshot, err := CompileAuthorizationSnapshot(compileTestIdentity(), project, role)
	if err != nil {
		t.Fatal(err)
	}
	bindings := snapshot.RoleBindings()
	if len(bindings) != 1 || bindings[0].Role != access.ProjectRoleAdmin {
		t.Fatalf("role bindings = %#v", bindings)
	}
	resource, err := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := snapshot.Allows(bindings[0].Subject, resource, access.CapabilityResourcePublish)
	if err != nil || !allowed {
		t.Fatalf("captured role did not authorize exact dashboard capability: allowed=%v err=%v", allowed, err)
	}
	model, err := access.NewResourceRef("model_orders", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err = snapshot.Allows(bindings[0].Subject, model, access.CapabilityResourcePublish)
	if !errors.Is(err, access.ErrCapabilityNotAllowed) || allowed {
		t.Fatalf("captured role authorized unsupported model capability: allowed=%v err=%v", allowed, err)
	}
}

func TestCompileAuthorizationSnapshotPreservesTypedRoleBinding(t *testing.T) {
	project := compileTestGraph(t)
	identity := compileTestIdentity()
	typed, err := access.NewTypedRoleBinding(
		"binding_viewer",
		"viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		access.PermissionRoleViewer,
		identity.ProjectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	policy := AccessPolicy{RoleBindings: map[string]RoleBinding{
		typed.ID: {
			ID: typed.ID, Name: typed.Name, Subject: Subject{Kind: "principal", PrincipalID: "alice"},
			PermissionProfile: typed.PermissionProfile, PermissionRole: typed.PermissionRole, Permissions: typed.Permissions,
		},
	}}

	snapshot, err := CompileAuthorizationSnapshot(identity, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	bindings := snapshot.RoleBindings()
	if len(bindings) != 1 {
		t.Fatalf("role bindings = %#v", bindings)
	}
	got := bindings[0]
	if got.Role != "" || got.Capabilities != nil || got.PermissionProfile != typed.PermissionProfile || got.PermissionRole != typed.PermissionRole {
		t.Fatalf("typed binding lost typed representation: %#v", got)
	}
	if len(got.Permissions) != len(typed.Permissions) {
		t.Fatalf("typed permission count = %d, want %d", len(got.Permissions), len(typed.Permissions))
	}
	for i := range typed.Permissions {
		if got.Permissions[i].Key() != typed.Permissions[i].Key() {
			t.Fatalf("typed permission %d = %#v, want %#v", i, got.Permissions[i], typed.Permissions[i])
		}
	}
	effective, err := snapshot.EffectiveTypedPermissions([]access.SubjectRef{got.Subject})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != len(typed.Permissions) {
		t.Fatalf("effective typed permissions = %d, want %d", len(effective), len(typed.Permissions))
	}

	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"permissionProfile":"`+access.PermissionCatalogProfile+`"`) || !strings.Contains(string(encoded), `"permissionRole":"viewer"`) {
		t.Fatalf("typed policy JSON lost typed fields: %s", encoded)
	}
	if strings.Contains(string(encoded), `"role":`) {
		t.Fatalf("typed policy JSON carried a legacy role: %s", encoded)
	}
	encodedAgain, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(encodedAgain) {
		t.Fatalf("typed policy JSON is not deterministic: %s != %s", encoded, encodedAgain)
	}
}

func TestCompileAuthorizationSnapshotRejectsNonCanonicalTypedRoleBinding(t *testing.T) {
	project := compileTestGraph(t)
	identity := compileTestIdentity()
	typed, err := access.NewTypedRoleBinding(
		"binding_viewer",
		"viewer",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"},
		access.PermissionRoleViewer,
		identity.ProjectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	typed.Permissions[0].Action = access.ActionDashboardUpdate
	policy := AccessPolicy{RoleBindings: map[string]RoleBinding{
		typed.ID: {
			ID: typed.ID, Subject: Subject{Kind: "principal", PrincipalID: "alice"},
			PermissionProfile: typed.PermissionProfile, PermissionRole: typed.PermissionRole, Permissions: typed.Permissions,
		},
	}}
	if _, err := CompileAuthorizationSnapshot(identity, project, policy); err == nil {
		t.Fatal("accepted typed role binding with altered exact permission pair")
	}
}

func TestCompileAuthorizationSnapshotRejectsIdentityFallbackSubjects(t *testing.T) {
	project := compileTestGraph(t)
	for _, subject := range []Subject{
		{Kind: "principal", Email: "alice@example.test"},
		{Kind: "dashboard_publication", Publication: "publication_public"},
	} {
		policy := AccessPolicy{Grants: map[string]Grant{
			"grant": {ID: "grant_subject", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: subject, Capability: "RESOURCE_READ"},
		}}
		if _, err := CompileAuthorizationSnapshot(compileTestIdentity(), project, policy); err == nil {
			t.Fatalf("accepted non-explicit subject %#v", subject)
		}
	}
}

func TestCompileAuthorizationSnapshotRejectsNonCanonicalIdentityLiterals(t *testing.T) {
	project := compileTestGraph(t)
	cases := []struct {
		name   string
		policy AccessPolicy
	}{
		{name: "role whitespace", policy: AccessPolicy{RoleBindings: map[string]RoleBinding{"role": {ID: "binding", Role: " admin", Subject: Subject{Kind: "principal", PrincipalID: "alice"}}}}},
		{name: "role case", policy: AccessPolicy{RoleBindings: map[string]RoleBinding{"role": {ID: "binding", Role: "ADMIN", Subject: Subject{Kind: "principal", PrincipalID: "alice"}}}}},
		{name: "grant id whitespace", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: " grant", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "RESOURCE_READ"}}}},
		{name: "resource id whitespace", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: "grant", Object: SecurableRef{ID: "dashboard_main ", Kind: "dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "RESOURCE_READ"}}}},
		{name: "resource kind case", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: "grant", Object: SecurableRef{ID: "dashboard_main", Kind: "Dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: "RESOURCE_READ"}}}},
		{name: "capability whitespace", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: "grant", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: "alice"}, Capability: " RESOURCE_READ"}}}},
		{name: "subject kind alias", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: "grant", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: Subject{Kind: "service_principal", PrincipalID: "alice"}, Capability: "RESOURCE_READ"}}}},
		{name: "subject id whitespace", policy: AccessPolicy{Grants: map[string]Grant{"grant": {ID: "grant", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, Subject: Subject{Kind: "principal", PrincipalID: " alice"}, Capability: "RESOURCE_READ"}}}},
		{name: "policy id whitespace", policy: AccessPolicy{DataPolicies: map[string]DataPolicy{"policy": {ID: " policy", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, PolicyType: "row_filter", ExpressionJSON: `{"field":"country","value":"DK"}`}}}},
		{name: "policy type whitespace", policy: AccessPolicy{DataPolicies: map[string]DataPolicy{"policy": {ID: "policy", Object: SecurableRef{ID: "dashboard_main", Kind: "dashboard"}, PolicyType: " row_filter", ExpressionJSON: `{"field":"country","value":"DK"}`}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompileAuthorizationSnapshot(compileTestIdentity(), project, test.policy); err == nil {
				t.Fatal("accepted non-canonical identity literal")
			}
		})
	}
}
