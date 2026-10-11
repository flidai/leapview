package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/admin/personalsettings"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func firstSourceTokenInput(t *testing.T, f *firstSourceAuthorityFixture) access.ScopedAPITokenInput {
	t.Helper()
	grant, err := f.admission.Intent.Grant()
	require.NoError(t, err)
	return access.ScopedAPITokenInput{PrincipalID: f.actor, Name: "first source", Permissions: grant.Permissions, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestFirstSourceTokenIssuerRequiresCurrentExactAuthority(t *testing.T) {
	for _, scenario := range []string{"allowed", "operator-only", "foreign-permission", "revoked-session", "revoked-grant", "revoked-owner", "api-credential", "authoring", "initial-publisher", "missing-session"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFirstSourceAuthorityFixtureWithPublisher(t, scenario != "operator-only")
			scope := firstSourceServiceScope(f)
			issuer := newFirstSourceTokenIssuer(&scope, f.repository)
			input := firstSourceTokenInput(t, f)
			ctx := f.ctx
			policyScope := access.AuthorizationPolicyScope{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment}
			switch scenario {
			case "foreign-permission":
				ref, err := access.NewResourceRef("connection:other", projectgraph.KindConnection)
				require.NoError(t, err)
				pair, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectgraph.ResourceID(f.resource.ProjectID), ref)
				require.NoError(t, err)
				input.Permissions = append(input.Permissions, pair)
			case "revoked-session":
				require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
			case "revoked-grant":
				grant, err := f.admission.Intent.Grant()
				require.NoError(t, err)
				grant.Permissions = grant.Permissions[1:]
				_, err = f.repository.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: policyScope, Grant: grant, ExpectedRevision: f.admission.PolicyRevision, IdempotencyKey: "revoke-grant"})
				require.NoError(t, err)
			case "revoked-owner":
				_, err := f.repository.RemoveAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingDeleteInput{Scope: policyScope, BindingID: access.BootstrapOwnerBindingID, ExpectedRevision: f.admission.PolicyRevision, IdempotencyKey: "revoke-owner"})
				require.NoError(t, err)
			case "authoring":
				ctx = accessmodule.WithAPICredential(accessmodule.WithPrincipal(t.Context(), accessmodule.Principal{ID: f.actor, Kind: access.PrincipalKindUser}), access.APICredential{Principal: access.Principal{ID: f.actor}, Authoring: &access.AuthoringSession{}})
			case "initial-publisher":
				ctx = accessmodule.WithAPICredential(accessmodule.WithPrincipal(t.Context(), accessmodule.Principal{ID: f.actor, Kind: access.PrincipalKindUser}), access.APICredential{Principal: access.Principal{ID: f.actor}, InitialPublisher: &access.InitialPublisherOrigin{}})
			case "api-credential":
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{Principal: access.Principal{ID: f.actor}})
			case "missing-session":
				ctx = accessmodule.WithPrincipal(t.Context(), accessmodule.Principal{ID: f.actor, Kind: access.PrincipalKindUser})
			}
			secret, handled, err := issuer.Create(ctx, input)
			require.True(t, handled)
			tokens, listErr := f.repository.ListAPITokens(t.Context(), f.actor)
			require.NoError(t, listErr)
			audits, auditErr := f.repository.ListAuditEvents(t.Context(), access.AuditEventFilter{PrincipalID: f.actor, Action: "api_token.created", Limit: 100})
			require.NoError(t, auditErr)
			if scenario == "allowed" {
				require.NoError(t, err)
				require.NotEmpty(t, secret)
				require.Len(t, tokens, 1)
				require.Equal(t, input.Permissions, tokens[0].Permissions)
				require.Len(t, audits, 1)
			} else {
				require.Error(t, err)
				require.Empty(t, secret)
				require.Empty(t, tokens)
				require.Empty(t, audits)
			}
		})
	}
}

func TestFirstSourceTokenIssuerPreservesNormalAuthority(t *testing.T) {
	f := newFirstSourceAuthorityFixtureWithPublisher(t, true)
	scope := firstSourceServiceScope(f)
	issuer := newFirstSourceTokenIssuer(&scope, f.repository)
	input := firstSourceTokenInput(t, f)
	for _, scenario := range []string{"published", "missing-admission", "provider-error"} {
		t.Run(scenario, func(t *testing.T) {
			current := scope
			failure := errors.New("target provider unavailable")
			switch scenario {
			case "published":
				current.targets = firstSourceCredentialTargetResult{target: deploymentpostgres.DeliveryTarget{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment, ActiveGenerationID: "generation:active"}}
			case "missing-admission":
				current.admissions = firstSourceTokenAdmissionFailure{err: credentialmodule.ErrValidationNotFound}
			case "provider-error":
				current.targets = firstSourceCredentialTargetResult{err: failure}
			}
			issuer.scope = &current
			secret, handled, err := issuer.Create(f.ctx, input)
			require.False(t, handled)
			require.Empty(t, secret)
			if scenario == "provider-error" {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type firstSourceTokenAdmissionFailure struct {
	credentialmodule.FirstSourceAdmissionReader
	err error
}

func (f firstSourceTokenAdmissionFailure) AdmissionForTarget(context.Context, string) (credentialmodule.FirstSourceAdmission, error) {
	return credentialmodule.FirstSourceAdmission{}, f.err
}

func TestFirstSourceTokenIssuerPreservesInstanceOnlyCreation(t *testing.T) {
	// An admitted operator need not be the claimant to keep independent instance authority.
	f := newFirstSourceAuthorityFixture(t)
	_, err := f.repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: f.actor, Role: access.PlatformRoleAdmin})
	require.NoError(t, err)
	scope := firstSourceServiceScope(f)
	issuer := newFirstSourceTokenIssuer(&scope, f.repository)
	instance, err := access.NewInstancePermissionPair(access.ActionPlatformAccessManage, f.resource.TargetID)
	require.NoError(t, err)
	secret, handled, err := issuer.Create(f.ctx, access.ScopedAPITokenInput{PrincipalID: f.actor, Name: "instance", Permissions: []access.PermissionPair{instance}})
	require.NoError(t, err)
	require.False(t, handled)
	require.Empty(t, secret)
	// Use the actual browser service's original audited path with its independent instance resolver.
	service := personalsettings.Service{Repository: f.repository, TokenIssuer: issuer, CurrentEffectivePermissionOptions: func(ctx context.Context, actor string) ([]access.PermissionPair, error) {
		return []access.PermissionPair{instance}, nil
	}}
	encoded, err := json.Marshal([]access.PermissionPair{instance})
	require.NoError(t, err)
	var signals []personalsettings.PermissionPairSignal
	require.NoError(t, json.Unmarshal(encoded, &signals))
	created, err := service.ApplyToken(f.ctx, f.actor, personalsettings.TokenCommand{Action: "create", Name: "instance", Permissions: signals})
	require.NoError(t, err)
	require.NotNil(t, created)
	state, err := service.Load(f.ctx, f.actor, f.session, true)
	require.NoError(t, err)
	require.Len(t, state.Tokens.Capabilities, 1)
	_, handled, err = issuer.PermissionOptions(f.ctx, f.actor)
	require.NoError(t, err)
	require.False(t, handled)
}

func TestFirstSourceTokenIssuerUsesOnlyFenceAndAuthorityConnections(t *testing.T) {
	f := newFirstSourceAuthorityFixtureWithPublisher(t, true)
	config := f.pool.Config()
	config.MaxConns = 2
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	defer pool.Close()
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte("kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk")})
	require.NoError(t, err)
	f.pool, f.repository = pool, repository
	f.authority.pool = pool
	f.authority.fence = deploymentpostgres.New(pool)
	scope := firstSourceServiceScope(f)
	issuer := newFirstSourceTokenIssuer(&scope, repository)
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	pairs, handled, err := issuer.PermissionOptions(ctx, f.actor)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotEmpty(t, pairs)
	secret, handled, err := issuer.Create(ctx, firstSourceTokenInput(t, f))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, secret != "")
	require.EqualValues(t, 0, pool.Stat().AcquiredConns())
}

func TestFirstSourceTokenIssuerCombinesOnlyCurrentInstanceAuthority(t *testing.T) {
	f := newFirstSourceAuthorityFixtureWithPublisher(t, true)
	scope := firstSourceServiceScope(f)
	issuer := newFirstSourceTokenIssuer(&scope, f.repository)
	instance, err := access.NewInstancePermissionPair(access.ActionPlatformAccessManage, f.resource.TargetID)
	require.NoError(t, err)
	pairs, handled, err := issuer.PermissionOptions(f.ctx, f.actor)
	require.NoError(t, err)
	require.True(t, handled)
	require.NotContains(t, pairs, instance)
	input := firstSourceTokenInput(t, f)
	input.Permissions = append(input.Permissions, instance)
	secret, handled, err := issuer.Create(f.ctx, input)
	require.ErrorIs(t, err, access.ErrTokenPermissionNotAllowed)
	require.True(t, handled)
	require.True(t, secret == "")
	_, err = f.repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: f.actor, Role: access.PlatformRoleAdmin})
	require.NoError(t, err)
	pairs, handled, err = issuer.PermissionOptions(f.ctx, f.actor)
	require.NoError(t, err)
	require.True(t, handled)
	require.Contains(t, pairs, instance)
	secret, handled, err = issuer.Create(f.ctx, input)
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, secret != "")
}
