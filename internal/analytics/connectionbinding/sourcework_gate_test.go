package connectionbinding

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

func TestPoolManagerRefreshWaitsAtSourceWorkGateForEveryOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		now := binding.UpdatedAt
		resolver := &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "source-v1", now)}}
		gate := &sourcework.Gate{}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding, Resolver: resolver, Factory: &recordingPoolFactory{},
			Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
			Now: func() time.Time { return now }, StaleAfter: time.Hour, SourceWork: gate,
		})
		require.NoError(t, err)

		requests := []RefreshRequest{
			{Actor: "runtime:target-1", Operation: RefreshRuntime},
			{Actor: "principal:operator-1", Operation: RefreshRequested},
			{Actor: "runtime:target-1", Operation: RefreshScheduled},
		}
		for _, request := range requests {
			pause, err := gate.Pause()
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- manager.Refresh(ctx, request) }()
			synctest.Wait()
			select {
			case err := <-result:
				t.Fatalf("%s refresh passed a paused source-work gate: %v", request.Operation, err)
			default:
			}
			cancel()
			require.ErrorIs(t, <-result, context.Canceled)
			require.NoError(t, pause.WaitDrained(context.Background()))
			require.NoError(t, pause.Resume())
		}

		resolver.mu.Lock()
		calls := resolver.calls
		resolver.mu.Unlock()
		require.Zero(t, calls, "paused refresh reached credential resolution")
	})
}

func TestPoolManagerRetirementCancelsRefreshWaitingAtSourceWorkGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		now := binding.UpdatedAt
		gate := &sourcework.Gate{}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding,
			Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
				testSnapshot(t, "source-v1", now),
			}},
			Factory: &recordingPoolFactory{}, Store: &recordingBindingStore{},
			Audit: noOpRotationAudit{}, Now: func() time.Time { return now },
			StaleAfter: time.Hour, SourceWork: gate,
		})
		require.NoError(t, err)
		pause, err := gate.Pause()
		require.NoError(t, err)

		refreshDone := make(chan error, 1)
		go func() {
			refreshDone <- manager.Refresh(context.Background(), RefreshRequest{
				Actor: "runtime:target-1", Operation: RefreshRuntime,
			})
		}()
		synctest.Wait()
		retirementDone := make(chan error, 1)
		go func() {
			retirementDone <- manager.RetireBounded(context.Background(), time.Now().Add(time.Second))
		}()
		require.ErrorIs(t, <-refreshDone, context.Canceled)
		require.NoError(t, <-retirementDone)
		require.NoError(t, pause.WaitDrained(context.Background()))
		require.NoError(t, pause.Resume())
	})
}

func TestPoolManagerSuccessfulRefreshReleasesSourceWorkLease(t *testing.T) {
	binding := validTargetBinding(t)
	now := binding.UpdatedAt
	gate := &sourcework.Gate{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: binding,
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
			testSnapshot(t, "source-v1", now),
		}},
		Factory: &recordingPoolFactory{}, Store: &recordingBindingStore{},
		Audit: noOpRotationAudit{}, Now: func() time.Time { return now },
		StaleAfter: time.Hour, SourceWork: gate,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}))

	pause, err := gate.Pause()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, pause.WaitDrained(ctx), "successful refresh left a source-work lease active")
	require.NoError(t, pause.Resume())
}

func TestPoolManagerSourceWorkDrainWaitsForTimedOutRejectedPoolCleanup(t *testing.T) {
	synctest.Test(t, testPoolManagerSourceWorkDrainWaitsForTimedOutRejectedPoolCleanup)
}

func testPoolManagerSourceWorkDrainWaitsForTimedOutRejectedPoolCleanup(t *testing.T) {
	binding := validTargetBinding(t)
	now := binding.UpdatedAt
	closeErr := errors.New("runtime close failed")
	pool := newRetirementBlockingClosePool(closeErr)
	t.Cleanup(pool.allowClose)
	pool.healthErr = errors.New("candidate health check failed")
	gate := &sourcework.Gate{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: binding,
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
			testSnapshot(t, "source-v1", now),
		}},
		Factory: retirementFixedPoolFactory{pool: pool}, Store: &recordingBindingStore{},
		Audit: noOpRotationAudit{}, Now: func() time.Time { return now },
		StaleAfter: time.Hour, SourceWork: gate,
	})
	require.NoError(t, err)
	refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancelRefresh()
	err = manager.Refresh(refreshCtx, RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-pool.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start rejected candidate cleanup")
	}

	pause, err := gate.Pause()
	require.NoError(t, err)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelDrain()
	require.ErrorIs(t, pause.WaitDrained(drainCtx), context.DeadlineExceeded,
		"source-work drain completed while candidate Close was still running")
	err = manager.RetireBounded(context.Background(), time.Now().Add(40*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"manager retirement completed while candidate Close was still running")

	pool.allowClose()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	require.NoError(t, pause.WaitDrained(waitCtx))
	require.ErrorIs(t, manager.RetireBounded(context.Background(), time.Now().Add(time.Second)), closeErr)
	require.NoError(t, pause.Resume())
}
