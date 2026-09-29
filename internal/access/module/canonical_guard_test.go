package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestConnectionAuthorizerFromSnapshotDirectGroupAndDeny(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "connection_orders", Kind: projectgraph.KindConnection, Name: "orders"},
	}, nil)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	require.NoError(t, err)
	alice, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	require.NoError(t, err)
	sales, err := access.NewSubjectRef(access.SubjectKindGroup, "sales")
	require.NoError(t, err)
	resource, err := access.NewResourceRef("connection_orders", projectgraph.KindConnection)
	require.NoError(t, err)
	readPair, err := access.NewExactPermissionPair(access.ActionConnectionRead, identity.ProjectID, resource)
	require.NoError(t, err)
	managePair, err := access.NewExactPermissionPair(access.ActionConnectionManage, identity.ProjectID, resource)
	require.NoError(t, err)
	direct, err := accesssnapshot.NewTypedGrant("direct", "reader", alice, []access.PermissionPair{readPair})
	require.NoError(t, err)
	group, err := accesssnapshot.NewTypedGrant("group", "manager", sales, []access.PermissionPair{managePair})
	require.NoError(t, err)
	leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{
		direct, group,
	}, nil)
	require.NoError(t, err)
	provider := ConnectionAuthorizerFromSnapshot("instance_prod",
		func(_ context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil },
		func(_ context.Context, principalID string) ([]access.SubjectRef, error) {
			if principalID == "alice" {
				return []access.SubjectRef{alice, sales}, nil
			}
			return []access.SubjectRef{mustSubjectForTest(t, access.SubjectKindPrincipal, principalID)}, nil
		},
	)

	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = provider(context.Background(), "bob", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.False(t, allowed)
	allowed, err = provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionDashboardPublish)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestConnectionAuthorizerRequiresExactUploadPermission(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "connection_finance_files", Kind: projectgraph.KindConnection, Name: "finance_files"},
	}, nil)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	require.NoError(t, err)
	publisher, err := access.NewSubjectRef(access.SubjectKindPrincipal, "publisher")
	require.NoError(t, err)
	resource, err := access.NewResourceRef("connection_finance_files", projectgraph.KindConnection)
	require.NoError(t, err)
	uploadPair, err := access.NewExactPermissionPair(access.ActionConnectionUpload, identity.ProjectID, resource)
	require.NoError(t, err)
	managePair, err := access.NewExactPermissionPair(access.ActionConnectionManage, identity.ProjectID, resource)
	require.NoError(t, err)

	providerFor := func(pair access.PermissionPair) func(context.Context, string, string, string, access.Action) (bool, error) {
		grant, err := accesssnapshot.NewTypedGrant("publisher", "publisher", publisher, []access.PermissionPair{pair})
		require.NoError(t, err)
		leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant}, nil)
		require.NoError(t, err)
		return ConnectionAuthorizerFromSnapshot("instance_prod",
			func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil },
			func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{publisher}, nil },
		)
	}

	uploadProvider := providerFor(uploadPair)
	allowed, err := uploadProvider(context.Background(), "publisher", "project_demo", "connection_finance_files", access.ActionConnectionUpload)
	require.NoError(t, err)
	require.True(t, allowed, "an exact active upload grant must authorize managed-data uploads")
	allowed, err = uploadProvider(context.Background(), "publisher", "project_demo", "connection_finance_files", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "upload authority must not permit connection administration")

	manageProvider := providerFor(managePair)
	allowed, err = manageProvider(context.Background(), "publisher", "project_demo", "connection_finance_files", access.ActionConnectionUpload)
	require.NoError(t, err)
	require.False(t, allowed, "connection.manage must not imply upload authority")
}

func TestConnectionAuthorizerAppliesTypedTokenCeiling(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "connection_orders", Kind: projectgraph.KindConnection, Name: "orders"}}, nil)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	require.NoError(t, err)
	alice := mustSubjectForTest(t, access.SubjectKindPrincipal, "alice")
	resource, err := access.NewResourceRef("connection_orders", projectgraph.KindConnection)
	require.NoError(t, err)
	readPair, err := access.NewExactPermissionPair(access.ActionConnectionRead, identity.ProjectID, resource)
	require.NoError(t, err)
	managePair, err := access.NewExactPermissionPair(access.ActionConnectionManage, identity.ProjectID, resource)
	require.NoError(t, err)
	grant, err := accesssnapshot.NewTypedGrant("typed", "typed", alice, []access.PermissionPair{readPair, managePair})
	require.NoError(t, err)
	leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant}, nil)
	require.NoError(t, err)
	provider := ConnectionAuthorizerFromSnapshot("instance_prod", func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil }, func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{alice}, nil })

	readCredential := access.APICredential{
		Principal: access.Principal{ID: "alice"},
		Token:     access.APIToken{ID: "token", PrincipalID: "alice", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{readPair}},
	}
	ctx := WithAPICredential(context.Background(), readCredential)
	allowed, err := provider(ctx, "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = provider(ctx, "alice", "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "a token limited to connection.read must not authorize connection.manage")

	readCredential.Token.PrincipalID = "bob"
	ctx = WithAPICredential(context.Background(), readCredential)
	allowed, err = provider(ctx, "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.False(t, allowed, "a token for another principal must not authorize this connection")
}

func TestConnectionAuthorizerAppliesWorkloadAuthoringScope(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "connection_orders", Kind: projectgraph.KindConnection, Name: "orders"}}, nil)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	require.NoError(t, err)
	servicePrincipal := access.Principal{ID: "workload_demo", Kind: access.PrincipalKindServicePrincipal}
	subject := mustSubjectForTest(t, access.SubjectKindPrincipal, servicePrincipal.ID)
	resource, err := access.NewResourceRef("connection_orders", projectgraph.KindConnection)
	require.NoError(t, err)
	managePair, err := access.NewExactPermissionPair(access.ActionConnectionManage, identity.ProjectID, resource)
	require.NoError(t, err)
	grant, err := accesssnapshot.NewTypedGrant("typed", "typed", subject, []access.PermissionPair{managePair})
	require.NoError(t, err)
	leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{grant}, nil)
	require.NoError(t, err)
	provider := ConnectionAuthorizerFromSnapshot("instance_prod",
		func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil },
		func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
	)

	scope, err := access.NewAuthoringScope("instance_prod", identity.ProjectID, []access.PermissionPair{managePair})
	require.NoError(t, err)
	credential := access.APICredential{
		Principal: servicePrincipal,
		Token: access.APIToken{
			ID: "workload_credential", PrincipalID: servicePrincipal.ID,
		},
		Authoring: &access.AuthoringSession{
			ID: "workload_session", Kind: access.AuthoringSessionWorkload,
			ClientID: servicePrincipal.ID, PrincipalID: servicePrincipal.ID, Scope: scope,
		},
	}
	ctx := WithAPICredential(context.Background(), credential)
	allowed, err := provider(ctx, servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.True(t, allowed, "a workload credential with the exact typed action and resource in its authoring scope should pass")

	emptySnapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, nil, nil)
	require.NoError(t, err)
	noGrantProvider := ConnectionAuthorizerFromSnapshot("instance_prod",
		func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return emptySnapshot, nil },
		func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
	)
	allowed, err = noGrantProvider(ctx, servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "an authoring scope must not authorize a pair absent from the active snapshot")

	missingInstanceProvider := ConnectionAuthorizerFromSnapshot("",
		func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil },
		func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{subject}, nil },
	)
	allowed, err = missingInstanceProvider(ctx, servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "an authoring credential must fail closed when no runtime instance binding is configured")

	otherResource, err := access.NewResourceRef("connection_customers", projectgraph.KindConnection)
	require.NoError(t, err)
	otherPair, err := access.NewExactPermissionPair(access.ActionConnectionManage, identity.ProjectID, otherResource)
	require.NoError(t, err)
	otherScope, err := access.NewAuthoringScope("instance_prod", identity.ProjectID, []access.PermissionPair{otherPair})
	require.NoError(t, err)
	credential.Authoring.Scope = otherScope
	allowed, err = provider(WithAPICredential(context.Background(), credential), servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "a workload credential scoped to another connection must remain denied")

	credential.Authoring.Scope = scope
	credential.Authoring.Scope.TargetID = "instance_other"
	allowed, err = provider(WithAPICredential(context.Background(), credential), servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "a workload credential scoped to another instance must remain denied")

	credential.Authoring.Scope = scope
	credential.Authoring.Scope.Permissions = []access.PermissionPair{}
	allowed, err = provider(WithAPICredential(context.Background(), credential), servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "an empty workload scope must not authorize a typed connection action")

	credential.Authoring.Scope = scope
	credential.Token.PrincipalID = "another_principal"
	allowed, err = provider(WithAPICredential(context.Background(), credential), servicePrincipal.ID, "project_demo", "connection_orders", access.ActionConnectionManage)
	require.NoError(t, err)
	require.False(t, allowed, "a workload credential for another principal must remain denied")
}

func TestConnectionAuthorizerIgnoresLegacyCapabilityGrant(t *testing.T) {
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "connection_orders", Kind: projectgraph.KindConnection, Name: "orders"}}, nil)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	require.NoError(t, err)
	alice, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	require.NoError(t, err)
	resource, err := access.NewResourceRef("connection_orders", projectgraph.KindConnection)
	require.NoError(t, err)
	legacy, err := access.NewCanonicalGrant(project, alice, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{{ID: "legacy", Canonical: legacy}}, nil)
	require.NoError(t, err)
	provider := ConnectionAuthorizerFromSnapshot("instance_prod", func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil }, func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{alice}, nil })
	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestConnectionAuthorizerFromSnapshotFailsClosedWithoutProviders(t *testing.T) {
	provider := ConnectionAuthorizerFromSnapshot("instance_prod", nil, nil)
	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.Error(t, err)
	require.False(t, allowed)
}

func mustSubjectForTest(t *testing.T, kind access.SubjectKind, id string) access.SubjectRef {
	t.Helper()
	subject, err := access.NewSubjectRef(kind, id)
	require.NoError(t, err)
	return subject
}
