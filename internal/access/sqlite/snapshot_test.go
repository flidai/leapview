package sqlite

import (
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/stretchr/testify/require"
)

func installerGraph(t *testing.T) graph.ProjectGraph {
	t.Helper()
	project, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_test", Kind: graph.KindProject, Name: "test"},
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "main"},
	}, nil)
	require.NoError(t, err)
	return project
}

func installerSnapshot(t *testing.T, project graph.ProjectGraph, generation, name string) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	resource, err := access.NewResourceRef("dashboard_main", graph.KindDashboard)
	require.NoError(t, err)
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal_test")
	require.NoError(t, err)
	grant, err := access.NewCanonicalGrant(project, subject, resource, access.CapabilityResourceRead)
	require.NoError(t, err)
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(graph.ServingIdentity{
		ProjectID: project.ProjectID(), Environment: "production", GenerationID: generation,
	}, project, []accesssnapshot.Grant{{ID: "grant_test", Name: name, Canonical: grant}}, nil)
	require.NoError(t, err)
	return snapshot
}

func TestInstallAuthorizationSnapshotIsIdempotentAndWriteOnce(t *testing.T) {
	ctx := t.Context()
	store, _ := openAccessRepo(t, ctx)
	project := installerGraph(t)
	_, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_test', 'project_test', 'production', 'active')`)
	require.NoError(t, err)
	snapshot := installerSnapshot(t, project, "generation_test", "original")
	install := func(value accesssnapshot.AuthorizationSnapshot) error {
		tx, err := store.SQLDB().BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		if err := InstallAuthorizationSnapshotTx(ctx, tx, value); err != nil {
			return err
		}
		return tx.Commit()
	}
	require.NoError(t, install(snapshot))
	require.NoError(t, install(snapshot))
	otherProject, err := graph.NewProjectGraph([]graph.Resource{
		{ID: "project_test", Kind: graph.KindProject, Name: "test"},
		{ID: "dashboard_main", Kind: graph.KindDashboard, Name: "changed"},
	}, nil)
	require.NoError(t, err)
	other := installerSnapshot(t, otherProject, "generation_test", "changed")
	err = install(other)
	require.ErrorIs(t, err, ErrAuthorizationSnapshotIdentityConflict)

	var count int
	require.NoError(t, store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM authorization_grants`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestInstallAuthorizationSnapshotRejectsTrailingJSON(t *testing.T) {
	project := installerGraph(t)
	if _, err := accesssnapshot.Decode([]byte(`{} {}`), project); err == nil {
		t.Fatal("snapshot decoder accepted trailing JSON")
	}
}

func TestInstallAuthorizationSnapshotRollsBackWithCallerTransaction(t *testing.T) {
	ctx := t.Context()
	store, _ := openAccessRepo(t, ctx)
	project := installerGraph(t)
	_, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_rollback', 'project_test', 'production', 'active')`)
	require.NoError(t, err)
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, InstallAuthorizationSnapshotTx(ctx, tx, installerSnapshot(t, project, "generation_rollback", "rollback")))
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM authorization_snapshots`).Scan(&count))
	require.Zero(t, count)
}

func TestInstallAuthorizationSnapshotConcurrentSameDigest(t *testing.T) {
	ctx := t.Context()
	store, _ := openAccessRepo(t, ctx)
	project := installerGraph(t)
	_, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_concurrent', 'project_test', 'production', 'active')`)
	require.NoError(t, err)
	snapshot := installerSnapshot(t, project, "generation_concurrent", "concurrent")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, beginErr := store.SQLDB().BeginTx(ctx, nil)
			if beginErr != nil {
				errs <- beginErr
				return
			}
			if installErr := InstallAuthorizationSnapshotTx(ctx, tx, snapshot); installErr != nil {
				_ = tx.Rollback()
				errs <- installErr
				return
			}
			errs <- tx.Commit()
		}()
	}
	wg.Wait()
	close(errs)
	for installErr := range errs {
		require.NoError(t, installErr)
	}
}
