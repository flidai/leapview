package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestCheckActiveRuntimeIdentityRequiresTheDurableGeneration(t *testing.T) {
	durable := servingstate.State{ID: "generation-2", ProjectID: "project:test", Environment: "prod"}
	for _, test := range []struct {
		name     string
		identity graph.ServingIdentity
		wantErr  bool
	}{
		{
			name:     "matching generation",
			identity: graph.ServingIdentity{ProjectID: durable.ProjectID, Environment: string(durable.Environment), GenerationID: string(durable.ID)},
		},
		{
			name:     "stale generation",
			identity: graph.ServingIdentity{ProjectID: durable.ProjectID, Environment: string(durable.Environment), GenerationID: "generation-1"},
			wantErr:  true,
		},
		{
			name:     "wrong project",
			identity: graph.ServingIdentity{ProjectID: "project:other", Environment: string(durable.Environment), GenerationID: string(durable.ID)},
			wantErr:  true,
		},
		{
			name:     "wrong environment",
			identity: graph.ServingIdentity{ProjectID: durable.ProjectID, Environment: "staging", GenerationID: string(durable.ID)},
			wantErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease := &runtimeReadinessLeaseStub{identity: test.identity}
			source := &runtimeReadinessSourceStub{state: durable, lease: lease}
			err := checkActiveRuntimeIdentity(t.Context(), source)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, source.acquireCalls)
			require.Equal(t, 1, lease.releaseCalls, "every acquired readiness lease must be released")
		})
	}
}

func TestCheckActiveRuntimeIdentityPreservesMissingAndReadFailures(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		require.Error(t, checkActiveRuntimeIdentity(t.Context(), nil))
	})

	t.Run("no active deployment", func(t *testing.T) {
		source := &runtimeReadinessSourceStub{activeErr: servingstate.ErrNotFound}
		err := checkActiveRuntimeIdentity(t.Context(), source)
		require.ErrorIs(t, err, errNoActiveDeployment)
		require.Zero(t, source.acquireCalls)
	})

	t.Run("active read failure", func(t *testing.T) {
		wantErr := errors.New("active state unavailable")
		source := &runtimeReadinessSourceStub{activeErr: wantErr}
		err := checkActiveRuntimeIdentity(t.Context(), source)
		require.ErrorIs(t, err, wantErr)
		require.Zero(t, source.acquireCalls)
	})

	t.Run("invalid active identity", func(t *testing.T) {
		source := &runtimeReadinessSourceStub{state: servingstate.State{ID: " generation-2", ProjectID: "project:test", Environment: "prod"}}
		err := checkActiveRuntimeIdentity(t.Context(), source)
		require.Error(t, err)
		require.Zero(t, source.acquireCalls)
	})

	t.Run("lease acquisition failure", func(t *testing.T) {
		wantErr := errors.New("runtime lease unavailable")
		source := &runtimeReadinessSourceStub{state: servingstate.State{ID: "generation-2", ProjectID: "project:test", Environment: "prod"}, acquireErr: wantErr}
		err := checkActiveRuntimeIdentity(t.Context(), source)
		require.ErrorIs(t, err, wantErr)
		require.Equal(t, 1, source.acquireCalls)
	})

	t.Run("nil lease", func(t *testing.T) {
		source := &runtimeReadinessSourceStub{state: servingstate.State{ID: "generation-2", ProjectID: "project:test", Environment: "prod"}}
		require.Error(t, checkActiveRuntimeIdentity(t.Context(), source))
		require.Equal(t, 1, source.acquireCalls)
	})
}

type runtimeReadinessSourceStub struct {
	state        servingstate.State
	activeErr    error
	lease        runtimehost.Lease
	acquireErr   error
	acquireCalls int
}

func (stub *runtimeReadinessSourceStub) ActiveArtifact(context.Context) (servingstate.State, servingstate.Artifact, error) {
	return stub.state, servingstate.Artifact{}, stub.activeErr
}

func (stub *runtimeReadinessSourceStub) Acquire(context.Context) (runtimehost.Lease, error) {
	stub.acquireCalls++
	return stub.lease, stub.acquireErr
}

type runtimeReadinessLeaseStub struct {
	identity     graph.ServingIdentity
	releaseCalls int
}

func (*runtimeReadinessLeaseStub) Runtime() runtimehost.Runtime         { return nil }
func (stub *runtimeReadinessLeaseStub) Identity() graph.ServingIdentity { return stub.identity }
func (stub *runtimeReadinessLeaseStub) Release()                        { stub.releaseCalls++ }
