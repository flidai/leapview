package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

func TestSourceRuntimeCredentialCallbackOwnsIndependentAuthMaps(t *testing.T) {
	model := &semanticmodel.Model{
		DefaultConnection: "crm",
		Connections: map[string]semanticmodel.Connection{
			"crm": {Kind: "postgres", Auth: semanticmodel.ConnectionAuth{"password": "authored-value"}},
		},
		Sources: map[string]semanticmodel.Source{"accounts": {Connection: "crm", Object: "public.accounts"}},
	}
	resolverAuth := semanticmodel.ConnectionAuth{"password": "resolver-value"}
	runtime := NewSourceRuntimeWithCredentials(nil, sourceAuthLifetimeResolver(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
		return resolverAuth, nil
	}))

	connection := model.Connections[model.DefaultConnection]
	var firstSeen, secondSeen any
	err := runtime.withResolvedConnection(context.Background(), model.DefaultConnection, connection, func(resolved semanticmodel.Connection) error {
		firstSeen = resolved.Auth["password"]
		resolved.Auth["password"] = "first-refresh-mutated"
		return nil
	})
	require.NoError(t, err)
	err = runtime.withResolvedConnection(context.Background(), model.DefaultConnection, connection, func(resolved semanticmodel.Connection) error {
		secondSeen = resolved.Auth["password"]
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, "resolver-value", firstSeen)
	require.Equal(t, "resolver-value", secondSeen, "each callback must own an independent auth map")
	require.Equal(t, "resolver-value", resolverAuth["password"], "runtime mutation must not reach resolver-owned auth")
	require.Equal(t, "authored-value", model.Connections["crm"].Auth["password"], "resolution must not mutate the input model")
}

func TestSourceRuntimeConnectionCallbackClonesTargetAuthAndDropsManagedAuth(t *testing.T) {
	targetAuth := semanticmodel.ConnectionAuth{"password": "target-value"}
	model := &semanticmodel.Model{
		DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{
			"warehouse": {Kind: "postgres"},
			"managed":   {Kind: "managed", Root: "/managed", Auth: semanticmodel.ConnectionAuth{"token": "authored-value"}},
		},
	}
	runtime := NewSourceRuntimeWithConnectionResolver(nil, sourceAuthLifetimeConnectionResolver(func(context.Context, string, semanticmodel.Connection) (semanticmodel.Connection, error) {
		return semanticmodel.Connection{Kind: "postgres", Auth: targetAuth}, nil
	}))

	var warehousePassword any
	err := runtime.withResolvedConnection(context.Background(), model.DefaultConnection, model.Connections[model.DefaultConnection], func(connection semanticmodel.Connection) error {
		warehousePassword = connection.Auth["password"]
		connection.Auth["password"] = "consumer-mutated"
		return nil
	})
	require.NoError(t, err)
	var managed semanticmodel.Connection
	err = runtime.withResolvedConnection(context.Background(), "managed", model.Connections["managed"], func(connection semanticmodel.Connection) error {
		managed = connection
		return nil
	})
	require.NoError(t, err)

	require.Equal(t, "target-value", warehousePassword)
	require.Equal(t, "target-value", targetAuth["password"], "target resolver auth must remain caller-owned")
	require.Nil(t, managed.Auth, "managed connections must not retain authored auth")
	require.Equal(t, "authored-value", model.Connections["managed"].Auth["token"], "the authored model must remain untouched")
}

func TestSourceRuntimePreparedStagingPlansAfterOwnedAuthIsCleared(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	session := &sourceAuthLifetimeSession{Session: conn, conn: conn}
	provider := &sourceWorkTestProvider{session: session}
	resolverAuth := semanticmodel.ConnectionAuth{"password": "temporary-secret"}
	runtime := NewSourceRuntimeWithCredentials(provider, sourceAuthLifetimeResolver(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
		return resolverAuth, nil
	}))
	runtime.extensionAdmission = targetPoolTestExtensionAdmission{}
	model := &semanticmodel.Model{
		DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{
			"warehouse": {Kind: "postgres", Host: "warehouse.internal", Database: "analytics"},
		},
		Sources: map[string]semanticmodel.Source{
			"accounts": {Connection: "warehouse", Object: "public.accounts"},
		},
		Tables: map[string]semanticmodel.Table{
			"accounts": {ModelName: "accounts", Execution: semanticmodel.ExecutionDefinition{Source: "accounts"}},
		},
	}

	preparedSources, err := runtime.Prepare(t.Context(), model)
	require.NoError(t, err, "statements: %v", session.statements)
	prepared := preparedSources.(*PreparedSources)
	t.Cleanup(func() {
		require.NoError(t, prepared.Close())
		if session.conn != nil {
			require.NoError(t, session.Close())
		}
	})
	for name, connection := range prepared.model.Connections {
		require.Nil(t, connection.Auth, "prepared source %q retained auth after Prepare", name)
	}
	require.Equal(t, "temporary-secret", resolverAuth["password"], "cleanup must not mutate resolver-owned auth")

	plan, err := prepared.PlanModelTable(t.Context(), model, "accounts", model.Tables["accounts"])
	require.NoError(t, err)
	require.Contains(t, plan.SQL, "leapview_stage_")
	_, err = session.ExecContext(t.Context(), "CREATE SCHEMA model")
	require.NoError(t, err)
	_, err = session.ExecContext(t.Context(), plan.SQL)
	require.NoError(t, err, "staged plan must execute after auth cleanup")
	var count int
	require.NoError(t, session.QueryRowContext(t.Context(), "SELECT count(*) FROM model.accounts").Scan(&count))
	require.Equal(t, 1, count)
}

func TestSourceRuntimeScopesEachSourceThroughCleanup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		sourceCount       int
		ownerCloseErr     bool
		tableCleanupPanic bool
	}{
		{name: "per source callback", sourceCount: 2},
		{name: "owner close failure", sourceCount: 1, ownerCloseErr: true},
		{name: "staged table cleanup panic", sourceCount: 1, ownerCloseErr: true, tableCleanupPanic: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("duckdb", ":memory:")
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			session := &sourceAuthLifetimeSession{Session: conn, conn: conn}
			session.panicOnDropTable = tc.tableCleanupPanic
			provider := &sourceAuthFatalProvider{sourceWorkTestProvider: &sourceWorkTestProvider{session: session}}
			targetAuth := semanticmodel.ConnectionAuth{"password": "owner-callback-secret"}
			var gate sourcework.Gate
			resolverCalls := 0
			resolver := sourceAuthLifetimeScopedConnectionResolver(func(ctx context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
				resolverCalls++
				require.GreaterOrEqual(t, countSourceStatementPrefix(session.statements, "DETACH DATABASE IF EXISTS "), resolverCalls-1, "the previous source callback returned before native detach")
				require.GreaterOrEqual(t, countSourceStatementPrefix(session.statements, "DROP SECRET IF EXISTS "), resolverCalls-1, "the previous source callback returned before secret removal")
				resolved := semanticmodel.Connection{Kind: "postgres", Host: "warehouse.internal", Database: "analytics", Auth: targetAuth}
				if err := consume(resolved); err != nil {
					return err
				}
				require.Equal(t, resolverCalls, countSourceStatementPrefix(session.statements, "DETACH DATABASE IF EXISTS "), "the current source returned before native detach")
				require.Equal(t, resolverCalls, countSourceStatementPrefix(session.statements, "DROP SECRET IF EXISTS "), "the current source returned before secret removal")
				if tc.ownerCloseErr {
					return analyticsruntime.ErrConnectionCleanupFailed
				}
				return nil
			})
			runtime := NewSourceRuntimeWithConnectionResolver(provider, resolver)
			runtime.extensionAdmission = targetPoolTestExtensionAdmission{}
			runtime.sourceWork = &gate
			sources := map[string]semanticmodel.Source{}
			for index := 0; index < tc.sourceCount; index++ {
				name := fmt.Sprintf("source_%d", index)
				sources[name] = semanticmodel.Source{Connection: "warehouse", Object: "public.accounts"}
			}
			model := &semanticmodel.Model{
				DefaultConnection: "warehouse",
				Connections:       map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres", Host: "authored-host"}},
				Sources:           sources,
			}

			prepared, err := runtime.Prepare(t.Context(), model)
			require.Equal(t, tc.sourceCount, resolverCalls)
			require.Equal(t, "owner-callback-secret", targetAuth["password"], "runtime cleanup must not mutate resolver-owned auth")
			if tc.ownerCloseErr {
				if prepared != nil {
					_ = prepared.Close()
					t.Fatal("owner cleanup failure returned prepared sources")
				}
				require.ErrorIs(t, err, analyticsruntime.ErrConnectionCleanupFailed)
				require.NotContains(t, err.Error(), "owner-callback-secret")
				require.NotContains(t, err.Error(), "private staged table cleanup diagnostic")
				require.ErrorIs(t, provider.fatalErr, analyticsruntime.ErrConnectionCleanupFailed, "owner close uncertainty must mark the DuckDB provider fatal")
				requireSourceWorkRemainsActive(t, &gate)
				return
			}
			require.NoError(t, err, "statements: %v", session.statements)
			require.NotNil(t, prepared)
			t.Cleanup(func() {
				require.NoError(t, prepared.Close())
				require.NoError(t, session.Close())
			})
			require.Len(t, prepared.(*PreparedSources).model.Sources, tc.sourceCount)
			require.Equal(t, tc.sourceCount, countSourceStatementPrefix(session.statements, "DETACH DATABASE IF EXISTS "))
			require.Equal(t, tc.sourceCount, countSourceStatementPrefix(session.statements, "DROP SECRET IF EXISTS "))
			require.Equal(t, 1, countSourceStatementPrefix(session.statements, "LOAD "), "extension loads should be shared by the session")
			preparedModel := prepared.(*PreparedSources).model
			require.Equal(t, "warehouse", preparedModel.DefaultConnection, "prepared metadata must retain the authored connection name")
			require.Equal(t, "authored-host", preparedModel.Connections["warehouse"].Host, "target endpoint metadata must remain callback-scoped")
			for _, connection := range prepared.(*PreparedSources).model.Connections {
				require.Nil(t, connection.Auth, "prepared metadata must not retain Auth")
			}
			for sourceName, source := range preparedModel.Sources {
				require.Equal(t, "warehouse", source.Connection, "prepared source must not retain an ephemeral connection alias")
				require.NotEmpty(t, source.Schema.Columns, "discovered schema metadata must be retained after callback cleanup")
				require.Equal(t, source.Schema, model.Sources[sourceName].Schema, "discovered schema must flow back to the compiled model")
			}
			requireSourceWorkDrains(t, &gate)
		})
	}
}

type sourceAuthFatalProvider struct {
	*sourceWorkTestProvider
	fatalErr error
}

func (p *sourceAuthFatalProvider) MarkFatal(err error) { p.fatalErr = err }

type sourceAuthLifetimeScopedConnectionResolver func(context.Context, string, semanticmodel.Connection, func(semanticmodel.Connection) error) error

func (f sourceAuthLifetimeScopedConnectionResolver) WithConnection(ctx context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
	return f(ctx, name, logical, consume)
}

func countSourceStatementPrefix(statements []string, prefix string) int {
	count := 0
	for _, statement := range statements {
		if strings.HasPrefix(statement, prefix) {
			count++
		}
	}
	return count
}

type sourceAuthLifetimeResolver func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error)

func (f sourceAuthLifetimeResolver) Resolve(ctx context.Context, name string, connection semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
	return f(ctx, name, connection)
}

type sourceAuthLifetimeConnectionResolver func(context.Context, string, semanticmodel.Connection) (semanticmodel.Connection, error)

func (f sourceAuthLifetimeConnectionResolver) WithConnection(ctx context.Context, name string, connection semanticmodel.Connection, consume func(semanticmodel.Connection) error) error {
	resolved, err := f(ctx, name, connection)
	if err != nil {
		return err
	}
	return consume(resolved)
}

type sourceAuthLifetimeSession struct {
	analyticsresource.Session
	conn             *sql.Conn
	statements       []string
	panicOnDropTable bool
}

func (s *sourceAuthLifetimeSession) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.statements = append(s.statements, query)
	switch {
	case strings.HasPrefix(query, "LOAD "), strings.HasPrefix(query, "CREATE OR REPLACE TEMPORARY SECRET "), strings.HasPrefix(query, "DROP SECRET IF EXISTS "):
		return nil, nil
	case s.panicOnDropTable && strings.HasPrefix(query, "DROP TABLE IF EXISTS "):
		panic("private staged table cleanup diagnostic")
	case strings.HasPrefix(query, "ATTACH "):
		fields := strings.Fields(query)
		alias := ""
		for index, field := range fields {
			if field == "AS" && index+1 < len(fields) {
				alias = fields[index+1]
				break
			}
		}
		if alias == "" {
			return nil, fmt.Errorf("test ATTACH statement is missing alias")
		}
		if _, err := s.conn.ExecContext(ctx, "ATTACH ':memory:' AS "+alias); err != nil {
			return nil, err
		}
		if _, err := s.conn.ExecContext(ctx, "CREATE SCHEMA "+alias+".public"); err != nil {
			return nil, err
		}
		if _, err := s.conn.ExecContext(ctx, "CREATE TABLE "+alias+".public.accounts (account_id INTEGER)"); err != nil {
			return nil, err
		}
		_, err := s.conn.ExecContext(ctx, "INSERT INTO "+alias+".public.accounts VALUES (7)")
		return nil, err
	default:
		return s.conn.ExecContext(ctx, query, args...)
	}
}

func (s *sourceAuthLifetimeSession) Close() error { return s.conn.Close() }

var _ analyticsruntime.ConnectionResolver = sourceAuthLifetimeConnectionResolver(nil)
var _ analyticsresource.Session = (*sourceAuthLifetimeSession)(nil)
