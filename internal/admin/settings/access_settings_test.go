package settings

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type accessSettingsPolicyRepository struct {
	access.Repository
	policy      access.AuthorizationPolicy
	upsertInput access.AuthorizationRoleBindingInput
	deleteInput access.AuthorizationRoleBindingDeleteInput
	mutationErr error
}

func (r *accessSettingsPolicyRepository) AuthorizationPolicy(context.Context, access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	return r.policy, nil
}

func (r *accessSettingsPolicyRepository) AuthorizationPolicyRevision(context.Context, access.AuthorizationPolicyScope, int64) (access.AuthorizationPolicy, error) {
	return r.policy, nil
}

func (r *accessSettingsPolicyRepository) UpsertAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	r.upsertInput = input
	if r.mutationErr != nil {
		return access.AuthorizationPolicy{}, r.mutationErr
	}
	return r.policy, nil
}

func (r *accessSettingsPolicyRepository) DeleteAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	r.deleteInput = input
	if r.mutationErr != nil {
		return access.AuthorizationPolicy{}, r.mutationErr
	}
	return r.policy, nil
}

func TestLoadAccessSettingsUsesActivePolicyAndLabelsUnsupportedGrants(t *testing.T) {
	scope := AccessSettingsScope{TargetID: "instance-1", ProjectID: "project-1", Environment: "production"}
	repository := &accessSettingsPolicyRepository{policy: access.AuthorizationPolicy{
		Scope: scopeForAccessSettingsTest(scope), Revision: 4, Digest: "sha256:policy",
		RoleBindings: []access.RoleBinding{{ID: "binding-1", Name: "Finance", Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group-finance"}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}},
	}}

	state, err := LoadAccessSettings(context.Background(), repository, scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != scope.ProjectID || state.PolicyRevision != 4 || state.PolicyDigest != "sha256:policy" || state.GrantAdministrationAvailable || state.GrantAdministrationLabel == "" {
		t.Fatalf("state metadata = %#v", state)
	}
	if len(state.RoleBindings) != 1 || state.RoleBindings[0].SubjectType != "group" || state.RoleBindings[0].SubjectID != "group-finance" || state.RoleBindings[0].Role != "viewer" {
		t.Fatalf("bindings = %#v", state.RoleBindings)
	}
	if len(state.Roles) != len(access.CanonicalProjectRoles()) || state.Loading {
		t.Fatalf("roles/loading = %d/%t", len(state.Roles), state.Loading)
	}
}

func TestLoadAccessSettingsIncludesActiveGenerationAccessProvenance(t *testing.T) {
	scope := AccessSettingsScope{TargetID: "instance-1", ProjectID: "project-1", Environment: "production"}
	repository := &accessSettingsPolicyRepository{policy: access.AuthorizationPolicy{Scope: scopeForAccessSettingsTest(scope), Revision: 4}}
	state, err := LoadAccessSettingsForPrincipal(context.Background(), repository, scope, "principal-admin", func(context.Context, string) ([]access.AuthorizationDecision, error) {
		return []access.AuthorizationDecision{
			{Allowed: true, Capability: access.CapabilityResourceRead, Reason: "direct role binding", ResourceKind: "dashboard", ResourceID: "dashboard-sales", SubjectType: "principal", SubjectID: "principal-admin"},
			{Allowed: true, Capability: access.CapabilityResourceUse, Reason: "group-inherited role binding", ResourceKind: "dashboard", ResourceID: "dashboard-sales", Inherited: true, SubjectType: "group", SubjectID: "group-sales"},
			{Allowed: true, Capability: access.CapabilityProjectAdmin, Reason: "owner role binding", ResourceKind: "project", ResourceID: "project-1", Owner: true, SubjectType: "principal", SubjectID: "principal-admin"},
			{Allowed: true, Capability: access.CapabilityProjectAdmin, Reason: "platform administrator", ResourceKind: "project", ResourceID: "project-1", Platform: true, SubjectType: "principal", SubjectID: "principal-admin"},
			{Allowed: true, Capability: access.CapabilityResourceRead, Reason: "direct grant", ResourceKind: "dashboard", ResourceID: "dashboard-sales", GrantID: "compiled-grant", SubjectType: "principal", SubjectID: "principal-admin"},
			{Allowed: false, Capability: access.CapabilityResourceEdit, Reason: "no direct, inherited, owner, or platform authority", ResourceKind: "dashboard", ResourceID: "dashboard-sales"},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveAccess.Loading || state.EffectiveAccess.Error != "" || len(state.EffectiveAccess.Decisions) != 6 {
		t.Fatalf("effective access state = %#v", state.EffectiveAccess)
	}
	wantAuthorities := []string{"Direct", "Group-derived", "Owner", "Platform", "Compiled", "Denied"}
	for i, want := range wantAuthorities {
		if got := state.EffectiveAccess.Decisions[i].Authority; got != want {
			t.Fatalf("decision %d authority = %q, want %q; decisions=%#v", i, got, want, state.EffectiveAccess.Decisions)
		}
	}
}

func TestLoadAccessSettingsBoundsEffectiveAccessProviderFailure(t *testing.T) {
	scope := AccessSettingsScope{TargetID: "instance-1", ProjectID: "project-1", Environment: "production"}
	repository := &accessSettingsPolicyRepository{policy: access.AuthorizationPolicy{Scope: scopeForAccessSettingsTest(scope), Revision: 4}}
	state, err := LoadAccessSettingsForPrincipal(context.Background(), repository, scope, "principal-admin", func(context.Context, string) ([]access.AuthorizationDecision, error) {
		return nil, errors.New("private generation detail")
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.EffectiveAccess.Loading || state.EffectiveAccess.Error != "Effective access explanation is unavailable." {
		t.Fatalf("effective access failure state = %#v", state.EffectiveAccess)
	}
}

func TestApplyAccessSettingsForwardsCASAndIdempotencyToPolicyAuthority(t *testing.T) {
	scope := AccessSettingsScope{TargetID: "instance-1", ProjectID: "project-1", Environment: "production"}
	repository := &accessSettingsPolicyRepository{policy: access.AuthorizationPolicy{Revision: 8}}
	state, err := ApplyAccessSettingsCommand(context.Background(), repository, scope, AccessSettingsCommand{
		Action: "create", BindingID: "binding-1", BindingName: "Finance", SubjectType: "group", SubjectID: "group-finance", Role: "viewer", ExpectedRevision: 7,
	}, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if repository.upsertInput.ExpectedRevision != 7 || repository.upsertInput.IdempotencyKey != "idem-1" || repository.upsertInput.Scope.ProjectID != scope.ProjectID {
		t.Fatalf("upsert input = %#v", repository.upsertInput)
	}
	if state.PolicyRevision != 8 || len(state.Roles) != len(access.CanonicalProjectRoles()) {
		t.Fatalf("state = %#v", state)
	}
	if got := repository.upsertInput.Binding.Capabilities; len(got) != len(access.ProjectRoleCapabilities(access.ProjectRoleViewer)) {
		t.Fatalf("capabilities = %#v", got)
	}

	repository.mutationErr = access.ErrAuthorizationPolicyConflict
	_, err = ApplyAccessSettingsCommand(context.Background(), repository, scope, AccessSettingsCommand{Action: "delete", BindingID: "binding-1", ExpectedRevision: 8}, "idem-2")
	if !errors.Is(err, access.ErrAuthorizationPolicyConflict) {
		t.Fatalf("delete error = %v", err)
	}
	if repository.deleteInput.ExpectedRevision != 8 || repository.deleteInput.IdempotencyKey != "idem-2" {
		t.Fatalf("delete input = %#v", repository.deleteInput)
	}
}

func scopeForAccessSettingsTest(scope AccessSettingsScope) access.AuthorizationPolicyScope {
	return access.AuthorizationPolicyScope{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment}
}
