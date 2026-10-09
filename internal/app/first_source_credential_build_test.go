package app

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func (f *firstSourcePlanAuthorityFixture) linkedPlan(t *testing.T) deployment.DeliveryPlan {
	t.Helper()
	evidence, err := f.authority.Resolve(f.ctx, f.request, f.target, f.candidate)
	require.NoError(t, err)
	plan := f.plan(t, evidence)
	_, err = deploymentpostgres.New(f.pool).CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: f.target.TargetID, ProjectID: f.target.ProjectID, Environment: f.target.Environment})
	require.NoError(t, err)
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	require.NoError(t, f.authority.BindTx(f.ctx, tx, f.request, f.target, plan))
	require.NoError(t, tx.Commit(t.Context()))
	return plan
}

type firstSourceBuildTestConnections struct {
	deployment.CandidateConnectionEvidenceResolver
	acquire func(context.Context, deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error)
}

func (c firstSourceBuildTestConnections) Acquire(ctx context.Context, request deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
	return c.acquire(ctx, request)
}

func TestFirstSourceBuildRuntimeScopeIsExactAndEndsAfterAcquisition(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	grant, err := f.admission.Intent.Grant()
	require.NoError(t, err)
	secret, token, err := f.repository.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: f.actor, Name: "same-actor-native-build", Permissions: grant.Permissions, ExpiresAt: time.Now().UTC().Add(time.Hour)})
	require.NoError(t, err)
	credential, err := f.repository.CredentialForAPIToken(t.Context(), secret)
	require.NoError(t, err)
	ctx := accessmodule.WithAPICredential(accessmodule.WithPrincipal(t.Context(), accessmodule.Principal{ID: f.actor, Kind: access.PrincipalKindUser}), credential)
	version := f.firstSourcePreparationServiceFixture.request.VersionID
	err = f.services.Runtime.UseVersion(ctx, f.resource, version, func(map[string]string) error { t.Fatal("ambient token reached credential fields"); return nil })
	require.Error(t, err)
	var escaped context.Context
	consumed := 0
	physical := firstSourceBuildTestConnections{acquire: func(ctx context.Context, _ deployment.CandidateConnectionRequest) (deployment.CandidateConnectionLeases, error) {
		escaped = ctx
		err := f.services.Runtime.UseVersion(ctx, f.resource, version, func(fields map[string]string) error {
			require.Equal(t, "saved-first-source-test-value", fields["password"])
			consumed++
			return nil
		})
		if err != nil {
			return nil, err
		}
		_, err = f.services.Drafts.ListDrafts(ctx, f.actor, f.resource, 20, "")
		require.Error(t, err, "the internal runtime scope cannot authorize browser draft APIs")
		return nil, nil
	}}
	selector := &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets, connections: func(connectionbinding.RuntimeBindingAuthorizer, connectionbinding.LocalCredentialPins) (appdeploymentpostgres.NativePlanConnectionAuthorities, error) {
		return appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: physical, Connections: physical}, nil
	}}
	selected, err := selector.Select(ctx, plan)
	require.NoError(t, err)
	_, err = selected.Connections.Acquire(ctx, f.candidate)
	require.NoError(t, err)
	require.Equal(t, 1, consumed)
	require.NoError(t, escaped.Err(), "scope revocation does not cancel the provider lease's work context")
	err = f.services.Runtime.UseVersion(escaped, f.resource, version, func(map[string]string) error { t.Fatal("escaped scope reached credential fields"); return nil })
	require.Error(t, err)
	require.NoError(t, f.repository.RevokeAPITokenForPrincipal(t.Context(), f.actor, token.ID))
	_, err = selected.Connections.Acquire(ctx, f.candidate)
	require.Error(t, err, "pool acquisition rechecks the current token even after selection")
	require.Equal(t, 1, consumed)
}

func TestFirstSourceBuildSelectionCannotUseAmbientAuthority(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	selector := &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets}
	_, err := selector.Select(t.Context(), plan)
	require.Error(t, err, "a stored plan link cannot authorize an anonymous build")
	_, err = selector.Select(f.ctx, plan)
	require.ErrorIs(t, err, credentialmodule.ErrValidationUnavailable, "missing physical authority must not fall back to ordinary credentials")
}

func TestFirstSourceBuildSelectionRetainsExactPinAndRechecksRevocation(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	var authorize connectionbinding.RuntimeBindingAuthorizer
	var pin connectionbinding.LocalCredentialPins
	selected := appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: candidateConnectionLeaser{}, Connections: candidateConnectionLeaser{}}
	selector := &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets, connections: func(a connectionbinding.RuntimeBindingAuthorizer, p connectionbinding.LocalCredentialPins) (appdeploymentpostgres.NativePlanConnectionAuthorities, error) {
		authorize, pin = a, p
		return selected, nil
	}}
	got, err := selector.Select(f.ctx, plan)
	require.NoError(t, err)
	require.NotNil(t, got.Connections)
	require.Same(t, got.Connections, got.BindingEvidence)
	bindings := f.service.authority.bindings.(credentialConnectionBindingLookup)
	binding, err := bindings.Binding(f.ctx, connectionbinding.BindingScope{ProjectID: plan.ProjectID, Environment: plan.Environment}, connectionbinding.TargetID(plan.TargetID), f.candidate.Requirements[0].ConnectionID)
	require.NoError(t, err)
	require.NoError(t, authorize(f.ctx, f.actor, binding))
	request := connectionbinding.RuntimeBindingRequest{Actor: f.actor, TargetID: binding.TargetID, Identity: f.candidate.Identity}
	version, err := pin(f.ctx, request, binding)
	require.NoError(t, err)
	require.Equal(t, f.firstSourcePreparationServiceFixture.request.VersionID, version)
	changed := binding
	changed.Revision++
	_, err = pin(f.ctx, request, changed)
	require.Error(t, err)
	request.Actor = uuid.NewString()
	_, err = pin(f.ctx, request, binding)
	require.Error(t, err)
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
	require.Error(t, authorize(f.ctx, f.actor, binding))
	request.Actor = f.actor
	_, err = pin(f.ctx, request, binding)
	require.Error(t, err, "a previously selected authority cannot outlive the request credential")
}

func TestFirstSourceBuildSelectionRejectsChangedPlanAndUsesNormalOnlyWithoutLink(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	normal := appdeploymentpostgres.NativePlanConnectionAuthorities{BindingEvidence: candidateConnectionLeaser{}, Connections: candidateConnectionLeaser{}}
	selector := &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets, normal: normal}
	for _, change := range []string{"source", "owner", "actor", "binding", "revision", "predecessor"} {
		t.Run(change, func(t *testing.T) {
			changed := plan
			switch change {
			case "source":
				changed.SourceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "owner":
				changed.SourceOwnerID = uuid.NewString()
			case "actor":
				changed.ActorID = uuid.NewString()
			case "binding":
				changed.Execution.BindingDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "revision":
				changed.BaseTargetRevision++
			case "predecessor":
				changed.BaseGenerationID = uuid.NewString()
			}
			_, err := selector.Select(f.ctx, changed)
			require.Error(t, err)
		})
	}
	unlinked := plan
	unlinked.ID = uuid.Must(uuid.NewV7()).String()
	got, err := selector.Select(t.Context(), unlinked)
	require.NoError(t, err)
	require.Equal(t, normal, got, "normal build retains its independent authorization")
}
