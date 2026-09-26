package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// SetInitialReviewerBootstrap installs the composition-owned claim and delivery
// fence. The callback runs only while the exact claimed target has never been
// published, under a database lock shared with delivery activation. This is a
// nomination authority, not a permission added to the owner's runtime identity.
func (m *Module) SetInitialReviewerBootstrap(fn func(*http.Request, func(context.Context, access.AuthorizationPolicyScope, string) error) (bool, error)) {
	if m != nil {
		m.initialReviewerBootstrap = fn
	}
}

// RoleBindingProjectID resolves command metadata before first publication. The
// mutation independently repeats the fenced authority check; this lookup grants
// no rights and does not change the active-project resolver used by data routes.
func (m *Module) RoleBindingProjectID(r *http.Request) (projectgraph.ResourceID, error) {
	projectID, err := m.CurrentProjectID(r.Context())
	if err == nil || m.initialReviewerBootstrap == nil {
		return projectID, err
	}
	handled, bootstrapErr := m.initialReviewerBootstrap(r, func(ctx context.Context, scope access.AuthorizationPolicyScope, owner string) error {
		if _, _, err := m.initialReviewerSession(r.WithContext(ctx), owner); err != nil {
			return err
		}
		projectID = projectgraph.ResourceID(scope.ProjectID)
		return nil
	})
	if handled || bootstrapErr != nil {
		return projectID, bootstrapErr
	}
	return "", err
}

// RoleBindingAdministrationForRequest exposes the single safe pre-publication
// role preset to the claimed project owner. The returned handled flag lets the
// admin surface keep its established active-project read model unchanged.
func (m *Module) RoleBindingAdministrationForRequest(r *http.Request) (access.RoleBindingAdministrationState, bool, error) {
	if m == nil || r == nil || m.initialReviewerBootstrap == nil {
		return access.RoleBindingAdministrationState{}, false, nil
	}
	var state access.RoleBindingAdministrationState
	handled, err := m.initialReviewerBootstrap(r, func(ctx context.Context, scope access.AuthorizationPolicyScope, owner string) error {
		if _, _, err := m.initialReviewerSession(r.WithContext(ctx), owner); err != nil {
			return err
		}
		repo := m.repositoryValue()
		if repo == nil {
			return access.ErrGrantAuthorityUnavailable
		}
		reader, ok := repo.(access.AuthorizationPolicyReader)
		if !ok {
			return access.ErrGrantAuthorityUnavailable
		}
		policy, err := reader.AuthorizationPolicy(ctx, scope)
		if err != nil {
			return err
		}
		canonicalOwner := false
		for _, binding := range policy.RoleBindings {
			if binding.ID == access.BootstrapOwnerBindingID && access.IsProjectClaimBootstrapBinding(binding, projectgraph.ResourceID(scope.ProjectID), owner) {
				canonicalOwner = true
				break
			}
		}
		if !canonicalOwner {
			return access.ErrForbidden
		}
		for _, preset := range access.PermissionRolePresets() {
			if preset.Role == access.PermissionRoleReleaseApprover {
				state = access.RoleBindingAdministrationState{
					Scope: scope, Revision: policy.Revision, Digest: policy.Digest,
					RoleBindings: append([]access.RoleBinding(nil), policy.RoleBindings...),
					RolePresets:  []access.PermissionRolePreset{preset},
					// No policy binding is active before first publication, so
					// render the captured assignments as pending activation.
					ActiveSnapshotReady: true,
				}
				return nil
			}
		}
		return fmt.Errorf("release approver role preset is unavailable")
	})
	if err != nil {
		// An absent or noncanonical bootstrap policy and an unqualified caller
		// should leave the existing directory-only administration view intact.
		if errors.Is(err, access.ErrForbidden) || errors.Is(err, access.ErrAuthorizationPolicyNotFound) {
			return access.RoleBindingAdministrationState{}, false, nil
		}
		return access.RoleBindingAdministrationState{}, handled, err
	}
	return state, handled, nil
}

func (m *Module) initialReviewerSession(r *http.Request, owner string) (access.Principal, access.CredentialEvidence, error) {
	principal, ok := m.CurrentPrincipal(r)
	if !ok || principal.DevBypass || principal.ID != owner || principal.Kind != access.PrincipalKindUser {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	if _, found := m.requestCredential(r); found {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	evidence, ok := m.CurrentCredentialEvidence(r)
	if !ok || evidence.Class != string(access.GrantCredentialClassSession) || evidence.PrincipalID != owner {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	admin, err := m.IsPlatformAdmin(r.Context(), owner)
	if err != nil {
		return access.Principal{}, access.CredentialEvidence{}, err
	}
	if !admin {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	repo := m.repositoryValue()
	if repo == nil {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrGrantAuthorityUnavailable
	}
	local, err := repo.LocalCredential(r.Context(), owner)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	if err != nil {
		return access.Principal{}, access.CredentialEvidence{}, err
	}
	if local.MustChangePassword {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	current, err := repo.PrincipalByID(r.Context(), owner)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows) {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	if err != nil {
		return access.Principal{}, access.CredentialEvidence{}, err
	}
	if current.AccessDisabled() {
		return access.Principal{}, access.CredentialEvidence{}, access.ErrForbidden
	}
	return current, evidence, nil
}

func (m *Module) applyInitialReviewer(r *http.Request, command access.RoleBindingAdministrationCommand) (access.RoleBindingAdministrationState, bool, error) {
	var state access.RoleBindingAdministrationState
	if m.initialReviewerBootstrap == nil || command.Action != string(access.RoleBindingAdministrationGrant) || command.Role != access.PermissionRoleReleaseApprover {
		return state, false, nil
	}
	handled, err := m.initialReviewerBootstrap(r, func(ctx context.Context, scope access.AuthorizationPolicyScope, owner string) error {
		r := r.WithContext(ctx)
		if command.Subject.Kind != access.SubjectKindPrincipal || command.Subject.ID == owner {
			return access.ErrForbidden
		}
		switch command.BindingID {
		case access.BootstrapOwnerBindingID, access.BootstrapEditorBindingID, access.BootstrapReleaseOperatorBindingID:
			return access.ErrForbidden
		}
		if _, _, err := m.initialReviewerSession(r, owner); err != nil {
			return err
		}
		repo := m.repositoryValue()
		recipient, err := repo.PrincipalByID(ctx, command.Subject.ID)
		if err != nil || recipient.Kind != access.PrincipalKindUser || recipient.AccessDisabled() {
			return access.ErrForbidden
		}
		reader, ok := repo.(access.AuthorizationPolicyReader)
		if !ok {
			return access.ErrGrantAuthorityUnavailable
		}
		writer, ok := repo.(access.DurableGrantWriter)
		if !ok {
			return access.ErrGrantAuthorityUnavailable
		}
		projectID := projectgraph.ResourceID(scope.ProjectID)
		permissions, err := access.ExpandPermissionRole(access.PermissionRoleReleaseApprover, projectID)
		if err != nil {
			return err
		}
		for _, action := range []access.Action{access.ActionProjectAccessManage, access.ActionProjectAccessDelegate} {
			pair, err := access.NewProjectPermissionPair(action, projectID)
			if err != nil {
				return err
			}
			permissions = append(permissions, pair)
		}
		resolve := func(ctx context.Context) (access.CurrentAuthoritySnapshot, error) {
			principal, evidence, err := m.initialReviewerSession(r.WithContext(ctx), owner)
			if err != nil {
				return access.CurrentAuthoritySnapshot{}, err
			}
			policy, err := reader.AuthorizationPolicy(ctx, scope)
			if err != nil {
				return access.CurrentAuthoritySnapshot{}, err
			}
			canonicalOwner := false
			for _, binding := range policy.RoleBindings {
				if binding.ID == access.BootstrapOwnerBindingID && access.IsProjectClaimBootstrapBinding(binding, projectID, owner) {
					canonicalOwner = true
				}
			}
			if !canonicalOwner {
				return access.CurrentAuthoritySnapshot{}, access.ErrForbidden
			}
			return access.CurrentAuthoritySnapshot{Principal: principal, Credential: evidence, Permissions: access.ClonePermissionPairs(permissions), Policy: access.GrantIssuancePolicy{Scope: scope, Revision: policy.Revision, Digest: policy.Digest}}, nil
		}
		// Overrides exist only on this request-local handler for this exact role and
		// recipient. Public envelope APIs and ordinary effective permissions retain
		// their serving-snapshot authority and cannot borrow this bootstrap ceiling.
		handler := m.handler
		handler.CurrentProjectID = func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil }
		handler.CurrentEffectivePermissionOptions = func(ctx context.Context, actor string) ([]access.PermissionPair, error) {
			if actor != owner {
				return nil, access.ErrForbidden
			}
			authority, err := resolve(ctx)
			return authority.Permissions, err
		}
		handler.DurableGrantService = func(*http.Request) (*access.DurableGrantService, error) {
			return access.NewDurableGrantService(writer, access.CurrentAuthorityResolverFunc(func(ctx context.Context, request access.CurrentAuthorityRequest) (access.CurrentAuthoritySnapshot, error) {
				if request.Target.ProjectID != projectID || request.Target.InstanceID != "" || request.Target.ResourceID != "" || request.Target.ResourceKind != "" {
					return access.CurrentAuthoritySnapshot{}, access.ErrGrantAuthorityInvalid
				}
				return resolve(ctx)
			}))
		}
		state, err = handler.ApplyRoleBindingAdministration(r, command)
		if err != nil {
			return fmt.Errorf("initial reviewer nomination: %w", err)
		}
		return nil
	})
	return state, handled, err
}
