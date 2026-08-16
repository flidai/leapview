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
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "demo"},
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
	direct, err := access.NewCanonicalGrant(project, alice, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	group, err := access.NewCanonicalGrant(project, sales, resource, access.CapabilityResourceEdit)
	require.NoError(t, err)
	leased, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, []accesssnapshot.Grant{
		{ID: "direct", Canonical: direct}, {ID: "group", Canonical: group},
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

	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.CapabilityResourceRead)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = provider(context.Background(), "alice", "project_demo", "connection_orders", access.CapabilityResourceEdit)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = provider(context.Background(), "bob", "project_demo", "connection_orders", access.CapabilityResourceRead)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestConnectionAuthorizerFromSnapshotFailsClosedWithoutProviders(t *testing.T) {
	provider := ConnectionAuthorizerFromSnapshot(nil, nil)
	allowed, err := provider(context.Background(), "alice", "project_demo", "connection_orders", access.CapabilityResourceRead)
	require.Error(t, err)
	require.False(t, allowed)
}

func mustSubjectForTest(t *testing.T, kind access.SubjectKind, id string) access.SubjectRef {
	t.Helper()
	subject, err := access.NewSubjectRef(kind, id)
	require.NoError(t, err)
	return subject
}
