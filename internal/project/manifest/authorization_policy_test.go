package manifest

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestTargetGrantPolicyAllowsOnlySpecifiedDashboard(t *testing.T) {
	project := compileTestGraph(t)
	identity := compileTestIdentity()
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "shared-demo"}
	dashboard, _ := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	model, _ := access.NewResourceRef("model_orders", graph.KindModel)
	policy, err := FromAuthorizationPolicy(access.AuthorizationPolicy{Grants: []access.AuthorizationGrant{{ID: "read-dashboard", Subject: subject, Resource: dashboard, Capability: access.CapabilityResourceRead}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := CompileAuthorizationSnapshot(identity, project, policy)
	if err != nil {
		t.Fatal(err)
	}
	if accesssnapshot.RoleAllowsCapability(snapshot, []access.SubjectRef{subject}, access.CapabilityResourceUse) {
		t.Fatal("narrow grants conferred project-wide agent access")
	}
	for _, check := range []struct {
		resource access.ResourceRef
		cap      access.Capability
		want     bool
	}{{dashboard, access.CapabilityResourceRead, true}, {dashboard, access.CapabilityResourceEdit, false}, {model, access.CapabilityResourceRead, false}} {
		got, err := snapshot.Allows(subject, check.resource, check.cap)
		if err != nil || got != check.want {
			t.Fatalf("decision %v %v = %v, %v", check.resource, check.cap, got, err)
		}
	}
	bad := policy.Grants["read-dashboard"]
	bad.Object.ID = "dashboard_missing"
	policy.Grants["read-dashboard"] = bad
	if _, err := CompileAuthorizationSnapshot(identity, project, policy); err == nil {
		t.Fatal("unknown dashboard admitted")
	}
}
