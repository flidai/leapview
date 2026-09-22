package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestAuthorizationGrantHistorySurvivesRoleMutation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id,principal_type,status) VALUES ($1::uuid,'user','active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-grant", ProjectID: "project-grant", Environment: "prod"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID}
	binding := access.RoleBinding{ID: "owner", Subject: subject, Role: access.ProjectRoleOwner, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleOwner)}
	original, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	resource, _ := access.NewResourceRef("dashboard:sales", graph.KindDashboard)
	command := access.AuthorizationGrantInput{Scope: scope, Grant: access.AuthorizationGrant{ID: "sales", Subject: subject, Resource: resource, Capability: access.CapabilityResourceRead}, ExpectedRevision: original.Revision, IdempotencyKey: "grant"}
	granted, err := repo.UpsertAuthorizationGrant(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted.Grants) != 1 || len(granted.RoleBindings) != 1 || granted.Revision != 2 {
		t.Fatalf("grant lost policy: %+v", granted)
	}
	replay, err := repo.UpsertAuthorizationGrant(ctx, command)
	if err != nil || replay.Digest != granted.Digest || replay.Revision != 2 {
		t.Fatal("replay changed revision", err)
	}
	command.Grant.Capability = access.CapabilityResourceEdit
	if _, err := repo.UpsertAuthorizationGrant(ctx, command); !errors.Is(err, access.ErrAuthorizationPolicyIdempotency) {
		t.Fatal("conflicting replay accepted", err)
	}
	command.IdempotencyKey = "stale"
	if _, err := repo.UpsertAuthorizationGrant(ctx, command); !errors.Is(err, access.ErrAuthorizationPolicyStaleRevision) {
		t.Fatal("stale policy accepted", err)
	}
	binding.Name = "renamed"
	changed, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, ExpectedRevision: 2, IdempotencyKey: "rename"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Grants) != 1 || changed.Grants[0] != granted.Grants[0] {
		t.Fatal("role update discarded grant")
	}
	old, err := repo.AuthorizationPolicyRevision(ctx, scope, 1)
	if err != nil || len(old.Grants) != 0 || old.Digest != original.Digest {
		t.Fatal("rewrote historical policy", err)
	}
	command.ExpectedRevision = changed.Revision
	command.IdempotencyKey = "replace"
	replaced, err := repo.UpsertAuthorizationGrant(ctx, command)
	if err != nil || replaced.Grants[0].Capability != access.CapabilityResourceEdit {
		t.Fatal("grant replace failed", err)
	}
	historical, err := repo.AuthorizationPolicyRevision(ctx, scope, 2)
	if err != nil || historical.Grants[0].Capability != access.CapabilityResourceRead {
		t.Fatal("grant history changed", err)
	}
	for _, verb := range []string{"UPDATE access.authorization_policy_grant SET name='tampered'", "DELETE FROM access.authorization_policy_grant"} {
		if _, err := db.runtime.Exec(ctx, verb); err == nil {
			t.Fatal("runtime mutated history", verb)
		}
	}
}
