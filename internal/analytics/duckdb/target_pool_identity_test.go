package duckdb

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/stretchr/testify/require"
)

func TestTargetRuntimePoolRetainsExactCredentialIdentityThroughClose(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "provider"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			session := &recordingTargetSession{}
			factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return session, nil })
			snapshot := identityTestSnapshot(t, local)
			want := snapshot.Identity()
			binding := testDuckDBTargetBinding(t)
			prepare := factory.Prepare
			if local {
				prepare = factory.PrepareLocal
			}
			pool, err := prepare(t.Context(), binding, snapshot)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pool.Close()) })
			identified := pool.(interface {
				CredentialIdentity() connectionbinding.CredentialIdentity
			})
			snapshot.Destroy()
			require.Equal(t, want, identified.CredentialIdentity())
			require.NoError(t, pool.HealthCheck(t.Context()))
			resolver := pool.(analyticsruntime.ConnectionResolver)
			called := false
			var resolved semanticmodel.Connection
			err = resolver.WithConnection(t.Context(), "warehouse", semanticmodel.Connection{Kind: "postgres"}, func(connection semanticmodel.Connection) error {
				called = true
				resolved = connection
				if !local {
					require.Equal(t, "identity-secret", connection.Auth["password"])
				}
				return nil
			})
			if local {
				require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
				require.False(t, called)
				require.Empty(t, resolved.Auth)
			} else {
				require.NoError(t, err)
				require.True(t, called)
				require.Empty(t, resolved.Auth, "callback-owned auth must be cleared when WithConnection returns")
			}
			require.Contains(t, strings.Join(session.statements, "\n"), "READ_ONLY")
			require.NoError(t, pool.Close())
			require.True(t, session.closed)
			require.Equal(t, want, identified.CredentialIdentity())
			called = false
			err = resolver.WithConnection(t.Context(), "warehouse", semanticmodel.Connection{Kind: "postgres"}, func(semanticmodel.Connection) error {
				called = true
				return nil
			})
			require.Error(t, err)
			require.False(t, called)
		})
	}
}

func TestTargetRuntimePoolRejectsWrongCredentialOriginBeforeOpeningSession(t *testing.T) {
	opened := 0
	factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) {
		opened++
		return &recordingTargetSession{}, nil
	})
	local := identityTestSnapshot(t, true)
	defer local.Destroy()
	external := identityTestSnapshot(t, false)
	defer external.Destroy()
	binding := testDuckDBTargetBinding(t)
	_, err := factory.Prepare(t.Context(), binding, local)
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	_, err = ApplyTargetBinding(semanticmodel.Connection{Kind: "postgres"}, binding, local)
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	_, err = factory.PrepareLocal(t.Context(), binding, external)
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	_, err = factory.PrepareLocal(t.Context(), testQuackTargetBinding(t), local)
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	_, err = factory.PrepareLocal(t.Context(), binding, connectionbinding.CredentialSnapshot{})
	require.ErrorIs(t, err, connectionbinding.ErrIncompatibleBinding)
	require.Zero(t, opened)
}

func identityTestFactory(t *testing.T, open TargetRuntimeSessionOpener) *TargetRuntimePoolFactory {
	t.Helper()
	factory, err := NewTargetRuntimePoolFactory(TargetRuntimePoolFactoryConfig{
		Open: open, Limits: TargetRuntimeLimits{MemoryMaxBytes: 64 << 20, TempMaxBytes: 16 << 20, MaxThreads: 1},
		RequireTLS: true, ExtensionAdmission: targetPoolTestExtensionAdmission{},
	})
	require.NoError(t, err)
	return factory
}

func identityTestSnapshot(t *testing.T, local bool) connectionbinding.CredentialSnapshot {
	t.Helper()
	constructor := connectionbinding.NewCredentialSnapshot
	version := "provider:v3"
	if local {
		constructor = connectionbinding.NewLocalCredentialSnapshot
		version = "93d623e3-8d75-41ea-b006-aed3375d0592"
	}
	snapshot, err := constructor(map[string]string{"password": "identity-secret"}, version, time.Now(), time.Time{})
	require.NoError(t, err)
	return snapshot
}
