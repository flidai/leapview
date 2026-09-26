package rolebindings

import (
	"context"
	"errors"
	"strings"
	"testing"

	access "github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type roleBindingRepository struct {
	access.Repository
	scope       access.AuthorizationPolicyScope
	envelope    access.GrantAdminEnvelope
	policy      access.AuthorizationPolicy
	upsertInput access.AuthorizationRoleBindingInput
	audit       access.AuditEventInput
	upserts     int
}

func (r *roleBindingRepository) CurrentGrantAdminEnvelopeForMutation(_ context.Context, id, actorID string) (access.GrantAdminEnvelope, error) {
	if id != r.envelope.ID || actorID != r.envelope.BoundPrincipalID {
		return access.GrantAdminEnvelope{}, access.ErrGrantNotFound
	}
	return r.envelope, nil
}

func (r *roleBindingRepository) UpsertAuthorizationRoleBinding(_ context.Context, input access.AuthorizationRoleBindingInput) (access.AuthorizationPolicy, error) {
	r.upserts++
	r.upsertInput = input
	r.policy = access.AuthorizationPolicy{
		Scope: input.Scope, Revision: input.ExpectedRevision + 1,
		Digest: "sha256:" + strings.Repeat("a", 64), RoleBindings: []access.RoleBinding{input.Binding},
	}
	return r.policy, nil
}

func (r *roleBindingRepository) RemoveAuthorizationRoleBinding(context.Context, access.AuthorizationRoleBindingDeleteInput) (access.AuthorizationPolicy, error) {
	return access.AuthorizationPolicy{}, errors.New("unexpected role-binding removal")
}

func roleBindingFixture(t *testing.T) (access.RoleBindingAdministrationMutation, access.RoleBindingAdministrationAuthority, *roleBindingRepository) {
	t.Helper()
	const actorID = "actor-1"
	const projectID = projectgraph.ResourceID("project_demo")
	scope := access.AuthorizationPolicyScope{TargetID: "target-1", ProjectID: string(projectID), Environment: "prod"}
	binding, err := access.NewTypedRoleBinding("binding-1", "viewer", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "recipient-1"}, access.PermissionRoleViewer, projectID)
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := access.NewProjectPermissionPair(access.ActionProjectAccessDelegate, projectID)
	if err != nil {
		t.Fatal(err)
	}
	permissions := append([]access.PermissionPair{delegate}, binding.Permissions...)
	repo := &roleBindingRepository{
		scope:  scope,
		policy: access.AuthorizationPolicy{Scope: scope, Revision: 4, Digest: "sha256:" + strings.Repeat("b", 64)},
		envelope: access.GrantAdminEnvelope{
			ID: "envelope-1", BoundPrincipalID: actorID, TargetProjectID: projectID,
			Issuer: access.GrantIssuerEvidence{PrincipalID: actorID, Credential: access.GrantCredentialEvidence{
				Class: access.GrantCredentialClassSession, ID: "session-1",
			}},
			Permissions: binding.Permissions, RecipientSelector: "principal:recipient-1",
			RoleVersion: access.PermissionRoleVersion(access.PermissionRoleViewer),
		},
	}
	mutation := access.RoleBindingAdministrationMutation{
		Scope: scope, Action: access.RoleBindingAdministrationGrant, Binding: binding,
		ExpectedRevision: 4, IdempotencyKey: "idem-1", ActorID: actorID,
		RequestID: "request-1", CorrelationID: "correlation-1",
	}
	authority := access.RoleBindingAdministrationAuthority{
		ActorID: actorID, Permissions: permissions,
		Credential: access.RoleBindingAdministrationCredential{
			Class: access.GrantCredentialClassSession, ID: "session-1", PrincipalID: actorID,
		},
	}
	return mutation, authority, repo
}

func TestApplyIssuesEnvelopeBeforeAuditedPolicyTransaction(t *testing.T) {
	mutation, authority, repo := roleBindingFixture(t)
	mutation.IssueAdminEnvelope = true
	issuerCalled, transactionEntered := false, false
	bootstrapCalls := 0
	ports := access.RoleBindingAdministrationPorts{
		EnvelopeIssuer: roleBindingEnvelopeIssuerFunc(func(_ context.Context, request access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error) {
			if transactionEntered {
				t.Fatal("grant envelope issued inside audited policy transaction")
			}
			issuerCalled = true
			if request.IdempotencyKey != "idem-1:envelope" || request.TTL <= 0 {
				t.Fatalf("envelope request = %#v", request)
			}
			return repo.envelope, nil
		}),
		ResolveCurrentAuthority: func(context.Context, string) (access.RoleBindingAdministrationAuthority, error) {
			return authority, nil
		},
		AuthorizeClaimBootstrap: func(context.Context, access.AuthorizationPolicyScope, access.RoleBinding, string) (bool, error) {
			bootstrapCalls++
			return true, nil
		},
	}
	var audit access.AuditEventInput
	policy, err := Apply(context.Background(), repo, mutation, ports, func(apply func(access.Repository) (access.AuditEventInput, error)) error {
		transactionEntered = true
		var err error
		audit, err = apply(repo)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !issuerCalled || !transactionEntered || bootstrapCalls != 0 {
		t.Fatalf("issuer=%t transaction=%t bootstrap calls=%d", issuerCalled, transactionEntered, bootstrapCalls)
	}
	if repo.upserts != 1 || repo.upsertInput.ExpectedRevision != 4 || repo.upsertInput.IdempotencyKey != "idem-1" {
		t.Fatalf("upsert count/input = %d / %#v", repo.upserts, repo.upsertInput)
	}
	if policy.Revision != 5 || audit.Action != "role_binding.created" || audit.ResourceID != "binding-1" || audit.RequestID != "request-1" || audit.CorrelationID != "correlation-1" {
		t.Fatalf("policy=%#v audit=%#v", policy, audit)
	}
}

func TestApplyGrantsProjectAdminWhenDelegateIsInRoleExpansion(t *testing.T) {
	mutation, authority, repo := roleBindingFixture(t)
	projectID := projectgraph.ResourceID(mutation.Scope.ProjectID)
	binding, err := access.NewTypedRoleBinding(
		"binding-admin", "Project admin",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "recipient-1"},
		access.PermissionRoleProjectAdmin, projectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	mutation.Binding = binding
	mutation.GrantAdminEnvelopeID = repo.envelope.ID
	repo.envelope.Permissions = binding.Permissions
	repo.envelope.RecipientSelector = "principal:recipient-1"
	repo.envelope.RoleVersion = access.PermissionRoleVersion(access.PermissionRoleProjectAdmin)
	// The principal's effective authority is a unique pair set. The selected
	// role already supplies project.access.delegate, which the grant path also
	// requires independently as delegation authority.
	authority.Permissions = binding.Permissions

	_, err = Apply(context.Background(), repo, mutation, access.RoleBindingAdministrationPorts{
		ResolveCurrentAuthority: func(context.Context, string) (access.RoleBindingAdministrationAuthority, error) {
			return authority, nil
		},
	}, func(apply func(access.Repository) (access.AuditEventInput, error)) error {
		_, err := apply(repo)
		return err
	})
	if err != nil {
		t.Fatalf("grant project_admin role: %v", err)
	}
	if repo.upserts != 1 || repo.upsertInput.Binding.PermissionRole != access.PermissionRoleProjectAdmin {
		t.Fatalf("upserts=%d binding=%#v", repo.upserts, repo.upsertInput.Binding)
	}
}

func TestAuthorizeGrantAllowsProjectAdminDelegateOverlapForAPIToken(t *testing.T) {
	mutation, authority, repo := roleBindingFixture(t)
	projectID := projectgraph.ResourceID(mutation.Scope.ProjectID)
	binding, err := access.NewTypedRoleBinding(
		"binding-admin", "Project admin",
		access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "recipient-1"},
		access.PermissionRoleProjectAdmin, projectID,
	)
	if err != nil {
		t.Fatal(err)
	}
	const fingerprint = "fingerprint-token-1234"
	authority.Permissions = binding.Permissions
	authority.Credential = access.RoleBindingAdministrationCredential{
		Class: access.GrantCredentialClassAPIToken, ID: "token-1", Fingerprint: fingerprint,
		PrincipalID: mutation.ActorID, PermissionProfile: access.PermissionCatalogProfile,
		TokenPermissions: binding.Permissions,
	}
	repo.envelope.Issuer.Credential = access.GrantCredentialEvidence{
		Class: access.GrantCredentialClassAPIToken, ID: "token-1", Fingerprint: fingerprint,
	}
	repo.envelope.Permissions = binding.Permissions
	repo.envelope.RecipientSelector = "principal:recipient-1"
	repo.envelope.RoleVersion = access.PermissionRoleVersion(access.PermissionRoleProjectAdmin)

	if err := authorizeGrant(authority, repo.envelope, mutation.Scope, binding.Permissions); err != nil {
		t.Fatalf("authorize project_admin role for API token: %v", err)
	}
}

func TestApplyPreservesCanonicalBootstrapEnvelopeException(t *testing.T) {
	mutation, _, repo := roleBindingFixture(t)
	binding, err := access.NewTypedRoleBinding(access.BootstrapOwnerBindingID, access.BootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: mutation.ActorID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(mutation.Scope.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	mutation.GrantAdminEnvelopeID = ""
	mutation.IssueAdminEnvelope = false
	mutation.Binding = binding
	var issuerCalls, bootstrapCalls int
	transactionEntered := false
	ports := access.RoleBindingAdministrationPorts{
		EnvelopeIssuer: roleBindingEnvelopeIssuerFunc(func(context.Context, access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error) {
			issuerCalls++
			return access.GrantAdminEnvelope{}, errors.New("bootstrap must not issue an envelope")
		}),
		AuthorizeClaimBootstrap: func(_ context.Context, scope access.AuthorizationPolicyScope, binding access.RoleBinding, actorID string) (bool, error) {
			bootstrapCalls++
			if !transactionEntered {
				t.Fatal("REST bootstrap authorization ran outside the audited transaction")
			}
			if scope != repo.scope || actorID != mutation.ActorID || !access.IsProjectClaimBootstrapBinding(binding, projectgraph.ResourceID(scope.ProjectID), actorID) {
				return false, nil
			}
			return true, nil
		},
	}
	_, err = Apply(context.Background(), repo, mutation, ports, func(apply func(access.Repository) (access.AuditEventInput, error)) error {
		transactionEntered = true
		_, err := apply(repo)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if issuerCalls != 0 || bootstrapCalls != 1 || repo.upserts != 1 {
		t.Fatalf("issuer=%d bootstrap=%d upserts=%d", issuerCalls, bootstrapCalls, repo.upserts)
	}
}

type roleBindingEnvelopeIssuerFunc func(context.Context, access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error)

func (f roleBindingEnvelopeIssuerFunc) IssueGrantAdminEnvelope(ctx context.Context, request access.GrantAdminEnvelopeRequest) (access.GrantAdminEnvelope, error) {
	return f(ctx, request)
}
