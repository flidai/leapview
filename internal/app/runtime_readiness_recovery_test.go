package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestRuntimeReadinessRecoversAfterCommittedGenerationCutover(t *testing.T) {
	repo := &readinessRecoveryRepository{active: "generation-1"}
	factory := &readinessRecoveryFactory{}
	config := runtimehostmodule.Config{
		States: repo, ProjectID: "project:readiness", Environment: "prod",
		Factory: factory, Authorization: factory, RequireSealedCatalog: true,
		ActiveReconcileInterval:  time.Hour,
		ResolveSealedActiveState: func(context.Context) (servingstate.ID, error) { return repo.active, nil },
	}
	host, err := runtimehostmodule.Build(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	assertReady := func(host *runtimehostmodule.Module, status int) {
		t.Helper()
		health := newHealth(healthConfig{
			Platform:                func(context.Context) error { return nil },
			ActiveProjectID:         func(context.Context) (projectgraph.ResourceID, error) { return host.ProjectID(), nil },
			RuntimeReady:            func(ctx context.Context) error { return checkActiveRuntimeIdentity(ctx, host) },
			RequireActiveDeployment: true,
		})
		response := httptest.NewRecorder()
		health.Readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		require.Equal(t, status, response.Code, response.Body.String())
		if status == http.StatusServiceUnavailable {
			require.JSONEq(t, `{"checks":{"platformStore":"ok","runtime":"failed"},"status":"not_ready"}`, response.Body.String())
		}
	}
	assertReady(host, http.StatusOK)
	oldLease, err := host.Acquire(t.Context())
	require.NoError(t, err)
	t.Cleanup(oldLease.Release)

	// Durable publication can precede local cutover, for example after a lost
	// notification or acknowledgement. Readiness must observe that mismatch.
	repo.active = "generation-2"
	assertReady(host, http.StatusServiceUnavailable)
	factory.fail = true
	require.Error(t, host.ReconcileSealed(t.Context(), repo.active))
	assertReady(host, http.StatusServiceUnavailable)
	factory.fail = false
	require.NoError(t, host.ReconcileSealed(t.Context(), repo.active))
	assertReady(host, http.StatusOK)
	// Convergence of new readers is separate from draining old readers.
	require.Equal(t, "generation-1", oldLease.Identity().GenerationID)
	oldLease.Release()
	// Stop after another durable commit but before its local cutover.
	repo.active = "generation-3"
	assertReady(host, http.StatusServiceUnavailable)
	require.NoError(t, host.Close())

	// Restart follows the same durable pointer without replaying publication.
	restarted, err := runtimehostmodule.Build(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	assertReady(restarted, http.StatusOK)
	lease, err := restarted.Acquire(t.Context())
	require.NoError(t, err)
	defer lease.Release()
	require.Equal(t, "generation-3", lease.Identity().GenerationID)
}

type readinessRecoveryRepository struct{ active servingstate.ID }

func (r *readinessRecoveryRepository) ActiveArtifact(ctx context.Context, _ projectgraph.ResourceID, _ servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	state, err := r.ByID(ctx, r.active)
	artifact, _ := r.ArtifactByServingState(ctx, r.active)
	return state, artifact, err
}

func (*readinessRecoveryRepository) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	return servingstate.State{ID: id, ProjectID: "project:readiness", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (r *readinessRecoveryRepository) ArtifactByServingState(ctx context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	state, _ := r.ByID(ctx, id)
	return servingstate.Artifact{ID: "artifact-" + string(id), ServingStateID: id, Digest: state.Digest}, nil
}

func (*readinessRecoveryRepository) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}

type readinessRecoveryFactory struct{ fail bool }

func (*readinessRecoveryFactory) Prepare(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	return nil, errors.New("readiness recovery must use sealed preparation")
}

func (f *readinessRecoveryFactory) PrepareSealed(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	if f.fail {
		return nil, errors.New("temporary sealed preparation failure")
	}
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(input.State.Environment), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	graph, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		return nil, err
	}
	return readinessRecoveryRuntime{authorization: authorization}, nil
}

func (*readinessRecoveryFactory) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

type readinessRecoveryRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
}

func (readinessRecoveryRuntime) Close() error { return nil }
func (r readinessRecoveryRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}
