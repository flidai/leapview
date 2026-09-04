package snapshot

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestFromControlStateProjectsActiveRowsAndExcludesSuspendedGrants(t *testing.T) {
	project := testGraph(t)
	dashboard, err := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	state := access.ControlState{
		InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Revision: 7,
		RoleAssignments: []access.RoleAssignment{{ID: "role_alice", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: alice, Role: string(access.ProjectRoleViewer)}},
		Grants: []access.ControlGrant{
			{ID: "grant_active", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: alice, Resource: dashboard, Capability: access.CapabilityResourceRead, ReferenceLifecycle: access.ControlReferenceActive},
			{ID: "grant_suspended", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: alice, Resource: dashboard, Capability: access.CapabilityResourceShare, ReferenceLifecycle: access.ControlReferenceSuspended},
		},
	}
	snapshot, err := FromControlState(testIdentity(), project, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RoleBindings()) != 1 || len(snapshot.Grants()) != 1 || snapshot.Grants()[0].ID != "grant_active" {
		t.Fatalf("projection = roles %#v grants %#v", snapshot.RoleBindings(), snapshot.Grants())
	}
}

func TestFromControlStateRejectsCrossInstanceAndProjectRows(t *testing.T) {
	project := testGraph(t)
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	for _, state := range []access.ControlState{
		{InstanceID: "instance_demo", ProjectID: "other_project"},
		{InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), RoleAssignments: []access.RoleAssignment{{ID: "role_alice", InstanceID: "other_instance", ProjectID: project.ProjectID().String(), Subject: alice, Role: string(access.ProjectRoleViewer)}}},
	} {
		if _, err := FromControlState(testIdentity(), project, state, nil); err == nil {
			t.Fatalf("FromControlState accepted %#v", state)
		}
	}
}

func TestControlSeedFromSnapshotPreservesOnlyRoleAndGrantEvidence(t *testing.T) {
	project := testGraph(t)
	source := testGrant(t, project)
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	projectRef, err := access.NewResourceRef(project.ProjectID(), graph.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	projectAdmin, err := access.NewCanonicalGrant(project, alice, projectRef, access.CapabilityProjectAdmin)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(testIdentity(), project, []RoleBinding{{
		ID: "role_alice", Subject: alice, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}}, []Grant{{ID: "grant_alice", Canonical: source}, {ID: "grant_project_admin", Canonical: projectAdmin}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := ControlSeedFromSnapshot("instance_demo", "actor", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if seed.InstanceID != "instance_demo" || seed.ProjectID != project.ProjectID().String() || len(seed.RoleAssignments) != 2 || len(seed.Grants) != 1 {
		t.Fatalf("seed = %#v", seed)
	}
	if converted := seed.RoleAssignments[1]; converted.ID != "grant_project_admin" || converted.Role != string(access.ProjectRoleAdmin) || converted.Subject != alice {
		t.Fatalf("converted project-admin grant = %#v", converted)
	}
}
