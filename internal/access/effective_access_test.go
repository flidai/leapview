package access

import "testing"

func TestExplainRoleBindingAccessDistinguishesDirectAndInheritedEvidence(t *testing.T) {
	projectID, err := NewResourceRef("project:test", "project")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := NewSubjectRef(SubjectKindPrincipal, "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	group, err := NewSubjectRef(SubjectKindGroup, "group-1")
	if err != nil {
		t.Fatal(err)
	}
	bindings := []RoleBinding{
		{ID: "direct", Subject: principal, Role: ProjectRoleViewer, Capabilities: ProjectRoleCapabilities(ProjectRoleViewer)},
		{ID: "group", Subject: group, Role: ProjectRoleViewer, Capabilities: ProjectRoleCapabilities(ProjectRoleViewer)},
	}
	decisions := ExplainRoleBindingAccess(bindings, []SubjectRef{principal, group}, projectID)
	if len(decisions) != 0 {
		t.Fatalf("project namespace unexpectedly accepted viewer role: %#v", decisions)
	}
	resource, err := NewResourceRef("model:orders", "model")
	if err != nil {
		t.Fatal(err)
	}
	decisions = ExplainRoleBindingAccess(bindings, []SubjectRef{principal, group}, resource)
	if len(decisions) != 4 {
		t.Fatalf("decisions=%#v, want two capabilities per binding", decisions)
	}
	var direct, inherited bool
	for _, decision := range decisions {
		if decision.GrantID == "direct" && !decision.Inherited && decision.Reason == "direct role binding" {
			direct = true
		}
		if decision.GrantID == "group" && decision.Inherited && decision.Reason == "group-inherited role binding" {
			inherited = true
		}
	}
	if !direct || !inherited {
		t.Fatalf("direct=%v inherited=%v decisions=%#v", direct, inherited, decisions)
	}
}

func TestSortAuthorizationDecisionsIsStableByCanonicalCapabilityOrder(t *testing.T) {
	decisions := []AuthorizationDecision{
		{ResourceKind: "model", ResourceID: "orders", Capability: CapabilityResourceRead, GrantID: "z"},
		{ResourceKind: "model", ResourceID: "orders", Capability: CapabilityResourceUse, GrantID: "a"},
	}
	SortAuthorizationDecisions(decisions)
	if decisions[0].Capability != CapabilityResourceUse || decisions[1].Capability != CapabilityResourceRead {
		t.Fatalf("sorted decisions=%#v", decisions)
	}
}

func TestExplainRoleBindingAccessMarksOwnerAuthority(t *testing.T) {
	resource, err := NewResourceRef("dashboard:owned", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewSubjectRef(SubjectKindPrincipal, "principal-owner")
	if err != nil {
		t.Fatal(err)
	}
	decisions := ExplainRoleBindingAccess([]RoleBinding{{
		ID: "owner-binding", Subject: owner, Role: ProjectRoleOwner,
		Capabilities: ProjectRoleCapabilities(ProjectRoleOwner),
	}}, []SubjectRef{owner}, resource)
	if len(decisions) == 0 {
		t.Fatal("owner binding produced no decisions")
	}
	for _, decision := range decisions {
		if !decision.Owner || decision.Reason != "owner role binding" || decision.Inherited {
			t.Fatalf("owner decision=%#v", decision)
		}
	}
}
