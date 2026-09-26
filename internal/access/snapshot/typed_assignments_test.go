package snapshot

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestTypedSnapshotPersistsRoleExpansionAndPrerequisites(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
		{ID: "semantic_orders", Kind: graph.KindSemanticModel, Name: "orders"},
	}, nil)
	require.NoError(t, err)
	identity := graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_typed"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	binding, err := access.NewTypedRoleBinding("binding", "explorer", subject, access.PermissionRoleExplorer, identity.ProjectID)
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, []RoleBinding{binding}, nil, nil)
	require.NoError(t, err)

	query, err := access.NewExactPermissionPair(access.ActionSemanticQuery, identity.ProjectID, mustResource(t, "semantic_orders", graph.KindSemanticModel))
	require.NoError(t, err)
	allowed, err := snapshot.AllowsTyped(subject, query)
	require.NoError(t, err)
	require.True(t, allowed, "explorer expansion must include semantic.query and its consume prerequisite")

	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "permissionProfile")
	decoded, err := Decode(encoded, project)
	require.NoError(t, err)
	require.Equal(t, snapshot.RoleBindings()[0].Permissions, decoded.RoleBindings()[0].Permissions)
}

func TestTypedSnapshotIgnoresAmbiguousLegacyRoleForTypedEvaluation(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"}}, nil)
	require.NoError(t, err)
	identity := graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_legacy"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, []RoleBinding{{ID: "legacy", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}}, nil, nil)
	require.NoError(t, err)
	requested, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, mustResource(t, "dashboard_main", graph.KindDashboard))
	require.NoError(t, err)
	allowed, err := snapshot.AllowsTyped(subject, requested)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestTypedSnapshotSupportsAdditiveMigrationBesideLegacyRole(t *testing.T) {
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"}}, nil)
	require.NoError(t, err)
	identity := graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_mixed"}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	typed, err := access.NewTypedRoleBinding("typed", "viewer", subject, access.PermissionRoleViewer, identity.ProjectID)
	require.NoError(t, err)
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(identity, project, []RoleBinding{
		{ID: "legacy", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)},
		typed,
	}, nil, nil)
	require.NoError(t, err)

	requested, err := access.NewExactPermissionPair(access.ActionDashboardRead, identity.ProjectID, mustResource(t, "dashboard_main", graph.KindDashboard))
	require.NoError(t, err)
	allowed, err := snapshot.AllowsTyped(subject, requested)
	require.NoError(t, err)
	require.True(t, allowed)
}

func mustResource(t *testing.T, id string, kind graph.Kind) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(graph.ResourceID(id), kind)
	require.NoError(t, err)
	return resource
}
