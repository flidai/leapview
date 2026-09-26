package postgres

import (
	"errors"
	"reflect"
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
	if len(changed.Grants) != 1 || !reflect.DeepEqual(changed.Grants[0], granted.Grants[0]) {
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

func TestTypedAuthorizationGrantPersistsExactPairsAndImmutableHistory(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id,principal_type,status) VALUES ($1::uuid,'user','active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-typed-grant", ProjectID: "project-typed-grant", Environment: "prod"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID}
	binding := access.RoleBinding{ID: "owner", Subject: subject, Role: access.ProjectRoleOwner, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleOwner)}
	initial, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := graph.NewResourceID(scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef("pipeline_refresh", graph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	command := access.AuthorizationGrantInput{
		Scope:            scope,
		Grant:            access.AuthorizationGrant{ID: "refresh-run", Subject: subject, Resource: resource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}},
		ExpectedRevision: initial.Revision, IdempotencyKey: "refresh-run",
	}
	updated, err := repo.UpsertAuthorizationGrant(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || len(updated.Grants) != 1 {
		t.Fatalf("unexpected typed grant policy: %+v", updated)
	}
	grant := updated.Grants[0]
	if grant.Capability != "" || grant.PermissionProfile != access.PermissionCatalogProfile || len(grant.Permissions) != 1 || grant.Permissions[0].Key() != pair.Key() {
		t.Fatalf("typed grant did not survive persistence: %+v", grant)
	}
	loaded, err := repo.AuthorizationPolicyRevision(ctx, scope, 2)
	if err != nil || len(loaded.Grants) != 1 || loaded.Grants[0].Permissions[0].Key() != pair.Key() {
		t.Fatalf("typed grant revision did not round-trip: %+v, %v", loaded, err)
	}
	old, err := repo.AuthorizationPolicyRevision(ctx, scope, 1)
	if err != nil || len(old.Grants) != 0 || old.Digest != initial.Digest {
		t.Fatalf("rewrote pre-grant policy revision: %+v, %v", old, err)
	}

	command.Grant.Permissions[0].Target.ProjectID = "another-project"
	command.ExpectedRevision = updated.Revision
	command.IdempotencyKey = "wrong-project"
	if _, err := repo.UpsertAuthorizationGrant(ctx, command); err == nil {
		t.Fatal("accepted typed grant for another project")
	}
	current, err := repo.AuthorizationPolicy(ctx, scope)
	if err != nil || current.Revision != updated.Revision || current.Digest != updated.Digest {
		t.Fatalf("invalid grant changed policy: %+v, %v", current, err)
	}
	insertMalformed := func(id string, capability, profile *string, permissions []byte) error {
		_, err := db.admin.Exec(ctx, `INSERT INTO access.authorization_policy_grant
			(target_id, project_id, environment, revision, id, subject_kind, subject_id, resource_kind, resource_id, capability, permission_profile, permissions)
			VALUES ($1,$2,$3,2,$4,'principal',$5,'pipeline','pipeline_other',$6,$7,$8::jsonb)`,
			scope.TargetID, scope.ProjectID, scope.Environment, id, policySubjectID, capability, profile, permissions)
		return err
	}
	if err := insertMalformed("missing-profile", nil, nil, []byte(`[]`)); err == nil {
		t.Fatal("database accepted typed grant with missing profile")
	}
	if err := insertMalformed("empty-pairs", nil, nullableString(access.PermissionCatalogProfile), []byte(`[]`)); err == nil {
		t.Fatal("database accepted typed grant with empty permission set")
	}
	if err := insertMalformed("mixed-form", nullableString(string(access.CapabilityResourceUse)), nullableString(access.PermissionCatalogProfile), []byte(`[{}]`)); err == nil {
		t.Fatal("database accepted mixed legacy and typed grant")
	}
}

func TestAuthorizationGrantsSurviveRoleBindingRemoval(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx := t.Context()
	if _, err := db.admin.Exec(ctx, `INSERT INTO access.principal (id,principal_type,status) VALUES ($1::uuid,'user','active')`, policySubjectID); err != nil {
		t.Fatal(err)
	}
	repo := &Repository{db: db.runtime}
	scope := access.AuthorizationPolicyScope{TargetID: "target-delete-grants", ProjectID: "project-delete-grants", Environment: "prod"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: policySubjectID}
	binding := access.RoleBinding{ID: "owner", Subject: subject, Role: access.ProjectRoleOwner, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleOwner)}
	policy, err := repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, IdempotencyKey: "owner"})
	if err != nil {
		t.Fatal(err)
	}

	legacyResource, err := access.NewResourceRef("dashboard:sales", graph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = repo.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
		Scope: scope, Grant: access.AuthorizationGrant{ID: "sales-read", Subject: subject, Resource: legacyResource, Capability: access.CapabilityResourceRead},
		ExpectedRevision: policy.Revision, IdempotencyKey: "legacy-grant",
	})
	if err != nil {
		t.Fatal(err)
	}

	projectID, err := graph.NewResourceID(scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	typedResource, err := access.NewResourceRef("pipeline_refresh", graph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, typedResource)
	if err != nil {
		t.Fatal(err)
	}
	policy, err = repo.UpsertAuthorizationGrant(ctx, access.AuthorizationGrantInput{
		Scope:            scope,
		Grant:            access.AuthorizationGrant{ID: "refresh-run", Subject: subject, Resource: typedResource, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}},
		ExpectedRevision: policy.Revision, IdempotencyKey: "typed-grant",
	})
	if err != nil {
		t.Fatal(err)
	}
	preDelete, err := repo.AuthorizationPolicyRevision(ctx, scope, policy.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(preDelete.Grants) != 2 || len(preDelete.RoleBindings) != 1 {
		t.Fatalf("pre-delete policy = %+v, want both grant forms and owner binding", preDelete)
	}

	removed, err := repo.RemoveAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingDeleteInput{
		Scope: scope, BindingID: binding.ID, ExpectedRevision: preDelete.Revision, IdempotencyKey: "remove-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.RoleBindings) != 0 || len(removed.Grants) != 2 {
		t.Fatalf("policy after role removal = %+v, want no role bindings and both grants", removed)
	}
	var legacy, typed *access.AuthorizationGrant
	for i := range removed.Grants {
		switch removed.Grants[i].ID {
		case "sales-read":
			legacy = &removed.Grants[i]
		case "refresh-run":
			typed = &removed.Grants[i]
		}
	}
	if legacy == nil || legacy.Capability != access.CapabilityResourceRead || legacy.PermissionProfile != "" || len(legacy.Permissions) != 0 {
		t.Fatalf("legacy grant after role removal = %+v", legacy)
	}
	if typed == nil || typed.Capability != "" || typed.PermissionProfile != access.PermissionCatalogProfile || len(typed.Permissions) != 1 || typed.Permissions[0].Key() != pair.Key() {
		t.Fatalf("typed grant after role removal = %+v", typed)
	}
	current, err := repo.AuthorizationPolicy(ctx, scope)
	if err != nil || !reflect.DeepEqual(current, removed) {
		t.Fatalf("current policy after role removal = %+v, %v; want %+v", current, err, removed)
	}
	historical, err := repo.AuthorizationPolicyRevision(ctx, scope, preDelete.Revision)
	if err != nil || !reflect.DeepEqual(historical, preDelete) {
		t.Fatalf("historical policy changed after role removal: got %+v, err %v; want %+v", historical, err, preDelete)
	}
}
