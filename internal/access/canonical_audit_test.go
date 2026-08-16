package access

import (
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
)

func TestCanonicalAuditEventRequiresExactServingIdentityAndResource(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_demo", Kind: graph.KindProject, Name: "demo"},
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewResourceRef("dashboard_main", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	event := CanonicalAuditEvent{
		Identity:    graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_7"},
		PrincipalID: "alice", Action: "dashboard.read", Resource: resource, Capability: CapabilityResourceRead,
	}
	if err := event.ValidateAgainst(project); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []CanonicalAuditEvent{
		func() CanonicalAuditEvent { copy := event; copy.Identity.ProjectID = "other_project"; return copy }(),
		func() CanonicalAuditEvent {
			copy := event
			copy.Resource, _ = NewResourceRef("dashboard_main", graph.KindModel)
			return copy
		}(),
		func() CanonicalAuditEvent { copy := event; copy.Capability = Capability("INVALID"); return copy }(),
	} {
		if err := bad.ValidateAgainst(project); err == nil {
			t.Fatalf("accepted invalid canonical audit event %#v", bad)
		}
	}
}
