package deploymentpostgres

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/stretchr/testify/require"
)

// This joins production completion coordination, PostgreSQL publication and
// runtime-host cutover. The prepared job/result and physical runtime factory
// remain explicit seams; it does not execute a source read or attach DuckLake.
func TestCredentialRefreshRuntimeCommitRollbackAndReplay(t *testing.T) {
	f := newCredentialRefreshFixture(t, nil)
	host, factory, _ := f.runtimeHost(t)
	coordinate := f.runtimeCoordinator(t, host)
	base := f.job.Identity.GenerationID
	assertCredentialRefreshRuntime(t, host, base)
	oldLease, err := host.Acquire(t.Context())
	require.NoError(t, err)
	t.Cleanup(oldLease.Release)
	oldRuntime := oldLease.Runtime().(*credentialRefreshRuntime)
	publication := f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)

	prepareFailure := errors.New("injected sealed runtime preparation failure")
	factory.fail = prepareFailure
	called := false
	err = coordinate(t.Context(), f.job, f.result, func() error {
		called = true
		return publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result)
	})
	require.ErrorIs(t, err, prepareFailure)
	require.False(t, called, "preparation must precede durable completion")
	f.assertOutcome(t, false)
	assertCredentialRefreshRuntime(t, host, base)
	factory.fail = nil

	completionFailure := errors.New("injected failure after transactional job completion")
	queue := &credentialRefreshLateFailure{PostgresJobsAdapter: f.queue, err: completionFailure}
	failed := f.persistence(t, queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	err = coordinate(t.Context(), f.job, f.result, func() error {
		assertCredentialRefreshRuntime(t, host, base)
		return failed.CompleteCanonicalRefresh(t.Context(), f.job, f.result)
	})
	require.ErrorIs(t, err, completionFailure)
	require.True(t, queue.called)
	f.assertOutcome(t, false)
	assertCredentialRefreshRuntime(t, host, base)
	require.Equal(t, f.input.Generation.CandidateID, factory.last.activationCandidateID)
	require.True(t, factory.last.closed.Load(), "failed successor must be discarded")
	require.False(t, oldRuntime.closed.Load())

	err = coordinate(t.Context(), f.job, f.result, func() error {
		require.Equal(t, f.input.Generation.CandidateID, factory.last.activationCandidateID)
		assertCredentialRefreshRuntime(t, host, base)
		if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result); err != nil {
			return err
		}
		f.assertOutcome(t, true)
		// A committed database record alone has not switched process readers.
		assertCredentialRefreshRuntime(t, host, base)
		return nil
	})
	require.NoError(t, err)
	assertCredentialRefreshRuntime(t, host, f.result.ServingStateID)
	f.assertOutcome(t, true)
	require.Equal(t, f.job.Identity, oldLease.Identity())
	require.False(t, oldRuntime.closed.Load(), "old readers must survive cutover")
	oldLease.Release()
	require.Eventually(t, oldRuntime.closed.Load, time.Second, time.Millisecond, "old runtime must close after its final reader releases")

	publication = f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
	require.NoError(t, coordinate(t.Context(), f.job, f.result, func() error {
		require.Equal(t, f.input.Generation.CandidateID, factory.last.activationCandidateID)
		return publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result)
	}))
	f.assertOutcome(t, true)
	assertCredentialRefreshRuntime(t, host, f.result.ServingStateID)
	active, err := host.Acquire(t.Context())
	require.NoError(t, err)
	t.Cleanup(active.Release)
	altered := f.result
	altered.SnapshotID++
	require.Error(t, coordinate(t.Context(), f.job, altered, func() error {
		return publication.CompleteCanonicalRefresh(t.Context(), f.job, altered)
	}))
	f.assertOutcome(t, true)
	assertCredentialRefreshRuntime(t, host, f.result.ServingStateID)
	require.True(t, factory.last.closed.Load())
	require.False(t, active.Runtime().(*credentialRefreshRuntime).closed.Load())
}

func TestCredentialRefreshRuntimeRecoversLostCommitAcknowledgement(t *testing.T) {
	for _, recovery := range []string{"reconcile", "restart"} {
		t.Run(recovery, func(t *testing.T) {
			f := newCredentialRefreshFixture(t, nil)
			host, factory, config := f.runtimeHost(t)
			coordinate := f.runtimeCoordinator(t, host)
			publication := f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
			lostAcknowledgement := errors.New("injected lost commit acknowledgement")
			err := coordinate(t.Context(), f.job, f.result, func() error {
				if err := publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result); err != nil {
					return err
				}
				return lostAcknowledgement
			})
			require.ErrorIs(t, err, lostAcknowledgement)
			f.assertOutcome(t, true)
			assertCredentialRefreshRuntime(t, host, f.job.Identity.GenerationID)
			require.True(t, factory.last.closed.Load(), "uncertain successor is not locally activated")
			if recovery == "restart" {
				require.NoError(t, host.Close())
				host, err = runtimehostmodule.Build(t.Context(), config)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, host.Close()) })
			} else {
				// Use the same durable pointer resolver as startup/background repair.
				id, err := config.ResolveSealedActiveState(t.Context())
				require.NoError(t, err)
				require.NoError(t, host.ReconcileSealed(t.Context(), id))
			}
			assertCredentialRefreshRuntime(t, host, f.result.ServingStateID)
			f.assertOutcome(t, true)
			// Worker replay after recovery must not publish a second time.
			coordinate = f.runtimeCoordinator(t, host)
			publication = f.persistence(t, f.queue).Publication.(refreshrun.CanonicalPublicationUnitOfWork)
			require.NoError(t, coordinate(t.Context(), f.job, f.result, func() error {
				return publication.CompleteCanonicalRefresh(t.Context(), f.job, f.result)
			}))
			f.assertOutcome(t, true)
			assertCredentialRefreshRuntime(t, host, f.result.ServingStateID)
		})
	}
}

func (f credentialRefreshFixture) runtimeHost(t *testing.T) (*runtimehostmodule.Module, *credentialRefreshRuntimeFactory, runtimehostmodule.Config) {
	t.Helper()
	factory := &credentialRefreshRuntimeFactory{candidateID: f.input.Generation.CandidateID, successorID: f.result.ServingStateID}
	config := runtimehostmodule.Config{
		States: servingnative.New(f.db), ProjectID: f.job.Identity.ProjectID, Environment: servingstate.Environment(f.job.Identity.Environment),
		Factory: factory, Authorization: factory, RequireSealedCatalog: true, ActiveReconcileInterval: time.Hour,
		ResolveSealedActiveState: func(ctx context.Context) (servingstate.ID, error) {
			target, err := f.delivery.Target(ctx, admissionInstanceID)
			return servingstate.ID(target.ActiveGenerationID), err
		},
	}
	host, err := runtimehostmodule.Build(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, host.Close()) })
	return host, factory, config
}

func (f credentialRefreshFixture) runtimeCoordinator(t *testing.T, host *runtimehostmodule.Module) refreshrun.CanonicalCompletionCoordinator {
	t.Helper()
	coordinate, err := apprefreshpostgres.NewNativeCanonicalCompletionCoordinator(admissionInstanceID, f.delivery,
		func(_ context.Context, candidate deployment.Deployment) error {
			expected := f.job.Identity
			expected.GenerationID = f.result.ServingStateID
			if candidate.ServingIdentity != expected {
				return errors.New("publication ownership received a different serving identity")
			}
			return nil
		}, host)
	require.NoError(t, err)
	return coordinate
}

func assertCredentialRefreshRuntime(t *testing.T, host *runtimehostmodule.Module, generation string) {
	t.Helper()
	lease, err := host.Acquire(t.Context())
	require.NoError(t, err)
	defer lease.Release()
	require.Equal(t, generation, lease.Identity().GenerationID)
}

// Physical preparation and ownership policy are deterministic seams. Runtime
// preparation still receives the real persisted serving bundle and candidate;
// the real host checks authorization identity and owns reader lifetimes.
type credentialRefreshRuntimeFactory struct {
	candidateID string
	successorID string
	fail        error
	last        *credentialRefreshRuntime
}

func (*credentialRefreshRuntimeFactory) Prepare(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	return nil, errors.New("refresh completion must prepare a sealed runtime")
}

func (*credentialRefreshRuntimeFactory) PinnedSnapshotSealed() {}

func (f *credentialRefreshRuntimeFactory) PrepareSealed(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	if input.Candidate != nil {
		return nil, errors.New("refresh completion must not prepare a private candidate")
	}
	if candidate := input.SealedActivationCandidate; candidate != nil && (candidate.CandidateID != f.candidateID || string(input.State.ID) != f.successorID) {
		return nil, errors.New("refresh completion supplied different sealed candidate evidence")
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
	f.last = &credentialRefreshRuntime{authorization: authorization}
	if input.SealedActivationCandidate != nil {
		f.last.activationCandidateID = input.SealedActivationCandidate.CandidateID
	}
	return f.last, nil
}

func (*credentialRefreshRuntimeFactory) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

type credentialRefreshRuntime struct {
	authorization         accesssnapshot.AuthorizationSnapshot
	activationCandidateID string
	closed                atomic.Bool
}

func (r *credentialRefreshRuntime) Close() error { r.closed.Store(true); return nil }
func (r *credentialRefreshRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}
