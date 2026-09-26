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
	provider := ConnectionAuthorizerFromSnapshot(
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
	provider := ConnectionAuthorizerFromSnapshot(func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil }, func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{alice}, nil })

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
	provider := ConnectionAuthorizerFromSnapshot(func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return leased, nil }, func(context.Context, string) ([]access.SubjectRef, error) { return []access.SubjectRef{alice}, nil })
	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.ActionConnectionRead)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestConnectionAuthorizerFromSnapshotFailsClosedWithoutProviders(t *testing.T) {
	provider := ConnectionAuthorizerFromSnapshot(nil, nil)
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
