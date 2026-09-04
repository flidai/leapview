package snapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	"github.com/flidai/leapview/internal/project/graph"
)

type controlProjectionStore struct {
	access.ControlStore
	state           access.ControlState
	stateErrors     []error
	initializeErr   error
	initializeCalls int
	initialized     access.ControlStateSeed
	reactivateCalls []controlReactivation
}

type controlReactivation struct {
	instanceID string
	grantID    string
	revision   int64
	project    graph.ProjectGraph
	actorID    string
}

func (s *controlProjectionStore) ControlState(context.Context, string) (access.ControlState, error) {
	if len(s.stateErrors) != 0 {
		err := s.stateErrors[0]
		s.stateErrors = s.stateErrors[1:]
		if err != nil {
			return access.ControlState{}, err
		}
	}
	return s.state, nil
}

func (s *controlProjectionStore) InitializeControlState(_ context.Context, seed access.ControlStateSeed, _ graph.ProjectGraph) (access.ControlState, error) {
	s.initializeCalls++
	s.initialized = seed
	if s.initializeErr != nil {
		return access.ControlState{}, s.initializeErr
	}
	return s.state, nil
}

func (s *controlProjectionStore) ReactivateGrant(_ context.Context, instanceID, grantID string, revision int64, project graph.ProjectGraph, actorID string) (access.ControlGrant, error) {
	s.reactivateCalls = append(s.reactivateCalls, controlReactivation{instanceID: instanceID, grantID: grantID, revision: revision, project: project, actorID: actorID})
	for i := range s.state.Grants {
		grant := &s.state.Grants[i]
		if grant.ID != grantID {
			continue
		}
		if grant.Revision != revision {
			return access.ControlGrant{}, access.ErrControlRevisionConflict
		}
		if grant.RevokedAt != "" {
			return access.ControlGrant{}, access.ErrControlRevoked
		}
		grant.ReferenceLifecycle = access.ControlReferenceActive
		return *grant, nil
	}
	return access.ControlGrant{}, access.ErrControlNotFound
}

var _ access.ControlStore = (*controlProjectionStore)(nil)

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

func TestProjectLiveAuthorizationSnapshotSeedsMissingState(t *testing.T) {
	project := testGraph(t)
	grant := testGrant(t, project)
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_authored", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &controlProjectionStore{
		stateErrors: []error{access.ErrControlNotFound},
		state: access.ControlState{InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Grants: []access.ControlGrant{{
			ID: "grant_authored", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: grant.Subject(), Resource: grant.Resource(), Capability: grant.Capability(), ReferenceLifecycle: access.ControlReferenceActive,
		}}},
	}
	projected, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if store.initializeCalls != 1 || len(store.initialized.Grants) != 1 || store.initialized.Grants[0].ID != "grant_authored" {
		t.Fatalf("initialize calls=%d seed=%#v", store.initializeCalls, store.initialized)
	}
	if len(projected.Grants()) != 1 || projected.Grants()[0].ID != "grant_authored" {
		t.Fatalf("projected grants=%#v", projected.Grants())
	}
}

func TestProjectLiveAuthorizationSnapshotUsesLiveStateAndReloadsAfterInitializationRace(t *testing.T) {
	project := testGraph(t)
	authored := testGrant(t, project)
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, []Grant{{ID: "grant_authored", Canonical: authored}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	liveResource, err := access.NewResourceRef("model_orders", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	live, err := access.NewCanonicalGrant(project, authored.Subject(), liveResource, access.CapabilityResourceRead)
	if err != nil {
		t.Fatal(err)
	}
	store := &controlProjectionStore{
		stateErrors:   []error{access.ErrControlNotFound},
		initializeErr: access.ErrControlConflict,
		state: access.ControlState{InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Grants: []access.ControlGrant{{
			ID: "grant_live", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: live.Subject(), Resource: live.Resource(), Capability: live.Capability(), ReferenceLifecycle: access.ControlReferenceActive,
		}}},
	}
	projected, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if store.initializeCalls != 1 || len(projected.Grants()) != 1 || projected.Grants()[0].ID != "grant_live" {
		t.Fatalf("initialize calls=%d projected grants=%#v", store.initializeCalls, projected.Grants())
	}
}

func TestProjectLiveAuthorizationSnapshotReactivatesOnlySuspendedNonRevokedGrantsWithCAS(t *testing.T) {
	project := testGraph(t)
	grant := testGrant(t, project)
	activeResource, err := access.NewResourceRef("model_orders", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &controlProjectionStore{state: access.ControlState{InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Grants: []access.ControlGrant{
		{ID: "grant_suspended", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: grant.Subject(), Resource: grant.Resource(), Capability: grant.Capability(), Revision: 4, ReferenceLifecycle: access.ControlReferenceSuspended},
		{ID: "grant_revoked", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: grant.Subject(), Resource: grant.Resource(), Capability: grant.Capability(), Revision: 5, RevokedAt: "2026-09-04T00:00:00Z", ReferenceLifecycle: access.ControlReferenceSuspended},
		{ID: "grant_active", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: grant.Subject(), Resource: activeResource, Capability: access.CapabilityResourceRead, Revision: 6, ReferenceLifecycle: access.ControlReferenceActive},
	}}}
	projected, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.reactivateCalls) != 1 {
		t.Fatalf("reactivation calls=%#v", store.reactivateCalls)
	}
	call := store.reactivateCalls[0]
	if call.grantID != "grant_suspended" || call.revision != 4 || call.instanceID != "instance_demo" || call.actorID != "runtimehost" || call.project.ProjectID() != project.ProjectID() {
		t.Fatalf("reactivation call=%#v", call)
	}
	if len(projected.Grants()) != 2 {
		t.Fatalf("projected grants=%#v", projected.Grants())
	}
}

func TestProjectLiveAuthorizationSnapshotLeavesSuspendedOmittedTargetDenied(t *testing.T) {
	project := testGraph(t)
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	omitted, err := access.NewResourceRef("dashboard_removed", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subject := mustSubject(t, access.SubjectKindPrincipal, "alice")
	store := &controlProjectionStore{state: access.ControlState{InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Grants: []access.ControlGrant{{
		ID: "grant_removed", InstanceID: "instance_demo", ProjectID: project.ProjectID().String(), Subject: subject, Resource: omitted, Capability: access.CapabilityResourceRead, Revision: 4, ReferenceLifecycle: access.ControlReferenceSuspended,
	}}}}
	projected, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.reactivateCalls) != 0 || len(projected.Grants()) != 0 {
		t.Fatalf("reactivation calls=%#v projected grants=%#v", store.reactivateCalls, projected.Grants())
	}
}

func TestProjectLiveAuthorizationSnapshotRejectsCrossAuthorityStateBeforeReactivation(t *testing.T) {
	project := testGraph(t)
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	grant := testGrant(t, project)
	store := &controlProjectionStore{state: access.ControlState{InstanceID: "other_instance", ProjectID: project.ProjectID().String(), Grants: []access.ControlGrant{{
		ID: "grant_suspended", InstanceID: "other_instance", ProjectID: project.ProjectID().String(), Subject: grant.Subject(), Resource: grant.Resource(), Capability: grant.Capability(), Revision: 4, ReferenceLifecycle: access.ControlReferenceSuspended,
	}}}}
	if _, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility); err == nil {
		t.Fatal("cross-authority control state was accepted")
	}
	if len(store.reactivateCalls) != 0 {
		t.Fatalf("cross-authority state triggered reactivation: %#v", store.reactivateCalls)
	}
}

func TestProjectLiveAuthorizationSnapshotPreservesCompatibilityDataPolicies(t *testing.T) {
	project := testGraph(t)
	resource, err := access.NewResourceRef("model_orders", graph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	expression := `{"filters":[{"field":"tenant_id","operator":"equals","values":["acme"]}]}`
	compiled, err := accesspolicy.Compile("policy_1", accesspolicy.TypeRowFilter, expression)
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, nil, []DataPolicy{{
		ID: "policy_1", Resource: resource, PolicyType: accesspolicy.TypeRowFilter, ExpressionJSON: expression, Compiled: compiled,
	}})
	if err != nil {
		t.Fatal(err)
	}
	store := &controlProjectionStore{state: access.ControlState{InstanceID: "instance_demo", ProjectID: project.ProjectID().String()}}
	projected, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	policies := projected.DataPolicies()
	if len(policies) != 1 || policies[0].ID != "policy_1" || policies[0].ExpressionJSON != expression || policies[0].PolicyType != accesspolicy.TypeRowFilter {
		t.Fatalf("projected policies=%#v", policies)
	}
}

func TestProjectLiveAuthorizationSnapshotFailsClosedOnUnexpectedControlError(t *testing.T) {
	project := testGraph(t)
	compatibility, err := NewAuthorizationSnapshot(testIdentity(), project, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("database unavailable")
	store := &controlProjectionStore{stateErrors: []error{want}}
	if _, err := ProjectLiveAuthorizationSnapshot(context.Background(), store, "instance_demo", "runtimehost", testIdentity(), project, compatibility); !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
}
