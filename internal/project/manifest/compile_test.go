package manifest

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

func compileTestGraph(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_demo", Kind: graph.KindProject, Name: "demo"},
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
		{ProjectID: "other", Environment: "production", GenerationID: "generation_7"},
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
