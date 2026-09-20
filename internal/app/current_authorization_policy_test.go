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
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	alice := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	bob := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "bob"}
	capturedRole := access.RoleBinding{ID: "binding_alice", Subject: alice, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}
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
	if err != nil || !accesssnapshot.RoleAllowsCapability(active, []access.SubjectRef{alice}, access.CapabilityResourceRead) {
		t.Fatalf("captured current role denied: err=%v bindings=%v", err, active.RoleBindings())
	}
	freshTargetFilter := currentTargetAuthorizationFilter(reader, scope.TargetID, "", identity.Environment)
	if _, err := freshTargetFilter(t.Context(), captured); err != nil {
		t.Fatalf("first claimed project rejected after fresh-target activation: %v", err)
	}
	reader.policy.Revision++
	reader.policy.RoleBindings = []access.RoleBinding{{ID: "binding_bob", Subject: bob, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}}
	reader.policy.Digest, err = access.AuthorizationPolicyDigest(scope, reader.policy.RoleBindings)
	if err != nil {
		t.Fatal(err)
	}
	active, err = filter(t.Context(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if accesssnapshot.RoleAllowsCapability(active, []access.SubjectRef{alice}, access.CapabilityResourceRead) || accesssnapshot.RoleAllowsCapability(active, []access.SubjectRef{bob}, access.CapabilityResourceRead) {
		t.Fatalf("old generation imported current authority: %v", active.RoleBindings())
	}
	reader.err = access.ErrAuthorizationPolicyNotFound
	if _, err := filter(t.Context(), captured); !errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
		t.Fatalf("missing current head must fail closed: %v", err)
	}
}
