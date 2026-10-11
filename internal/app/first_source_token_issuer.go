package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// firstSourceTokenIssuer serves only the existing browser personal-token create
// command. It does not extend serving permissions, delegation, token editing,
// OAuth credentials or the initial publisher credential's authority.
type firstSourceTokenIssuer struct {
	scope      *firstSourceCredentialServiceScope
	repository access.AuditedMutationRepository
}

func newFirstSourceTokenIssuer(scope *firstSourceCredentialServiceScope, repository access.AuditedMutationRepository) *firstSourceTokenIssuer {
	if scope == nil {
		return nil
	}
	return &firstSourceTokenIssuer{scope: scope, repository: repository}
}

func (i *firstSourceTokenIssuer) PermissionOptions(ctx context.Context, actor string) (pairs []access.PermissionPair, handled bool, err error) {
	nonClaimant := false
	handled, err = i.withAdmission(ctx, actor, func(ctx context.Context) error {
		tx, err := i.scope.authority.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		claim, err := platformbootstrap.New(tx).GetProjectClaim(ctx)
		if err != nil {
			return err
		}
		if claim.ClaimedBy != actor {
			nonClaimant = true
			return nil
		}
		pairs, err = i.permissionsTx(ctx, tx, actor)
		return err
	})
	if err == nil && nonClaimant {
		handled = false
	}
	return
}

func (i *firstSourceTokenIssuer) Create(ctx context.Context, input access.ScopedAPITokenInput) (secret string, handled bool, err error) {
	// Identity-only and instance-only tokens borrow no staged project authority.
	borrowsProject := false
	for _, pair := range input.Permissions {
		if pair.Target.Scope != access.PermissionScopeInstance {
			borrowsProject = true
		}
	}
	if !borrowsProject {
		return "", false, nil
	}
	handled, err = i.withAdmission(ctx, input.PrincipalID, func(ctx context.Context) error {
		var err error
		secret, err = accessmodule.CreatePersonalTokenWithAuthority(ctx, i.repository, input, func(ctx context.Context, tx pgx.Tx) error {
			pairs, err := i.permissionsTx(ctx, tx, input.PrincipalID)
			if err != nil {
				return err
			}
			return access.ValidatePermissionPairsAgainstAuthority(pairs, input.Permissions)
		})
		return err
	})
	if err != nil {
		secret = ""
	}
	return
}

func (i *firstSourceTokenIssuer) withAdmission(ctx context.Context, actor string, use func(context.Context) error) (bool, error) {
	if i == nil || i.scope == nil {
		return false, nil
	}
	unpublished, err := i.scope.unpublished(ctx)
	if err != nil || !unpublished {
		return false, err
	}
	admission, err := i.scope.admissions.AdmissionForTarget(ctx, i.scope.authority.targetID)
	if errors.Is(err, credentialmodule.ErrValidationNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if admission.Validate() != nil {
		return true, access.ErrForbidden
	}
	if admission.Intent.OperatorPrincipalID != actor {
		return false, nil
	}
	err = i.scope.authority.fence.WithUnpublishedTarget(ctx, i.scope.authority.targetID, admission.Intent.ProjectID, i.scope.authority.environment, use)
	if errors.Is(err, appdeploymentpostgres.ErrTargetAlreadyPublished) {
		return false, nil
	}
	return true, err
}

func (i *firstSourceTokenIssuer) permissionsTx(ctx context.Context, tx pgx.Tx, actor string) ([]access.PermissionPair, error) {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	if !ok || principal.ID != actor || principal.Kind != access.PrincipalKindUser || principal.DevBypass {
		return nil, access.ErrForbidden
	}
	issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
	if err != nil || issuer.Credential.Class != access.GrantCredentialClassSession {
		return nil, access.ErrForbidden
	}
	a := i.scope.authority
	admission, err := a.admissions.AdmissionForTargetTx(ctx, tx, a.targetID)
	if err != nil {
		return nil, err
	}
	if admission.Validate() != nil || admission.Intent.OperatorPrincipalID != actor {
		return nil, access.ErrForbidden
	}
	grant, err := admission.Intent.Grant()
	if err != nil {
		return nil, err
	}
	if err = accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, grant.Permissions); err != nil {
		return nil, err
	}
	if _, err = a.lockAdmittedStateTx(ctx, tx, admission); err != nil {
		return nil, err
	}
	claim, err := platformbootstrap.New(tx).GetProjectClaim(ctx)
	if err != nil {
		return nil, err
	}
	if claim.ClaimedBy != actor || claim.ProjectID != admission.Intent.ProjectID || claim.Environment != a.environment {
		return nil, access.ErrForbidden
	}
	project := projectgraph.ResourceID(admission.Intent.ProjectID)
	policy, err := a.policyTx(ctx, tx, access.AuthorizationPolicyScope{TargetID: a.targetID, ProjectID: project.String(), Environment: a.environment})
	if err != nil {
		return nil, err
	}
	canonical := map[string]bool{}
	for _, binding := range policy.RoleBindings {
		if access.IsProjectClaimBootstrapBinding(binding, project, actor) {
			canonical[binding.ID] = true
		}
	}
	for _, id := range []string{access.BootstrapOwnerBindingID, access.BootstrapEditorBindingID, access.BootstrapReleaseOperatorBindingID} {
		if !canonical[id] {
			return nil, access.ErrForbidden
		}
	}
	pairs, err := access.InitialProjectPublisherPermissions(project)
	if err != nil {
		return nil, err
	}
	pairs = append(pairs, grant.Permissions...)
	// Instance authority remains an independent durable role. Never derive it
	// from the claim or admission; lock a positive read through token commit.
	admin, err := accessmodule.IsPlatformAdministratorTx(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	if admin {
		if err = accessmodule.LockPlatformAdministratorTx(ctx, tx, actor); err != nil {
			return nil, err
		}
		instance, err := access.InstancePermissionOptions(a.targetID)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, instance...)
	}
	return pairs, nil
}
