package postgres

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestAuthorizationRoleBindingEncodingPersistsTypedRoleAndPairs(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	pairs, err := access.ExpandPermissionRole(access.PermissionRoleViewer, projectID)
	if err != nil {
		t.Fatal(err)
	}
	binding := access.RoleBinding{
		ID:                "binding-viewer",
		Subject:           access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"},
		PermissionProfile: access.PermissionCatalogProfile,
		Permissions:       pairs,
		PermissionRole:    access.PermissionRoleViewer,
	}
	legacy, encoded, profile, role, err := authorizationRoleBindingEncoding(binding)
	if err != nil {
		t.Fatal(err)
	}
	if legacy != nil || profile == nil || *profile != access.PermissionCatalogProfile || role == nil || *role != string(access.PermissionRoleViewer) {
		t.Fatalf("typed role encoding = legacy=%q profile=%v role=%v", legacy, profile, role)
	}
	decoded, err := access.DecodePermissionPairs(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(pairs) {
		t.Fatalf("decoded typed pair count = %d, want %d", len(decoded), len(pairs))
	}
	for i := range pairs {
		if decoded[i].Key() != pairs[i].Key() {
			t.Fatalf("decoded pair %d = %s, want %s", i, decoded[i].Key(), pairs[i].Key())
		}
	}
}

func TestAuthorizationRoleBindingEncodingRejectsOmittedLegacyCapabilities(t *testing.T) {
	_, _, _, _, err := authorizationRoleBindingEncoding(access.RoleBinding{
		ID:      "legacy-without-caps",
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"},
		Role:    access.ProjectRoleViewer,
	})
	if err == nil {
		t.Fatal("legacy role binding without captured capabilities unexpectedly encoded")
	}
}

func TestFreshSchemaHasTypedAssignmentIdentityIndexes(t *testing.T) {
	schema := SchemaSQL()
	for _, required := range []string{
		"permission_role text",
		"authorization_grant_typed_pair_key",
		"authorization_role_binding_typed_role_key",
		"authorization_policy_role_binding_typed_role_key",
		") NULLS NOT DISTINCT WHERE revoked_at IS NULL AND permission_profile IS NOT NULL",
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("fresh access schema missing typed assignment contract %q", required)
		}
	}
}

func TestTypedSnapshotProfileSupportsAdditiveLegacyMigration(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "dashboard_main", Kind: projectgraph.KindDashboard, Name: "main"}}, nil)
	require.NoError(t, err)
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"}
	typed, err := access.NewTypedRoleBinding("typed", "viewer", subject, access.PermissionRoleViewer, projectID)
	require.NoError(t, err)
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(
		projectgraph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: "generation"},
		project,
		[]accesssnapshot.RoleBinding{
			{ID: "legacy", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)},
			typed,
		},
		nil,
		nil,
	)
	require.NoError(t, err)
	profile, err := typedSnapshotProfile(snapshot)
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.Equal(t, access.PermissionCatalogProfile, *profile)
}
