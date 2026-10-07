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

func TestTypedSnapshotPersistsCompoundGrantPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	projectID := projectgraph.ResourceID("project_compound")
	resource, err := access.NewResourceRef("connection_events", projectgraph.KindConnection)
	require.NoError(t, err)
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: resource.ID(), Kind: resource.Kind(), Name: "events"}}, nil)
	require.NoError(t, err)
	pairs := make([]access.PermissionPair, 0, 2)
	for _, action := range []access.Action{access.ActionConnectionRead, access.ActionConnectionUpload} {
		pair, pairErr := access.NewExactPermissionPair(action, projectID, resource)
		require.NoError(t, pairErr)
		pairs = append(pairs, pair)
	}
	grant, err := accesssnapshot.NewTypedGrant("grant_upload", "Upload and read", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}, pairs)
	require.NoError(t, err)
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: "generation_compound"}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{grant}, nil)
	require.NoError(t, err)
	tx, err := db.runtime.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(t.Context())
	require.NoError(t, InstallAuthorizationSnapshotTx(t.Context(), tx, snapshot))
	require.NoError(t, InstallAuthorizationSnapshotTx(t.Context(), tx, snapshot), "exact snapshot replay must remain idempotent")
	require.NoError(t, tx.Commit(t.Context()))
	var encoded []byte
	var profile string
	var legacyAbsent bool
	require.NoError(t, db.admin.QueryRow(t.Context(), `SELECT permission_profile, permissions, resource_id IS NULL AND resource_kind IS NULL AND capability IS NULL FROM access.authorization_grant WHERE project_id=$1 AND generation_id=$2 AND id=$3`, projectID.String(), identity.GenerationID, grant.ID).Scan(&profile, &encoded, &legacyAbsent))
	require.Equal(t, access.PermissionCatalogProfile, profile)
	require.True(t, legacyAbsent)
	decoded, err := access.DecodePermissionPairs(encoded)
	require.NoError(t, err)
	require.ElementsMatch(t, pairs, decoded)
	_, err = db.admin.Exec(t.Context(), `UPDATE access.authorization_grant SET permissions='[]'::jsonb WHERE project_id=$1 AND generation_id=$2`, projectID.String(), identity.GenerationID)
	require.Error(t, err, "installed authority must stay immutable")
}
