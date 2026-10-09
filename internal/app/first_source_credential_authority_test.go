package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	bindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deliverypostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type firstSourceAuthorityFixture struct {
	authority      firstSourceCredentialAuthority
	pool           *pgxpool.Pool
	repository     *accesspostgres.Repository
	admission      credentialmodule.FirstSourceAdmission
	resource       credentialmodule.ValidationResource
	ctx            context.Context
	session, actor string
}

type firstSourceAuthorityAudit struct{}

func (firstSourceAuthorityAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

func newFirstSourceAuthorityFixture(t *testing.T) *firstSourceAuthorityFixture {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "first_source_authority")
	pool, err := pgxpool.New(t.Context(), db.AdminURL())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	for _, apply := range []func(context.Context, pgx.Tx) error{
		func(ctx context.Context, tx pgx.Tx) error { return platformbootstrap.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx pgx.Tx) error { return accesspostgres.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx pgx.Tx) error { return bindingpostgres.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx pgx.Tx) error { return credentialpostgres.ApplySchema(ctx, tx) },
		func(ctx context.Context, tx pgx.Tx) error { return deliverypostgres.ApplySchema(ctx, tx) },
	} {
		require.NoError(t, apply(t.Context(), tx))
	}
	require.NoError(t, tx.Commit(t.Context()))
	bootstrap := platformbootstrap.New(pool)
	const target = "lvinst_0123456789abcdef0123456789abcdef"
	require.NoError(t, bootstrap.EnsureInstanceID(t.Context(), target))
	require.NoError(t, bootstrap.BindInstanceEnvironment(t.Context(), "prod"))
	_, err = bootstrap.DeclareCustomerOwner(t.Context(), "customer:first")
	require.NoError(t, err)
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("k", 32))})
	require.NoError(t, err)
	owner, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-owner@example.com"})
	require.NoError(t, err)
	operator, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-operator@example.com"})
	require.NoError(t, err)
	_, err = repository.ChangeLocalPassword(t.Context(), operator.Principal.ID, operator.Password, "A-real-first-source-password-42!")
	require.NoError(t, err)
	_, err = bootstrap.ClaimProject(t.Context(), platformbootstrap.ProjectClaimInput{ProjectID: "project:first", Environment: "prod", ClaimedBy: owner.Principal.ID, ClaimedAt: time.Now().UTC()})
	require.NoError(t, err)
	role, err := access.NewTypedRoleBinding("initial-owner", "Owner", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: owner.Principal.ID}, access.PermissionRoleProjectAdmin, "project:first")
	require.NoError(t, err)
	scope := access.AuthorizationPolicyScope{TargetID: target, ProjectID: "project:first", Environment: "prod"}
	policy, err := repository.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: role, IdempotencyKey: "initial-owner"})
	require.NoError(t, err)
	intent := credentialmodule.FirstSourceAdmissionIntent{Version: 1, OperationID: "0198f2c0-7c7a-7f00-8a11-000000000301", TargetID: target, ProjectID: scope.ProjectID, Environment: scope.Environment, CustomerOwnerID: "customer:first", OperatorPrincipalID: operator.Principal.ID, ConnectionID: "connection:warehouse", BindingID: "binding:first", ExpectedPolicyRevision: policy.Revision, ExpectedPolicyDigest: policy.Digest,
		Endpoint:            credentialmodule.FirstSourceEndpoint{Host: "postgres.internal", Port: 5432, Database: "analytics", TLSMode: "require"},
		CredentialReference: credentialmodule.FirstSourceCredentialReference{ProjectID: "project:first", Environment: "prod", SecretPath: "/customer/warehouse", SecretKey: "password"}}
	grant, err := intent.Grant()
	require.NoError(t, err)
	policy, err = repository.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: grant, ExpectedRevision: policy.Revision, IdempotencyKey: "first-source-grant"})
	require.NoError(t, err)
	digest, err := intent.Digest()
	require.NoError(t, err)
	bindingDigest, err := intent.BindingDigest()
	require.NoError(t, err)
	admission := credentialmodule.FirstSourceAdmission{Intent: intent, IntentDigest: digest, BindingDigest: bindingDigest, PolicyRevision: policy.Revision, PolicyDigest: policy.Digest, CreatedAt: time.Now().UTC()}
	audit := firstSourceAuthorityAudit{}
	bindings, err := bindingpostgres.NewProduction(pool, audit)
	require.NoError(t, err)
	bound, err := connectionbinding.NewTargetBinding(connectionbinding.TargetBindingInput{ID: connectionbinding.BindingID(intent.BindingID), TargetID: connectionbinding.TargetID(target), ConnectionID: projectgraph.ResourceID(intent.ConnectionID), ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle, Scope: connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(scope.ProjectID), Environment: "prod"}, Endpoint: connectionbinding.EndpointConfig(intent.Endpoint), CredentialReference: connectionbinding.CredentialReference(intent.CredentialReference), Enabled: true, Now: admission.CreatedAt})
	require.NoError(t, err)
	tx, err = pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	bindAudit, err := connectionbinding.BuildAdministrationAuditIntent(connectionbinding.AdministrationAuditInvocation{}, connectionbinding.AdministrationAuditEvent{ProjectID: bound.Scope.ProjectID, BindingID: bound.ID, TargetID: bound.TargetID, ConnectionID: bound.ConnectionID, Action: connectionbinding.AuditBindingCreated, Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: 1})
	require.NoError(t, err)
	bindAudit.ScopeID, bindAudit.ActorID = target, "offline_operator"
	require.NoError(t, bindings.CreateTx(connectionbinding.WithAuditIntent(t.Context(), bindAudit), tx, bound))
	admissions, err := credentialpostgres.NewFirstSourceAdmissions(pool, audit)
	require.NoError(t, err)
	admission, err = admissions.InsertAdmissionTx(t.Context(), tx, admission, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error { return nil })
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	reader, err := credentialmodule.NewFirstSourceAdmissionReader(pool, audit.RecordAuditEvent)
	require.NoError(t, err)
	session, err := repository.CreateSession(t.Context(), operator.Principal.ID, time.Hour)
	require.NoError(t, err)
	auth, err := accessmodule.NewAuth(repository, accessmodule.AuthConfig{LocalAuth: true, CSRFKey: strings.Repeat("k", 32)})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/connections", nil).WithContext(t.Context())
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName(), Value: session})
	var ctx context.Context
	auth.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), request)
	require.NotNil(t, ctx, "real browser authentication must create durable session evidence")
	return &firstSourceAuthorityFixture{pool: pool, repository: repository, admission: admission, actor: operator.Principal.ID, ctx: ctx, session: session,
		resource: credentialmodule.ValidationResource{ScopeKind: "connection", TargetID: target, ProjectID: scope.ProjectID, Environment: "prod", ResourceID: intent.ConnectionID},
		authority: firstSourceCredentialAuthority{production: true, targetID: target, environment: "prod", pool: pool, fence: deliverypostgres.New(pool), admissions: reader, bindings: bindings, policyTx: func(ctx context.Context, tx pgx.Tx, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
			policy, err := accesspostgres.AuthorizationPolicyTx(ctx, tx, scope)
			if err != nil {
				return access.AuthorizationPolicy{}, err
			}
			return accesspostgres.ValidateAuthorizationPolicyRevisionTx(ctx, tx, scope, policy.Revision, policy.Digest)
		}}}
}

type firstSourceAuthorityClosedFence struct{ err error }

func (f firstSourceAuthorityClosedFence) WithUnpublishedTarget(context.Context, string, string, string, func(context.Context) error) error {
	return f.err
}

type firstSourceAuthorityUnreadableAdmission struct{ err error }

func (r firstSourceAuthorityUnreadableAdmission) AdmissionForTargetTx(context.Context, pgx.Tx, string) (credentialmodule.FirstSourceAdmission, error) {
	return credentialmodule.FirstSourceAdmission{}, r.err
}

func TestFirstSourceCredentialAuthorityKeepsClosedOnMissingOrFailedAuthority(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	failure := errors.New("authority unavailable")
	for _, scenario := range []string{"published", "fence-failure", "journal-failure", "policy-failure", "missing-pool", "missing-fence", "missing-journal", "missing-binding", "missing-policy"} {
		t.Run(scenario, func(t *testing.T) {
			authority, want := f.authority, failure
			switch scenario {
			case "published":
				want = deliverypostgres.ErrAlreadyActive
				authority.fence = firstSourceAuthorityClosedFence{err: want}
			case "fence-failure":
				authority.fence = firstSourceAuthorityClosedFence{err: want}
			case "journal-failure":
				authority.admissions = firstSourceAuthorityUnreadableAdmission{err: want}
			case "policy-failure":
				authority.policyTx = func(context.Context, pgx.Tx, access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
					return access.AuthorizationPolicy{}, want
				}
			case "missing-pool":
				authority.pool = nil
				want = access.ErrForbidden
			case "missing-fence":
				authority.fence = nil
				want = access.ErrForbidden
			case "missing-journal":
				authority.admissions = nil
				want = access.ErrForbidden
			case "missing-binding":
				authority.bindings = nil
				want = access.ErrForbidden
			case "missing-policy":
				authority.policyTx = nil
				want = access.ErrForbidden
			}
			err := authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
				t.Fatal("unproved authority reached credential work")
				return nil
			})
			require.ErrorIs(t, err, want)
		})
	}
}

func TestFirstSourceCredentialAuthorityRequiresLiveExactSessionAdmission(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	called := false
	err := f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(ctx context.Context, tx pgx.Tx, admission credentialmodule.FirstSourceAdmission) error {
		called = true
		require.Equal(t, f.admission, admission)
		require.NotNil(t, tx)
		issuer, err := accessmodule.CredentialTransactionEvidence(ctx, f.actor)
		require.NoError(t, err)
		require.Equal(t, access.GrantCredentialClassSession, issuer.Credential.Class)
		return nil
	})
	require.NoError(t, err)
	require.True(t, called)
	_, err = deliverypostgres.New(f.pool).Target(t.Context(), f.resource.TargetID)
	require.ErrorIs(t, err, deliverypostgres.ErrNotFound, "the temporary fence must not manufacture a published or durable target")
}

func TestFirstSourceCredentialAuthorityRejectsCredentialOrScopeSubstitution(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	for _, scenario := range []string{"no-session", "api-token", "mixed-token", "dev-bypass", "other-actor", "other-target", "other-project", "other-environment", "other-connection", "upload", "development"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, actor, resource, action, authority := f.ctx, f.actor, f.resource, access.ActionConnectionManage, f.authority
			switch scenario {
			case "no-session":
				ctx = t.Context()
			case "api-token", "mixed-token":
				if scenario == "api-token" {
					ctx = t.Context()
				}
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{Principal: access.Principal{ID: f.actor}, Token: access.APIToken{ID: "0198f2c0-7c7a-7f00-8a11-000000000302", PrincipalID: f.actor, TokenFingerprint: strings.Repeat("a", 64)}})
			case "dev-bypass":
				ctx = accessmodule.WithPrincipal(ctx, accessmodule.Principal{ID: f.actor, DevBypass: true})
			case "other-actor":
				actor = "0198f2c0-7c7a-7f00-8a11-000000000303"
			case "other-target":
				resource.TargetID = "other"
			case "other-project":
				resource.ProjectID = "other"
			case "other-environment":
				resource.Environment = "other"
			case "other-connection":
				resource.ResourceID = "connection:other"
			case "upload":
				action = access.ActionConnectionUpload
			case "development":
				authority.production = false
			}
			called := false
			err := authority.WithAuthorization(ctx, actor, resource, action, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
		})
	}
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
	err := f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
		t.Fatal("revoked session reached credential work")
		return nil
	})
	require.ErrorIs(t, err, access.ErrForbidden)
}

func TestFirstSourceCredentialAuthorityPreservesCallbackFailure(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	failure := errors.New("credential mutation failed")
	_, err := f.pool.Exec(t.Context(), "CREATE TABLE public.first_source_authority_probe(id integer PRIMARY KEY)")
	require.NoError(t, err)
	require.ErrorIs(t, f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionUse, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.FirstSourceAdmission) error {
		_, err := tx.Exec(ctx, "INSERT INTO public.first_source_authority_probe VALUES(1)")
		require.NoError(t, err)
		return failure
	}), failure)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM public.first_source_authority_probe").Scan(&count))
	require.Zero(t, count, "a failed callback must roll back its transaction")
	ctx, cancel := context.WithCancel(f.ctx)
	require.ErrorIs(t, f.authority.WithAuthorization(ctx, f.actor, f.resource, access.ActionConnectionUse, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.FirstSourceAdmission) error {
		_, err := tx.Exec(ctx, "INSERT INTO public.first_source_authority_probe VALUES(1)")
		require.NoError(t, err)
		cancel()
		return nil
	}), context.Canceled)
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM public.first_source_authority_probe").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionUse, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.FirstSourceAdmission) error {
		_, err := tx.Exec(ctx, "INSERT INTO public.first_source_authority_probe VALUES(1)")
		return err
	}))
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM public.first_source_authority_probe").Scan(&count))
	require.Equal(t, 1, count, "successful explicit attachment can commit in this same bounded transaction")
}

type firstSourceAuthorityBindingChange struct {
	firstSourceCredentialBindings
	mutate func(*connectionbinding.TargetBinding)
}

func (r firstSourceAuthorityBindingChange) BindingForShareTx(ctx context.Context, tx pgx.Tx, scope connectionbinding.BindingScope, target connectionbinding.TargetID, id projectgraph.ResourceID) (connectionbinding.TargetBinding, error) {
	binding, err := r.firstSourceCredentialBindings.BindingForShareTx(ctx, tx, scope, target, id)
	if err == nil {
		r.mutate(&binding)
	}
	return binding, err
}

func TestFirstSourceCredentialAuthorityRejectsFullBindingDrift(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	for _, scenario := range []string{"binding", "revision", "connector", "authentication", "disabled", "endpoint", "options", "reference", "external-version"} {
		t.Run(scenario, func(t *testing.T) {
			authority := f.authority
			authority.bindings = firstSourceAuthorityBindingChange{firstSourceCredentialBindings: authority.bindings, mutate: func(binding *connectionbinding.TargetBinding) {
				switch scenario {
				case "binding":
					binding.ID = "binding:other"
				case "revision":
					binding.Revision++
				case "connector":
					binding.ConnectorKind = "sqlite"
				case "authentication":
					binding.AuthenticationMode = connectionbinding.AuthenticationNone
				case "disabled":
					binding.Enabled = false
				case "endpoint":
					binding.Endpoint.Database = "other"
				case "options":
					binding.Endpoint.Options = map[string]string{"application_name": "other"}
				case "reference":
					binding.CredentialReference.SecretKey = "other"
				case "external-version":
					binding.ValidatedVersion = "external:foreign"
				}
			}}
			err := authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
				t.Fatal("changed binding reached credential work")
				return nil
			})
			require.ErrorIs(t, err, access.ErrForbidden)
		})
	}
}

func TestFirstSourceCredentialAuthorityTracksCurrentStagedGrant(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	scope := access.AuthorizationPolicyScope{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment}
	reviewer, err := f.repository.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "first-reviewer@example.com"})
	require.NoError(t, err)
	role, err := access.NewTypedRoleBinding("reviewer", "Initial reviewer", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: reviewer.Principal.ID}, access.PermissionRoleReleaseApprover, projectgraph.ResourceID(f.resource.ProjectID))
	require.NoError(t, err)
	policy, err := f.repository.UpsertAuthorizationRoleBinding(t.Context(), access.AuthorizationRoleBindingInput{Scope: scope, Binding: role, ExpectedRevision: f.admission.PolicyRevision, IdempotencyKey: "reviewer"})
	require.NoError(t, err)
	require.Greater(t, policy.Revision, f.admission.PolicyRevision)
	require.NoError(t, f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error { return nil }), "initial reviewer nomination may advance policy while preserving the exact admitted grant")
	grant, err := f.admission.Intent.Grant()
	require.NoError(t, err)
	grant.Permissions = grant.Permissions[1:]
	_, err = f.repository.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: scope, Grant: grant, ExpectedRevision: policy.Revision, IdempotencyKey: "attenuate-first-source"})
	require.NoError(t, err)
	err = f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
		t.Fatal("revoked management reached credential work")
		return nil
	})
	require.ErrorIs(t, err, access.ErrForbidden)
}

func TestFirstSourceCredentialAuthorityHoldsSessionAndTargetUntilCallbackEnds(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	delivery := deliverypostgres.New(f.pool)
	_, err := delivery.CreateTarget(t.Context(), deliverypostgres.TargetInput{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment})
	require.NoError(t, err)
	err = f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(ctx context.Context, _ pgx.Tx, _ credentialmodule.FirstSourceAdmission) error {
		competing, err := f.pool.Begin(ctx)
		require.NoError(t, err)
		defer competing.Rollback(context.Background())
		_, err = competing.Exec(ctx, "SET LOCAL lock_timeout='100ms'")
		require.NoError(t, err)
		_, err = delivery.TargetForUpdateTx(ctx, competing, f.resource.TargetID)
		var postgresErr *pgconn.PgError
		require.ErrorAs(t, err, &postgresErr)
		require.Equal(t, "55P03", postgresErr.Code, "publication must wait until the bounded callback finishes")
		revokeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		require.Error(t, f.repository.DeleteSession(revokeCtx, f.session), "the live session row is locked through credential work")
		return nil
	})
	require.NoError(t, err)
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	_, err = delivery.TargetForUpdateTx(t.Context(), tx, f.resource.TargetID)
	require.NoError(t, err, "callback completion releases the target fence")
	require.NoError(t, tx.Rollback(t.Context()))
	_, err = f.repository.CredentialForSessionToken(t.Context(), f.session)
	require.NoError(t, err, "blocked revocation did not alter the live session")
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session), "callback completion releases the session lock")
}

func TestFirstSourceCredentialAuthorityRejectsDisabledPrincipalAndChangedStoredBinding(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	_, err := f.pool.Exec(t.Context(), "UPDATE access.principal SET blocked_at=clock_timestamp() WHERE id=$1::uuid", f.actor)
	require.NoError(t, err)
	err = f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
		t.Fatal("disabled operator reached credential work")
		return nil
	})
	require.ErrorIs(t, err, access.ErrForbidden)
	_, err = f.pool.Exec(t.Context(), "UPDATE access.principal SET blocked_at=NULL WHERE id=$1::uuid", f.actor)
	require.NoError(t, err)
	bindings := f.authority.bindings.(*bindingpostgres.Repository)
	binding, err := bindings.Binding(t.Context(), connectionbinding.BindingScope{ProjectID: projectgraph.ResourceID(f.resource.ProjectID), Environment: f.resource.Environment}, connectionbinding.TargetID(f.resource.TargetID), projectgraph.ResourceID(f.resource.ResourceID))
	require.NoError(t, err)
	configuration := binding.Configuration()
	configuration.Endpoint.Database = "different"
	configuration.CredentialReference.SecretPath = "/customer/different"
	binding, err = binding.UpdateConfiguration(configuration, time.Now().UTC())
	require.NoError(t, err)
	audit, err := connectionbinding.BuildAdministrationAuditIntent(connectionbinding.AdministrationAuditInvocation{}, connectionbinding.AdministrationAuditEvent{ProjectID: binding.Scope.ProjectID, BindingID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID, Action: connectionbinding.AuditBindingUpdated, Outcome: connectionbinding.AdministrationAuditSucceeded, Revision: 2})
	require.NoError(t, err)
	audit.ScopeID, audit.ActorID = f.resource.TargetID, "offline_operator"
	_, err = bindings.Save(connectionbinding.WithAuditIntent(t.Context(), audit), binding, 1)
	require.NoError(t, err)
	err = f.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error {
		t.Fatal("changed stored binding reached credential work")
		return nil
	})
	require.ErrorIs(t, err, access.ErrForbidden)
}
