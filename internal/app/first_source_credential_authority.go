package app

import (
	"context"
	"reflect"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

type firstSourceAdmissionReader interface {
	AdmissionForTargetTx(context.Context, pgx.Tx, string) (credentialmodule.FirstSourceAdmission, error)
}

type firstSourceCredentialPool interface {
	Begin(context.Context) (pgx.Tx, error)
}

type firstSourceCredentialBindings interface {
	BindingForShareTx(context.Context, pgx.Tx, connectionbinding.BindingScope, connectionbinding.TargetID, projectgraph.ResourceID) (connectionbinding.TargetBinding, error)
}

// This capability is deliberately separate from serving-snapshot authority.
// Its callback is bounded by the unpublished-target fence and a transaction
// retaining the exact session, binding and staged policy authority. It cannot
// authorize publication, pick a source plan or select a credential version.
// The callback must use the supplied transaction for bounded database work.
// It must not call pool-owning credential services or external probes while
// these authority locks are retained.
type firstSourceCredentialAuthority struct {
	production            bool
	targetID, environment string
	pool                  firstSourceCredentialPool
	fence                 unpublishedTargetFence
	admissions            firstSourceAdmissionReader
	bindings              firstSourceCredentialBindings
	// Composition must read and lock the current exact policy head in tx.
	policyTx func(context.Context, pgx.Tx, access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error)
}

func (a firstSourceCredentialAuthority) WithAuthorization(ctx context.Context, actor string, resource credentialmodule.ValidationResource, action access.Action, use func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error) error {
	if ctx == nil || !a.production || resource.Validate() != nil || resource.ScopeKind != "connection" ||
		resource.TargetID != a.targetID || resource.Environment != a.environment ||
		(action != access.ActionConnectionManage && action != access.ActionConnectionUse) || use == nil ||
		typednil.IsNil(a.pool) || typednil.IsNil(a.fence) || typednil.IsNil(a.admissions) || typednil.IsNil(a.bindings) || a.policyTx == nil {
		return access.ErrForbidden
	}
	principal, found := accessmodule.PrincipalFromContext(ctx)
	if !found || principal.DevBypass || principal.Kind != access.PrincipalKindUser || principal.ID != actor {
		return access.ErrForbidden
	}
	issuer, err := accessmodule.CredentialTransactionEvidence(ctx, actor)
	if err != nil || issuer.Credential.Class != access.GrantCredentialClassSession {
		return access.ErrForbidden
	}
	// Absence is proved by the durable fence, including before a target row
	// exists. A failed serving-runtime acquisition never selects this path.
	return a.fence.WithUnpublishedTarget(ctx, a.targetID, resource.ProjectID, a.environment, func(ctx context.Context) error {
		tx, err := a.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if typednil.IsNil(tx) {
			return access.ErrForbidden
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		admission, err := a.admissions.AdmissionForTargetTx(ctx, tx, a.targetID)
		if err != nil {
			return err
		}
		i := admission.Intent
		if admission.Validate() != nil || i.TargetID != a.targetID || i.Environment != a.environment ||
			i.ProjectID != resource.ProjectID || i.ConnectionID != resource.ResourceID || i.OperatorPrincipalID != actor {
			return access.ErrForbidden
		}
		bootstrap := platformbootstrap.New(tx)
		instance, err := bootstrap.ExistingInstanceID(ctx)
		if err != nil {
			return err
		}
		environment, err := bootstrap.InstanceEnvironment(ctx)
		if err != nil {
			return err
		}
		owner, err := bootstrap.CustomerOwner(ctx)
		if err != nil {
			return err
		}
		claim, err := bootstrap.GetProjectClaim(ctx)
		if err != nil {
			return err
		}
		if instance != a.targetID || environment != a.environment || owner != i.CustomerOwnerID ||
			claim.Validate() != nil || claim.ProjectID != i.ProjectID || claim.Environment != i.Environment {
			return access.ErrForbidden
		}
		grant, err := i.Grant()
		if err != nil {
			return err
		}
		// This locks the current enabled principal and exact browser session.
		// The session establishes identity; the admission's staged grant below
		// establishes only these two exact connection permissions.
		if err := accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, grant.Permissions); err != nil {
			return err
		}
		binding, err := a.bindings.BindingForShareTx(ctx, tx, connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(i.ProjectID), Environment: i.Environment}, connectionbinding.TargetID(i.TargetID), projectgraph.ResourceID(i.ConnectionID))
		if err != nil {
			return err
		}
		if !firstSourceCredentialBindingMatches(admission, binding) {
			return access.ErrForbidden
		}
		scope := access.AuthorizationPolicyScope{TargetID: i.TargetID, ProjectID: i.ProjectID, Environment: i.Environment}
		policy, err := a.policyTx(ctx, tx, scope)
		if err != nil {
			return err
		}
		digest, err := access.AuthorizationPolicyDigest(scope, policy.RoleBindings, policy.Grants...)
		if err != nil || policy.Scope != scope || policy.Revision < admission.PolicyRevision || policy.Digest != digest {
			return access.ErrForbidden
		}
		found := false
		for _, current := range policy.Grants {
			if current.ID == grant.ID && reflect.DeepEqual(current, grant) {
				found = true
				break
			}
		}
		if !found {
			return access.ErrForbidden
		}
		if err := use(ctx, tx, admission); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

func firstSourceCredentialBindingMatches(admission credentialmodule.FirstSourceAdmission, binding connectionbinding.TargetBinding) bool {
	i := admission.Intent
	if binding.Validate() != nil || !binding.Enabled || binding.Revision != 1 || binding.ValidatedVersion != "" ||
		binding.ID.String() != i.BindingID || binding.TargetID.String() != i.TargetID || binding.ConnectionID.String() != i.ConnectionID ||
		binding.Scope.ProjectID.String() != i.ProjectID || binding.Scope.Environment != i.Environment ||
		binding.ConnectorKind != "postgres" || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return false
	}
	// Recompute the complete admitted identity from the locked live binding,
	// including every endpoint option and the real external-bundle reference.
	current := i
	current.Endpoint = credentialmodule.FirstSourceEndpoint(binding.Endpoint)
	current.CredentialReference = credentialmodule.FirstSourceCredentialReference(binding.CredentialReference)
	digest, err := current.BindingDigest()
	return err == nil && digest == admission.BindingDigest
}
