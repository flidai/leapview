package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/stretchr/testify/require"
)

func TestTargetPoolCloseTimeoutRetainsActualCompletion(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("session close failed")} {
		name := "success"
		if closeErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			session := newControlledCloseSession(closeErr)
			defer session.finishClose()
			pool := &targetRuntimePool{session: session}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.ErrorIs(t, pool.CloseContext(ctx), context.Canceled)
			awaitTargetCloseSignal(t, session.closeStarted)
			// A second waiter must still see a pending close, not an empty pool.
			require.ErrorIs(t, pool.CloseContext(ctx), context.Canceled)
			require.ErrorIs(t, pool.HealthCheck(context.Background()), connectionbinding.ErrProviderUnavailable)

			const callers = 8
			results := make(chan error, callers)
			for range callers {
				go func() { results <- pool.Close() }()
			}
			select {
			case result := <-results:
				t.Fatalf("close returned before session cleanup: %v", result)
			case <-time.After(20 * time.Millisecond):
			}
			session.finishClose()
			for range callers {
				select {
				case result := <-results:
					require.ErrorIs(t, result, closeErr)
				case <-time.After(time.Second):
					t.Fatal("close did not observe completed session cleanup")
				}
			}
			require.ErrorIs(t, pool.Close(), closeErr)
			require.Equal(t, int32(1), session.closeCalls.Load())
		})
	}
}

func TestTargetPoolCloseWaitsForCanceledHealthToExit(t *testing.T) {
	session := newControlledCloseSession(nil)
	session.healthStarted = make(chan struct{})
	session.healthCanceled = make(chan struct{})
	session.healthRelease = make(chan struct{})
	defer session.finishClose()
	var releaseHealth sync.Once
	finishHealth := func() { releaseHealth.Do(func() { close(session.healthRelease) }) }
	defer finishHealth()
	pool := &targetRuntimePool{session: session, healthStatement: "SELECT 1"}
	healthResult := make(chan error, 1)
	go func() { healthResult <- pool.HealthCheck(context.Background()) }()
	awaitTargetCloseSignal(t, session.healthStarted)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, pool.CloseContext(ctx), context.Canceled)
	awaitTargetCloseSignal(t, session.healthCanceled)
	awaitTargetCloseSignal(t, session.closeStarted)
	session.finishClose()
	awaitTargetCloseSignal(t, session.closeReturned)
	// The driver close returned, but the canceled operation is still running.
	// Cancellation must not remove it from the completion accounting.
	require.ErrorIs(t, pool.CloseContext(ctx), context.Canceled)
	finishHealth()
	select {
	case err := <-healthResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("health operation did not exit")
	}
	bounded, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	require.NoError(t, pool.CloseContext(bounded))
	require.Equal(t, int32(1), session.closeCalls.Load())
}

func TestTargetPoolCloseWaitsForConnectionConsumerAndClearsItsAuth(t *testing.T) {
	session := &recordingTargetSession{}
	pool := &targetRuntimePool{session: session, connection: targetPoolCallbackConnection()}
	consumerStarted := make(chan struct{})
	releaseConsumer := make(chan struct{})
	consumerDone := make(chan error, 1)
	var observed semanticmodel.Connection
	callbackErr := errors.New("consumer failed after cleanup")
	go func() {
		consumerDone <- pool.WithConnection(
			context.Background(), "warehouse", semanticmodel.Connection{Kind: "postgres"},
			func(connection semanticmodel.Connection) error {
				observed = connection
				close(consumerStarted)
				<-releaseConsumer
				return callbackErr
			},
		)
	}()
	awaitTargetCloseSignal(t, consumerStarted)

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelClose()
	closeResult := make(chan error, 1)
	go func() { closeResult <- pool.CloseContext(closeCtx) }()
	select {
	case err := <-closeResult:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("CloseContext did not return its deadline while the callback was active")
	}
	require.Equal(t, "callback-secret", observed.Auth["password"], "the callback clone must live until it returns")

	close(releaseConsumer)
	require.ErrorIs(t, <-consumerDone, callbackErr)
	require.Empty(t, observed.Auth, "callback auth must be cleared before its active-work lease is released")
	bounded, cancelBounded := context.WithTimeout(context.Background(), time.Second)
	defer cancelBounded()
	require.NoError(t, pool.CloseContext(bounded))
	require.True(t, session.closed)
}

func TestTargetPoolCleanupFailurePoisonsPoolAndCloseReturnsSentinel(t *testing.T) {
	for _, panics := range []bool{false, true} {
		name := "consumer sentinel"
		if panics {
			name = "consumer panic"
		}
		t.Run(name, func(t *testing.T) {
			nativeCloseErr := errors.New("driver close secret diagnostics")
			session := &cleanupTrackingTargetSession{closeErr: nativeCloseErr}
			pool := &targetRuntimePool{session: session, connection: targetPoolCallbackConnection()}
			var observed semanticmodel.Connection
			err := pool.WithConnection(
				context.Background(), "warehouse", semanticmodel.Connection{Kind: "postgres"},
				func(connection semanticmodel.Connection) error {
					observed = connection
					if panics {
						panic(errors.New("driver secret diagnostics"))
					}
					return analyticsruntime.ErrConnectionCleanupFailed
				},
			)
			require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
			require.NotContains(t, err.Error(), "driver secret diagnostics")
			require.Empty(t, observed.Auth)
			called := false
			require.ErrorIs(t, pool.WithConnection(
				context.Background(), "warehouse", semanticmodel.Connection{Kind: "postgres"},
				func(semanticmodel.Connection) error { called = true; return nil },
			), analyticsruntime.ErrConnectionCleanupFailed)
			require.False(t, called, "poisoned pool must reject new callbacks")
			require.ErrorIs(t, pool.HealthCheck(context.Background()), analyticsruntime.ErrConnectionCleanupFailed)
			closeErr := pool.Close()
			require.ErrorIs(t, closeErr, analyticsruntime.ErrConnectionCleanupFailed)
			require.NotContains(t, closeErr.Error(), nativeCloseErr.Error())
			require.Equal(t, 1, session.closeCalls)
		})
	}
}

func targetPoolCallbackConnection() semanticmodel.Connection {
	return semanticmodel.Connection{
		Kind: "postgres", Host: "warehouse.internal", Port: 5432,
		Database: "analytics", Username: "reader", SSLMode: "verify-full",
		Auth: semanticmodel.ConnectionAuth{"password": "callback-secret"},
	}
}

type controlledCloseSession struct {
	closeStarted   chan struct{}
	closeRelease   chan struct{}
	closeReturned  chan struct{}
	closeOnce      sync.Once
	closeCalls     atomic.Int32
	closeErr       error
	healthStarted  chan struct{}
	healthCanceled chan struct{}
	healthRelease  chan struct{}
}

func newControlledCloseSession(err error) *controlledCloseSession {
	return &controlledCloseSession{
		closeStarted: make(chan struct{}), closeRelease: make(chan struct{}),
		closeReturned: make(chan struct{}), closeErr: err,
	}
}

func (s *controlledCloseSession) finishClose() {
	s.closeOnce.Do(func() { close(s.closeRelease) })
}

func (s *controlledCloseSession) Close() error {
	s.closeCalls.Add(1)
	close(s.closeStarted)
	<-s.closeRelease
	close(s.closeReturned)
	return s.closeErr
}

func (s *controlledCloseSession) ExecContext(ctx context.Context, _ string, _ ...any) (sql.Result, error) {
	close(s.healthStarted)
	<-ctx.Done()
	close(s.healthCanceled)
	<-s.healthRelease
	return nil, ctx.Err()
}

func awaitTargetCloseSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("target pool lifecycle signal did not arrive")
	}
}
