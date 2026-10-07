package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type currentAuthorizationPolicyReader struct {
	policy access.AuthorizationPolicy
	err    error
}

func (r *currentAuthorizationPolicyReader) AuthorizationPolicy(context.Context, access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	return r.policy, r.err
}

func (r *currentAuthorizationPolicyReader) AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error) {
	return access.AuthorizationPolicy{}, errors.New("historical policy is not used for live authorization")
}

func TestCurrentTargetAuthorizationFilterRevokesCapturedRole(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_1"}
	dashboard, err := projectgraph.NewResourceID("dashboard_main")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: dashboard, Kind: projectgraph.KindDashboard, Name: "main"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	alice := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	bob := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "bob"}
	capturedRole, err := access.NewTypedRoleBinding("binding_alice", "viewer", alice, access.PermissionRoleViewer, identity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []access.RoleBinding{capturedRole}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target_demo", ProjectID: identity.ProjectID.String(), Environment: identity.Environment}
	reader := &currentAuthorizationPolicyReader{policy: access.AuthorizationPolicy{Scope: scope, Revision: 1, RoleBindings: []access.RoleBinding{capturedRole}}}
	reader.policy.Digest, err = access.AuthorizationPolicyDigest(scope, reader.policy.RoleBindings)
	if err != nil {
		t.Fatal(err)
	}
	filter := currentTargetAuthorizationFilter(reader, scope.TargetID, identity.ProjectID, identity.Environment)
	active, err := filter(t.Context(), captured)
	if err != nil {
		t.Fatalf("filter captured current role: %v", err)
	}
	dashboardRef, err := access.NewResourceRef(dashboard, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	dashboardRead, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, dashboardRef)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, checkErr := active.AllowsTyped(alice, dashboardRead); checkErr != nil || !allowed {
		t.Fatalf("captured current typed role denied: allowed=%v err=%v bindings=%v", allowed, checkErr, active.RoleBindings())
	}
	freshTargetFilter := currentTargetAuthorizationFilter(reader, scope.TargetID, "", identity.Environment)
	if _, err := freshTargetFilter(t.Context(), captured); err != nil {
		t.Fatalf("first claimed project rejected after fresh-target activation: %v", err)
	}
	reader.policy.Revision++
	bobBinding, err := access.NewTypedRoleBinding("binding_bob", "viewer", bob, access.PermissionRoleViewer, identity.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	reader.policy.RoleBindings = []access.RoleBinding{bobBinding}
	reader.policy.Digest, err = access.AuthorizationPolicyDigest(scope, reader.policy.RoleBindings)
	if err != nil {
		t.Fatal(err)
	}
	active, err = filter(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, checkErr := active.AllowsTyped(alice, dashboardRead); checkErr != nil || allowed {
		t.Fatalf("old generation imported revoked typed authority for Alice: allowed=%v err=%v", allowed, checkErr)
	}
	if allowed, checkErr := active.AllowsTyped(bob, dashboardRead); checkErr != nil || allowed {
		t.Fatalf("old generation imported current authority: %v", active.RoleBindings())
	}
	reader.err = access.ErrAuthorizationPolicyNotFound
	if _, err := filter(t.Context(), captured); !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		t.Fatalf("missing current head must fail closed: %v", err)
	}
}

func TestCurrentTargetAuthorizationFilterValidatesDigestIncludingGrants(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_1"}
	dashboard := projectgraph.ResourceID("dashboard_main")
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: dashboard, Kind: projectgraph.KindDashboard, Name: "main"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(dashboard, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	alice := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	legacy := access.AuthorizationGrant{ID: "legacy-read", Subject: alice, Resource: resource, Capability: access.CapabilityResourceRead}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	typed := access.AuthorizationGrant{ID: "typed-read", Subject: alice, Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}}
	legacyCanonical, err := access.NewCanonicalGrant(graph, legacy.Subject, legacy.Resource, legacy.Capability)
	if err != nil {
		t.Fatal(err)
	}
	capturedTyped, err := accesssnapshot.NewTypedGrant(typed.ID, typed.Name, typed.Subject, typed.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, nil, []accesssnapshot.Grant{{ID: legacy.ID, Canonical: legacyCanonical}, capturedTyped}, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthorizationPolicyScope{TargetID: "target_demo", ProjectID: identity.ProjectID.String(), Environment: identity.Environment}
	policy := access.AuthorizationPolicy{Scope: scope, Revision: 1, Grants: []access.AuthorizationGrant{legacy, typed}}
	policy.Digest, err = access.AuthorizationPolicyDigest(scope, nil, policy.Grants...)
	if err != nil {
		t.Fatal(err)
	}
	reader := &currentAuthorizationPolicyReader{policy: policy}
	filter := currentTargetAuthorizationFilter(reader, scope.TargetID, identity.ProjectID, identity.Environment)
	if _, err := filter(t.Context(), captured); err != nil {
		t.Fatalf("filter valid captured grants using full policy digest: %v", err)
	}
	roleOnlyDigest, err := access.AuthorizationPolicyDigest(scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader.policy.Digest = roleOnlyDigest
	if _, err := filter(t.Context(), captured); err == nil {
		t.Fatal("accepted a current-policy digest that omitted grants")
	}
}
