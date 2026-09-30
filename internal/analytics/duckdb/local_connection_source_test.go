package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

func TestLocalConnectionScopeContainsSourcePreparationAndCleanup(t *testing.T) {
	for _, failure := range []string{"", "source cleanup", "target close"} {
		t.Run("failure="+failure, func(t *testing.T) {
			db, err := sql.Open("duckdb", ":memory:")
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			session := &localConnectionSourceSession{
				sourceAuthLifetimeSession: &sourceAuthLifetimeSession{Session: conn, conn: conn},
				failCleanup:               failure == "source cleanup",
			}
			t.Cleanup(func() { _ = session.Close() })
			var gate sourcework.Gate
			var retainedAuth semanticmodel.ConnectionAuth
			var callbackCalls, closeCalls int
			var detachOnClose, dropOnClose, authOnClose int
			target := &localConnectionTargetSession{onClose: func() error {
				closeCalls++
				detachOnClose = countSourceStatementPrefix(session.statements, "DETACH DATABASE IF EXISTS ")
				dropOnClose = countSourceStatementPrefix(session.statements, "DROP SECRET IF EXISTS ")
				authOnClose = len(retainedAuth)
				if failure == "target close" {
					return errors.New("private target close diagnostic")
				}
				return nil
			}}
			factory := identityTestFactory(t, func(context.Context) (TargetRuntimeSession, error) { return target, nil })
			binding := testDuckDBTargetBinding(t)
			snapshot := identityTestSnapshot(t, true)
			defer snapshot.Destroy()
			provider := &sourceAuthFatalProvider{sourceWorkTestProvider: &sourceWorkTestProvider{session: session}}
			resolver := sourceAuthLifetimeScopedConnectionResolver(func(ctx context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
				require.Equal(t, binding.ConnectionID.String(), name)
				return factory.WithLocalConnection(ctx, binding, snapshot, logical, func(connection semanticmodel.Connection) error {
					callbackCalls++
					retainedAuth = connection.Auth
					require.Equal(t, "identity-secret", connection.Auth["password"])
					require.Equal(t, binding.Endpoint.Host, connection.Host)
					return consume(connection)
				})
			})
			runtime := NewSourceRuntimeWithConnectionResolver(provider, resolver)
			runtime.extensionAdmission = targetPoolTestExtensionAdmission{}
			runtime.sourceWork = &gate
			model := &semanticmodel.Model{
				DefaultConnection: "warehouse",
				Connections:       map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres", Host: "ignored-authored-host"}},
				Sources:           map[string]semanticmodel.Source{"accounts": {Connection: "warehouse", Object: "public.accounts"}},
				Tables:            map[string]semanticmodel.Table{"accounts": {ModelName: "accounts", Execution: semanticmodel.ExecutionDefinition{Source: "accounts"}}},
			}
			prepared, err := runtime.Prepare(t.Context(), model)
			require.Equal(t, 1, callbackCalls)
			require.Equal(t, 1, closeCalls)
			require.Equal(t, 1, detachOnClose)
			require.Equal(t, 1, dropOnClose, "native source access must finish before the local pool closes")
			require.Zero(t, authOnClose, "the connection callback must clear its owned auth before closing the pool")
			require.Empty(t, retainedAuth)
			if failure != "" {
				require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
				require.NotContains(t, err.Error(), "private")
				require.Nil(t, prepared)
				require.ErrorIs(t, provider.fatalErr, analyticsruntime.ErrConnectionCleanupFailed)
				requireSourceWorkRemainsActive(t, &gate)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, prepared.Close()) })
			for _, connection := range prepared.(*PreparedSources).model.Connections {
				require.Empty(t, connection.Auth)
			}
			plan, err := prepared.PlanModelTable(t.Context(), model, "accounts", model.Tables["accounts"])
			require.NoError(t, err)
			_, err = session.ExecContext(t.Context(), "CREATE SCHEMA model")
			require.NoError(t, err)
			_, err = session.ExecContext(t.Context(), plan.SQL)
			require.NoError(t, err, "staged data must remain usable after credential and pool cleanup")
			var id int
			require.NoError(t, session.QueryRowContext(t.Context(), "SELECT account_id FROM model.accounts").Scan(&id))
			require.Equal(t, 7, id)
			requireSourceWorkDrains(t, &gate)
		})
	}
}

type localConnectionSourceSession struct {
	*sourceAuthLifetimeSession
	failCleanup bool
}

func (s *localConnectionSourceSession) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if s.failCleanup && strings.HasPrefix(query, "DROP SECRET IF EXISTS ") {
		s.statements = append(s.statements, query)
		return nil, errors.New("private source cleanup diagnostic")
	}
	return s.sourceAuthLifetimeSession.ExecContext(ctx, query, args...)
}

type localConnectionTargetSession struct {
	recordingTargetSession
	onClose func() error
}

func (s *localConnectionTargetSession) Close() error {
	s.closed = true
	return s.onClose()
}
