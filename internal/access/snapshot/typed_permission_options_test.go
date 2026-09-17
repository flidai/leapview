package snapshot

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func typedOptionsGraph(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "pipeline_b", Kind: graph.KindPipeline, Name: "pipeline_b"},
		{ID: "semantic_b", Kind: graph.KindSemanticModel, Name: "semantic_b"},
		{ID: "dashboard_b", Kind: graph.KindDashboard, Name: "dashboard_b"},
		{ID: "dashboard_a", Kind: graph.KindDashboard, Name: "dashboard_a"},
		{ID: "pipeline_a", Kind: graph.KindPipeline, Name: "pipeline_a"},
		{ID: "semantic_a", Kind: graph.KindSemanticModel, Name: "semantic_a"},
	}, nil)
	require.NoError(t, err)
	return project
}

func typedOptionsIdentity() graph.ServingIdentity {
	return graph.ServingIdentity{ProjectID: "project_demo", Environment: "production", GenerationID: "generation_1"}
}

func typedOptionsGrant(t *testing.T, project graph.ProjectGraph, id, subjectID, resourceID string, kind graph.Kind, capability access.Capability) Grant {
	t.Helper()
	resource, err := access.NewResourceRef(graph.ResourceID(resourceID), kind)
	require.NoError(t, err)
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, subjectID)
	require.NoError(t, err)
	grant, err := access.NewCanonicalGrant(project, subject, resource, capability)
	require.NoError(t, err)
	return Grant{ID: id, Canonical: grant}
}

func typedOptionsSubjects(t *testing.T, ids ...string) []access.SubjectRef {
	t.Helper()
	result := make([]access.SubjectRef, 0, len(ids))
	for _, id := range ids {
		result = append(result, mustSubject(t, access.SubjectKindPrincipal, id))
	}
	return result
}

func typedOptionsPairs(t *testing.T, snapshot AuthorizationSnapshot, subjects ...access.SubjectRef) []access.PermissionPair {
	t.Helper()
	pairs, err := snapshot.EffectiveTypedPermissionOptions(subjects)
	require.NoError(t, err)
	return pairs
}

func TestEffectiveTypedPermissionOptionsProjectsDirectQualifiedGrants(t *testing.T) {
	project := typedOptionsGraph(t)
	snapshot, err := NewAuthorizationSnapshot(typedOptionsIdentity(), project, []Grant{
		typedOptionsGrant(t, project, "dashboard-read", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourceRead),
		typedOptionsGrant(t, project, "semantic-use", "alice", "semantic_a", graph.KindSemanticModel, access.CapabilityResourceUse),
		typedOptionsGrant(t, project, "pipeline-use", "alice", "pipeline_a", graph.KindPipeline, access.CapabilityResourceUse),
	}, nil)
	require.NoError(t, err)

	pairs := typedOptionsPairs(t, snapshot, typedOptionsSubjects(t, "alice")...)
	require.Equal(t, []access.PermissionPair{
		exactTypedPair(t, access.ActionDashboardRead, "dashboard_a", graph.KindDashboard),
		exactTypedPair(t, access.ActionPipelineRun, "pipeline_a", graph.KindPipeline),
		exactTypedPair(t, access.ActionSemanticConsume, "semantic_a", graph.KindSemanticModel),
	}, pairs)
}

func TestEffectiveTypedPermissionOptionsProjectsQualifiedRoleAccess(t *testing.T) {
	project := typedOptionsGraph(t)
	subject := mustSubject(t, access.SubjectKindPrincipal, "alice")
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(typedOptionsIdentity(), project, []RoleBinding{{
		ID: "viewer", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}}, nil, nil)
	require.NoError(t, err)

	pairs := typedOptionsPairs(t, snapshot, subject)
	require.Len(t, pairs, 4)
	for _, pair := range pairs {
		switch pair.Target.ResourceKind {
		case graph.KindDashboard:
			require.Equal(t, access.ActionDashboardRead, pair.Action)
		case graph.KindSemanticModel:
			require.Equal(t, access.ActionSemanticConsume, pair.Action)
		default:
			t.Fatalf("unexpected role-derived target = %#v", pair)
		}
	}
}

func TestEffectiveTypedPermissionOptionsDoesNotTurnViewerRoleIntoPipelineRun(t *testing.T) {
	project := typedOptionsGraph(t)
	subject := mustSubject(t, access.SubjectKindPrincipal, "alice")
	snapshot, err := NewAuthorizationSnapshotWithRoleBindings(typedOptionsIdentity(), project, []RoleBinding{{
		ID: "viewer", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer),
	}}, nil, nil)
	require.NoError(t, err)

	for _, pair := range typedOptionsPairs(t, snapshot, subject) {
		require.NotEqual(t, access.ActionPipelineRun, pair.Action)
	}
}

func TestEffectiveTypedPermissionOptionsPreservesCrossResourceIsolation(t *testing.T) {
	project := typedOptionsGraph(t)
	snapshot, err := NewAuthorizationSnapshot(typedOptionsIdentity(), project, []Grant{
		typedOptionsGrant(t, project, "dashboard-read", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourceRead),
		typedOptionsGrant(t, project, "semantic-use", "alice", "semantic_a", graph.KindSemanticModel, access.CapabilityResourceUse),
		typedOptionsGrant(t, project, "pipeline-use", "alice", "pipeline_a", graph.KindPipeline, access.CapabilityResourceUse),
	}, nil)
	require.NoError(t, err)

	pairs := typedOptionsPairs(t, snapshot, typedOptionsSubjects(t, "alice")...)
	for _, pair := range pairs {
		require.NotEqual(t, graph.ResourceID("dashboard_b"), pair.Target.ResourceID)
		require.NotEqual(t, graph.ResourceID("semantic_b"), pair.Target.ResourceID)
		require.NotEqual(t, graph.ResourceID("pipeline_b"), pair.Target.ResourceID)
	}
}

func TestEffectiveTypedPermissionOptionsHasStableOrderAndDeduplication(t *testing.T) {
	project := typedOptionsGraph(t)
	snapshot, err := NewAuthorizationSnapshot(typedOptionsIdentity(), project, []Grant{
		typedOptionsGrant(t, project, "dashboard-principal", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourceRead),
		typedOptionsGrant(t, project, "dashboard-group", "sales", "dashboard_a", graph.KindDashboard, access.CapabilityResourceRead),
		typedOptionsGrant(t, project, "semantic", "alice", "semantic_a", graph.KindSemanticModel, access.CapabilityResourceUse),
	}, nil)
	require.NoError(t, err)
	sales := mustSubject(t, access.SubjectKindGroup, "sales")
	alice := mustSubject(t, access.SubjectKindPrincipal, "alice")
	pairs := typedOptionsPairs(t, snapshot, alice, sales, alice)
	require.Equal(t, []access.PermissionPair{
		exactTypedPair(t, access.ActionDashboardRead, "dashboard_a", graph.KindDashboard),
		exactTypedPair(t, access.ActionSemanticConsume, "semantic_a", graph.KindSemanticModel),
	}, pairs)
}

func TestEffectiveTypedPermissionOptionsOmitsUnsafeLegacyMappings(t *testing.T) {
	project := typedOptionsGraph(t)
	snapshot, err := NewAuthorizationSnapshot(typedOptionsIdentity(), project, []Grant{
		typedOptionsGrant(t, project, "dashboard-edit", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourceEdit),
		typedOptionsGrant(t, project, "dashboard-share", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourceShare),
		typedOptionsGrant(t, project, "dashboard-publish", "alice", "dashboard_a", graph.KindDashboard, access.CapabilityResourcePublish),
		typedOptionsGrant(t, project, "semantic-read", "alice", "semantic_a", graph.KindSemanticModel, access.CapabilityResourceRead),
		typedOptionsGrant(t, project, "pipeline-read", "alice", "pipeline_a", graph.KindPipeline, access.CapabilityResourceRead),
	}, nil)
	require.NoError(t, err)
	pairs := typedOptionsPairs(t, snapshot, typedOptionsSubjects(t, "alice")...)
	require.Empty(t, pairs)
}

func exactTypedPair(t *testing.T, action access.Action, resourceID string, kind graph.Kind) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(graph.ResourceID(resourceID), kind)
	require.NoError(t, err)
	pair, err := access.NewExactPermissionPair(action, graph.ResourceID("project_demo"), resource)
	require.NoError(t, err)
	return pair
}
