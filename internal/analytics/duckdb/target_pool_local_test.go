package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

func TestTargetRuntimePoolFactoryWithLocalConnectionScopesCredential(t *testing.T) {
	session := &recordingTargetSession{}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	binding := testDuckDBTargetBinding(t)
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	var callbackAuth semanticmodel.ConnectionAuth
	callbackCalled := false
	err := factory.WithLocalConnection(t.Context(), binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(connection semanticmodel.Connection) error {
		callbackCalled = true
		callbackAuth = connection.Auth
		require.Equal(t, "identity-secret", connection.Auth["password"])
		require.Equal(t, binding.Endpoint.Host, connection.Host)
		require.False(t, session.closed, "the local pool must remain open through the consumer")
		return nil
	})
	require.NoError(t, err)
	require.True(t, callbackCalled)
	require.Empty(t, callbackAuth, "callback auth must be cleared before WithLocalConnection returns")
	require.True(t, session.closed, "the temporary local pool must close synchronously")
	require.Equal(t, 1, session.closeCalls)

	// The ordinary resolver path remains denied for local pins.
	pool, err := factory.PrepareLocal(t.Context(), binding, snapshot)
	require.NoError(t, err)
	target := pool.(*targetRuntimePool)
	called := false
	err = target.WithConnection(t.Context(), binding.ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	require.False(t, called)
	require.NoError(t, pool.Close())
}

func TestTargetRuntimePoolFactoryRejectsDisabledLocalBindingBeforeOpen(t *testing.T) {
	opened := 0
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) {
		opened++
		return &recordingTargetSession{}, nil
	})
	binding := testDuckDBTargetBinding(t)
	binding.Enabled = false
	binding.Health = connectionbinding.HealthDisabled
	require.NoError(t, binding.Validate(), "fixture must model a valid disabled binding")
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	checks := 0
	ctx := sourcework.WithRevalidator(t.Context(), func(context.Context) error {
		checks++
		return nil
	})
	called := false
	err := factory.WithLocalConnection(ctx, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	require.Zero(t, opened, "disabled bindings must be rejected before opening a local runtime")
	require.False(t, called)
	require.Zero(t, checks, "disabled bindings must fail before reaching source authorization or runtime work")
}

func TestTargetRuntimePoolFactoryWithLocalConnectionRedactsAndCloses(t *testing.T) {
	for _, test := range []struct {
		name        string
		consume     func(semanticmodel.Connection) error
		closeErr    error
		want        error
		wantText    string
		wantCleanup bool
	}{
		{
			name:    "consumer panic",
			consume: func(semanticmodel.Connection) error { panic("private local cleanup detail") },
			want:    analyticsruntime.ErrConnectionCleanupFailed,
		},
		{
			name:    "consumer cancellation",
			consume: func(semanticmodel.Connection) error { return context.Canceled },
			want:    context.Canceled,
		},
		{
			name:        "close failure",
			consume:     func(semanticmodel.Connection) error { return nil },
			closeErr:    errors.New("private target close detail"),
			want:        ErrTargetPoolCleanupFailed,
			wantText:    "private target close detail",
			wantCleanup: true,
		},
		{
			name:        "close failure preserves cancellation",
			consume:     func(semanticmodel.Connection) error { return context.Canceled },
			closeErr:    errors.New("private target close detail"),
			want:        context.Canceled,
			wantText:    "private target close detail",
			wantCleanup: true,
		},
		{
			name:     "ordinary consumer error is redacted",
			consume:  func(semanticmodel.Connection) error { return errors.New("private consumer detail") },
			want:     connectionbinding.ErrProviderUnavailable,
			wantText: "private consumer detail",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &cleanupTrackingTargetSession{closeErr: test.closeErr}
			factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
			binding := testDuckDBTargetBinding(t)
			snapshot := identityTestSnapshot(t, true)
			defer snapshot.Destroy()
			err := factory.WithLocalConnection(t.Context(), binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, test.consume)
			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "private local cleanup detail")
			if test.wantText != "" {
				require.NotContains(t, err.Error(), test.wantText)
			}
			require.Equal(t, 1, session.closeCalls, "cleanup must finish before the scoped call returns")
			if test.wantCleanup {
				require.ErrorIs(t, err, ErrTargetPoolCleanupFailed)
				require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
			}
		})
	}
}

func TestTargetRuntimePoolFactoryWithLocalConnectionPreservesCancellationDuringClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	session := &targetLocalCancelOnCloseSession{cancel: cancel}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	err := factory.WithLocalConnection(ctx, testDuckDBTargetBinding(t), snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, session.closed)
}

func TestTargetRuntimePoolFactoryRedactsPanicsAndQuarantinesLocalScope(t *testing.T) {
	for _, test := range []struct {
		name         string
		panicOn      string
		panicOnClose bool
	}{
		{name: "prepare", panicOn: "CREATE OR REPLACE TEMPORARY SECRET"},
		{name: "health", panicOn: "SELECT 1"},
		{name: "close", panicOnClose: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := &targetLocalPanickingSession{panicOn: test.panicOn, panicOnClose: test.panicOnClose}
			factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
			snapshot := identityTestSnapshot(t, true)
			defer snapshot.Destroy()
			err := factory.WithLocalConnection(t.Context(), testDuckDBTargetBinding(t), snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
				return nil
			})
			require.ErrorIs(t, err, ErrTargetPoolCleanupFailed)
			require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
			require.NotContains(t, err.Error(), "private native panic detail")
			require.True(t, session.closed)
		})
	}
}

func TestTargetRuntimePoolFactoryWithLocalConnectionClosesAfterCancellation(t *testing.T) {
	session := &recordingTargetSession{}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	binding := testDuckDBTargetBinding(t)
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	ctx, cancel := context.WithCancel(t.Context())
	err := factory.WithLocalConnection(ctx, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		cancel()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, session.closed, "cleanup must use a synchronous context-independent close after cancellation")
	require.Equal(t, 1, session.closeCalls)
}

func TestTargetRuntimePoolFactoryRevalidatesAfterLocalHealthCheck(t *testing.T) {
	var revoked atomic.Bool
	session := &targetLocalRevokingSession{revoked: &revoked}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	binding := testDuckDBTargetBinding(t)
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	checks := 0
	denied := errors.New("private revoked grant detail")
	ctx := sourcework.WithRevalidator(t.Context(), func(context.Context) error {
		checks++
		if revoked.Load() {
			return denied
		}
		return nil
	})
	called := false
	err := factory.WithLocalConnection(ctx, binding, snapshot, semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, denied)
	require.NotContains(t, err.Error(), "private revoked grant detail")
	require.False(t, called, "revocation during health must deny before source consumption")
	require.Equal(t, 3, checks, "revalidate before prepare, before health, and before consumer")
	require.Equal(t, 1, session.closeCalls)
}

type targetLocalRevokingSession struct {
	recordingTargetSession
	revoked *atomic.Bool
}

func (session *targetLocalRevokingSession) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	result, err := session.recordingTargetSession.ExecContext(ctx, statement, args...)
	if strings.TrimSpace(statement) == "SELECT 1" && session.revoked != nil {
		session.revoked.Store(true)
	}
	return result, err
}

type targetLocalCancelOnCloseSession struct {
	recordingTargetSession
	cancel context.CancelFunc
}

func (session *targetLocalCancelOnCloseSession) Close() error {
	session.closeCalls++
	session.closed = true
	if session.cancel != nil {
		session.cancel()
	}
	return nil
}

type targetLocalPanickingSession struct {
	panicOn      string
	panicOnClose bool
	closed       bool
}

func (session *targetLocalPanickingSession) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	if session.panicOn != "" && strings.Contains(statement, session.panicOn) {
		panic("private native panic detail")
	}
	return nil, nil
}

func (session *targetLocalPanickingSession) Close() error {
	session.closed = true
	if session.panicOnClose {
		panic("private native panic detail")
	}
	return nil
}

func TestTargetRuntimePoolLocalProjectionRequiresExactCredentialIdentity(t *testing.T) {
	session := &recordingTargetSession{}
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
	snapshot := identityTestSnapshot(t, true)
	defer snapshot.Destroy()
	pool, err := factory.PrepareLocal(t.Context(), testDuckDBTargetBinding(t), snapshot)
	require.NoError(t, err)
	target := pool.(*targetRuntimePool)
	called := false
	err = target.withLocalConnection(t.Context(), testDuckDBTargetBinding(t).ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, snapshot.Identity(), func(semanticmodel.Connection) error {
		called = true
		return nil
	})
	require.NoError(t, err)
	require.True(t, called)
	err = target.withLocalConnection(t.Context(), testDuckDBTargetBinding(t).ConnectionID.String(), semanticmodel.Connection{Kind: "postgres"}, connectionbinding.CredentialIdentity{CredentialVersionID: "c9f9b69c-4706-49dc-a6bf-28096d0df596"}, func(semanticmodel.Connection) error {
		t.Fatal("mismatched identity must not invoke the local consumer")
		return nil
	})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	require.NoError(t, pool.Close())
}
