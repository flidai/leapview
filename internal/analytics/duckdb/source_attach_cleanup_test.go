package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/stretchr/testify/require"
)

func TestRefreshSourceAccessCleansAmbiguousAttachAndDatabaseSecret(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			model := &semanticmodel.Model{
				Connections: map[string]semanticmodel.Connection{
					"warehouse": {Kind: "postgres", Host: "warehouse.internal", Port: 5432,
						Database: "analytics", Username: "reader", Auth: semanticmodel.ConnectionAuth{"password": "test-secret"}},
				},
				Sources: map[string]semanticmodel.Source{"orders": {Connection: "warehouse", Object: "public.orders"}},
			}
			session := &ambiguousAttachSession{panics: panics}
			attempted := map[string]struct{}{}
			if panics {
				require.Panics(t, func() { _ = prepareRefreshSourceAccess(t.Context(), session, model, attempted) })
			} else {
				require.ErrorIs(t, prepareRefreshSourceAccess(t.Context(), session, model, attempted), errAmbiguousAttach)
			}
			require.Contains(t, attempted, "warehouse", "a failed native call may already have created the attachment")
			require.NoError(t, cleanupSourceAccess(session, model, attempted))
			require.Contains(t, session.cleanup, "DETACH DATABASE IF EXISTS conn_warehouse")
			require.Contains(t, session.cleanup, "DROP SECRET IF EXISTS leapview_warehouse")
		})
	}
}

func TestSourceAccessCleanupDetachesExistingAndMissingDuckDBAliases(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(t.Context(), "ATTACH ':memory:' AS conn_warehouse")
	require.NoError(t, err)
	attempted := map[string]struct{}{"warehouse": {}, "missing": {}}
	require.NoError(t, cleanupSourceAccess(conn, &semanticmodel.Model{}, attempted))
	var count int
	require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_databases() WHERE database_name = 'conn_warehouse'").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, cleanupSourceAccess(conn, &semanticmodel.Model{}, attempted), "a repeated cleanup confirms absence without failing")
}

var errAmbiguousAttach = errors.New("attach acknowledgment lost")

type ambiguousAttachSession struct {
	sourceWorkTestSession
	panics  bool
	cleanup []string
}

func (s *ambiguousAttachSession) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	if strings.HasPrefix(query, "ATTACH ") {
		if s.panics {
			panic(errAmbiguousAttach)
		}
		return nil, errAmbiguousAttach
	}
	if strings.HasPrefix(query, "DETACH ") || strings.HasPrefix(query, "DROP SECRET ") {
		s.cleanup = append(s.cleanup, query)
	}
	return nil, nil
}
