package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	appaccesspostgres "github.com/flidai/leapview/internal/app/accesspostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

type firstSourceCredentialTargetResult struct {
	target deploymentpostgres.DeliveryTarget
	err    error
}

func (r firstSourceCredentialTargetResult) Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error) {
	return r.target, r.err
}

func firstSourceServiceScope(f *firstSourceAuthorityFixture) firstSourceCredentialServiceScope {
	authority := f.authority
	authority.policyTx = appaccesspostgres.LockedCurrentAuthorizationPolicyTx
	return firstSourceCredentialServiceScope{
		authority: authority, targets: deploymentpostgres.New(f.pool),
		admissions: f.authority.admissions.(credentialmodule.FirstSourceAdmissionReader),
		activeProject: func(context.Context) (projectgraph.ResourceID, error) {
			return "", errors.New("active runtime unavailable")
		},
		activeAuthorize: func(context.Context, string, string, string, access.Action) (bool, error) {
			return false, errors.New("active runtime unavailable")
		},
	}
}

func TestFirstSourceCredentialServicesUseOnlyExactAdmittedOperator(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	s := firstSourceServiceScope(f)
	project, err := s.CurrentProject(f.ctx)
	require.NoError(t, err, "explicitly unpublished admitted target needs no active runtime")
	require.Equal(t, projectgraph.ResourceID(f.resource.ProjectID), project)
	for _, action := range []access.Action{access.ActionConnectionManage, access.ActionConnectionUse, access.ActionConnectionRead} {
		allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, action)
		require.NoError(t, err)
		require.True(t, allowed, "credential metadata reads recheck exact manage without granting ordinary connection.read")
	}
	for _, scenario := range []string{"foreign-connection", "foreign-project", "foreign-actor", "upload", "api-token"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, actor, project, connection, action := f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead
			switch scenario {
			case "foreign-connection":
				connection = "connection:other"
			case "foreign-project":
				project = "project:other"
			case "foreign-actor":
				actor = "0198f2c0-7c7a-7f00-8a11-000000000399"
			case "upload":
				action = access.ActionConnectionUpload
			case "api-token":
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{Principal: access.Principal{ID: f.actor}})
			}
			allowed, err := s.AuthorizeConnection(ctx, actor, project, connection, action)
			require.Error(t, err)
			require.False(t, allowed)
		})
	}
	for _, ctx := range []context.Context{context.Background(), accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: f.actor, Kind: access.PrincipalKindUser})} {
		_, err := s.CurrentProject(ctx)
		require.Error(t, err, "background/publisher identity alone cannot select prepublication credential scope")
		allowed, err := s.AuthorizeConnection(ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
		require.Error(t, err)
		require.False(t, allowed)
	}

	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
	allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
	require.Error(t, err)
	require.False(t, allowed)
	_, err = s.CurrentProject(f.ctx)
	require.Error(t, err, "project resolution must not revive a revoked session")
}

func TestFirstSourceCredentialServicesPreserveActiveSnapshotDecision(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	s := firstSourceServiceScope(f)
	normalFailure := errors.New("serving snapshot provider failed")
	var projectCalls, authCalls int
	s.activeProject = func(context.Context) (projectgraph.ResourceID, error) { projectCalls++; return "", normalFailure }
	s.activeAuthorize = func(_ context.Context, _ string, _ string, _ string, action access.Action) (bool, error) {
		authCalls++
		require.Equal(t, access.ActionConnectionRead, action)
		return false, normalFailure
	}
	for _, pointers := range [][2]string{{"generation:active", "publication:active"}, {"generation:warming", ""}, {"", "publication:committed"}} {
		s.targets = firstSourceCredentialTargetResult{target: deploymentpostgres.DeliveryTarget{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment, ActiveGenerationID: pointers[0], ActivePublicationID: pointers[1]}}
		_, err := s.CurrentProject(f.ctx)
		require.ErrorIs(t, err, normalFailure)
		allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
		require.ErrorIs(t, err, normalFailure)
		require.False(t, allowed)
	}
	require.Equal(t, 3, projectCalls)
	require.Equal(t, 3, authCalls)
	// Even an ordinary snapshot denial without a provider failure is final.
	s.activeAuthorize = func(context.Context, string, string, string, access.Action) (bool, error) { return false, nil }
	allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestFirstSourceCredentialServicesPreserveDevelopmentPorts(t *testing.T) {
	s := firstSourceCredentialServiceScope{
		targets:       firstSourceCredentialTargetResult{err: errors.New("development must not read target")},
		activeProject: func(context.Context) (projectgraph.ResourceID, error) { return "project:development", nil },
		activeAuthorize: func(_ context.Context, actor, project, connection string, action access.Action) (bool, error) {
			require.Equal(t, "development-user", actor)
			require.Equal(t, "project:development", project)
			require.Equal(t, "connection:warehouse", connection)
			require.Equal(t, access.ActionConnectionRead, action)
			return true, nil
		},
	}
	project, err := s.CurrentProject(t.Context())
	require.NoError(t, err)
	require.Equal(t, projectgraph.ResourceID("project:development"), project)
	allowed, err := s.AuthorizeConnection(t.Context(), "development-user", string(project), "connection:warehouse", access.ActionConnectionRead)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestFirstSourceCredentialServicesNeverInferUnpublishedFromProviderFailure(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	s := firstSourceServiceScope(f)
	failure := errors.New("durable target read failed")
	s.targets = firstSourceCredentialTargetResult{err: failure}
	s.activeProject = func(context.Context) (projectgraph.ResourceID, error) {
		t.Fatal("target failure fell through to runtime")
		return "", nil
	}
	s.activeAuthorize = func(context.Context, string, string, string, access.Action) (bool, error) {
		t.Fatal("target failure fell through to runtime")
		return true, nil
	}
	_, err := s.CurrentProject(f.ctx)
	require.ErrorIs(t, err, failure)
	allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
	require.ErrorIs(t, err, failure)
	require.False(t, allowed)
}

func TestFirstSourceCredentialMetadataReadRequiresManageRatherThanUseAlone(t *testing.T) {
	f := newFirstSourceAuthorityFixture(t)
	s := firstSourceServiceScope(f)
	grant, err := f.admission.Intent.Grant()
	require.NoError(t, err)
	grant.Permissions = grant.Permissions[1:]
	_, err = f.repository.UpsertAuthorizationGrant(t.Context(), access.AuthorizationGrantInput{Scope: access.AuthorizationPolicyScope{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment}, Grant: grant, ExpectedRevision: f.admission.PolicyRevision, IdempotencyKey: "metadata-management-revoked"})
	require.NoError(t, err)
	allowed, err := s.AuthorizeConnection(f.ctx, f.actor, f.resource.ProjectID, f.resource.ResourceID, access.ActionConnectionRead)
	require.Error(t, err)
	require.False(t, allowed)
}
