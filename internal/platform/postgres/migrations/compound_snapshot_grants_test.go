package migrations

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func TestCompoundSnapshotGrantsUpgradePostgreSQL18(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	_, err := provider.UpTo(t.Context(), 54)
	require.NoError(t, err)
	projectID := graph.ResourceID("project_compound")
	resource, err := access.NewResourceRef("connection_events", graph.KindConnection)
	require.NoError(t, err)
	project, err := graph.NewProjectGraph([]graph.Resource{{ID: resource.ID(), Kind: resource.Kind(), Name: "events"}}, nil)
	require.NoError(t, err)
	pairs := make([]access.PermissionPair, 0, 2)
	for _, action := range []access.Action{access.ActionConnectionRead, access.ActionConnectionUpload} {
		pair, pairErr := access.NewExactPermissionPair(action, projectID, resource)
		require.NoError(t, pairErr)
		pairs = append(pairs, pair)
	}
	snapshotFor := func(generation string, authority []access.PermissionPair) accesssnapshot.AuthorizationSnapshot {
		t.Helper()
		grant, grantErr := accesssnapshot.NewTypedGrant("grant_upload", "Connection authority", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}, authority)
		require.NoError(t, grantErr)
		snapshot, snapshotErr := accesssnapshot.NewAuthorizationSnapshot(graph.ServingIdentity{ProjectID: projectID, Environment: "production", GenerationID: generation}, project, []accesssnapshot.Grant{grant}, nil)
		require.NoError(t, snapshotErr)
		return snapshot
	}
	install := func(snapshot accesssnapshot.AuthorizationSnapshot) error {
		t.Helper()
		tx, txErr := pool.Begin(t.Context())
		require.NoError(t, txErr)
		defer tx.Rollback(t.Context())
		if installErr := accesspostgres.InstallAuthorizationSnapshotTx(t.Context(), tx, snapshot); installErr != nil {
			return installErr
		}
		return tx.Commit(t.Context())
	}
	existing := snapshotFor("existing", pairs[:1])
	require.NoError(t, install(existing))
	digest, err := existing.Digest()
	require.NoError(t, err)
	compound := snapshotFor("compound", pairs)
	require.ErrorContains(t, install(compound), "authorization_grant_typed_permissions_check")
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	require.NoError(t, install(existing), "existing immutable snapshot must still replay")
	require.NoError(t, install(compound), "upgrade must retain the complete permission set")
	require.NoError(t, install(compound), "compound snapshot replay must remain idempotent")
	var storedDigest string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT digest FROM access.authorization_snapshot WHERE project_id=$1 AND generation_id='existing'`, projectID.String()).Scan(&storedDigest))
	require.Equal(t, digest, storedDigest)
	var encoded []byte
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT permissions FROM access.authorization_grant WHERE project_id=$1 AND generation_id='compound'`, projectID.String()).Scan(&encoded))
	decoded, err := access.DecodePermissionPairs(encoded)
	require.NoError(t, err)
	require.ElementsMatch(t, pairs, decoded)
	_, err = pool.Exec(t.Context(), `UPDATE access.authorization_grant SET permissions='[]'::jsonb WHERE project_id=$1 AND generation_id='compound'`, projectID.String())
	require.Error(t, err, "migration must preserve immutable captured authority")
	for _, malformed := range []string{`[]`, `[{"profile":"leapview.permissions/v1","action":"invalid","target":{}}]`} {
		_, err = pool.Exec(t.Context(), `INSERT INTO access.authorization_grant (id,project_id,environment,generation_id,subject_kind,subject_id,permission_profile,permissions) VALUES ('malformed',$1,'production','compound','principal','bob','leapview.permissions/v1',$2::jsonb)`, projectID.String(), malformed)
		require.Error(t, err, "empty or invalid authority must remain rejected")
	}
}
