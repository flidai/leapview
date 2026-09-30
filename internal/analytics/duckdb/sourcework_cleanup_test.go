package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	extensiondomain "github.com/flidai/leapview/internal/extension"
	"github.com/stretchr/testify/require"
)

func TestSourceRuntimeCleanupUncertaintyQuarantinesAdmissionLease(t *testing.T) {
	cases := []struct {
		name          string
		failDetach    bool
		failDrop      bool
		failClose     bool
		wantErrDetail string
	}{
		{name: "detach", failDetach: true, wantErrDetail: "detach driver secret"},
		{name: "drop secret", failDrop: true, wantErrDetail: "drop driver secret"},
		{name: "session close", failClose: true, wantErrDetail: "close driver secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSourceWorkCleanupFixture(t, sourceWorkCleanupFailures{
				detachErr: errorWhen(tc.failDetach, tc.wantErrDetail),
				dropErr:   errorWhen(tc.failDrop, tc.wantErrDetail),
				closeErr:  errorWhen(tc.failClose, tc.wantErrDetail),
			})

			_, err := fixture.runtime.Prepare(context.Background(), sourceWorkCleanupModel())
			require.Error(t, err)
			require.ErrorIs(t, err, errSourceCleanupFailed)
			require.NotContains(t, err.Error(), tc.wantErrDetail)
			requireSourceStatementPrefix(t, fixture.session.statements, "DETACH DATABASE IF EXISTS conn_crm_r")
			requireSourceStatementPrefix(t, fixture.session.statements, "DROP SECRET IF EXISTS leapview_crm_r")
			requireSourceWorkRemainsActive(t, fixture.gate)
		})
	}
}

func TestSourceRuntimePreparationErrorReleasesLeaseAfterSuccessfulCleanup(t *testing.T) {
	fixture := newSourceWorkCleanupFixture(t, sourceWorkCleanupFailures{})
	_, err := fixture.runtime.Prepare(context.Background(), sourceWorkCleanupModel())
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquiring source \"accounts\" failed")
	require.NotContains(t, err.Error(), "stage driver secret")
	require.NotErrorIs(t, err, errSourceCleanupFailed)
	requireSourceStatementPrefix(t, fixture.session.statements, "DROP SECRET IF EXISTS leapview_crm_r")
	requireSourceWorkDrains(t, fixture.gate)
}

func TestSourceRuntimePanicQuarantinesAdmissionLeaseAndRedactsPanic(t *testing.T) {
	fixture := newSourceWorkCleanupFixture(t, sourceWorkCleanupFailures{panicOnStage: true})
	_, err := fixture.runtime.Prepare(context.Background(), sourceWorkCleanupModel())
	require.ErrorIs(t, err, errSourceCleanupFailed)
	require.NotContains(t, err.Error(), "stage driver secret diagnostics")
	requireSourceStatementPrefix(t, fixture.session.statements, "DETACH DATABASE IF EXISTS conn_crm_r")
	requireSourceStatementPrefix(t, fixture.session.statements, "DROP SECRET IF EXISTS leapview_crm_r")
	requireSourceWorkRemainsActive(t, fixture.gate)
}

type sourceWorkCleanupFixture struct {
	runtime *SourceRuntime
	gate    *sourcework.Gate
	session *sourceWorkCleanupSession
}

type sourceWorkCleanupFailures struct {
	detachErr    error
	dropErr      error
	closeErr     error
	panicOnStage bool
}

func newSourceWorkCleanupFixture(t *testing.T, failures sourceWorkCleanupFailures) sourceWorkCleanupFixture {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	session := &sourceWorkCleanupSession{conn: conn, failures: failures}
	provider := &sourceWorkTestProvider{session: session}
	gate := &sourcework.Gate{}
	runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(
		func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
			return semanticmodel.ConnectionAuth{"password": "credential-value"}, nil
		},
	))
	runtime.extensionAdmission = sourceWorkCleanupAdmission{}
	runtime.sourceWork = gate
	return sourceWorkCleanupFixture{runtime: runtime, gate: gate, session: session}
}

func sourceWorkCleanupModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		DefaultConnection: "crm",
		Connections: map[string]semanticmodel.Connection{
			"crm": {Kind: "postgres", Host: "warehouse.internal", Database: "analytics"},
		},
		Sources: map[string]semanticmodel.Source{
			"accounts": {Connection: "crm", Object: "public.accounts"},
		},
	}
}

type sourceWorkCleanupSession struct {
	conn       *sql.Conn
	failures   sourceWorkCleanupFailures
	statements []string
}

func (s *sourceWorkCleanupSession) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	s.statements = append(s.statements, query)
	switch {
	case strings.HasPrefix(query, "LOAD "):
		return nil, nil
	case strings.HasPrefix(query, "CREATE OR REPLACE TEMPORARY SECRET "):
		return nil, nil
	case strings.HasPrefix(query, "ATTACH "):
		fields := strings.Fields(query)
		for index := 0; index+1 < len(fields); index++ {
			if fields[index] == "AS" {
				return s.conn.ExecContext(ctx, "ATTACH ':memory:' AS "+fields[index+1])
			}
		}
		return nil, fmt.Errorf("test ATTACH statement is missing alias")
	case strings.HasPrefix(query, "CREATE TEMP TABLE "):
		if s.failures.panicOnStage {
			panic(errors.New("stage driver secret diagnostics"))
		}
		return nil, errors.New("stage driver secret diagnostics")
	case strings.HasPrefix(query, "DETACH DATABASE IF EXISTS "):
		if s.failures.detachErr != nil {
			return nil, s.failures.detachErr
		}
		return s.conn.ExecContext(ctx, query, args...)
	case strings.HasPrefix(query, "DROP SECRET IF EXISTS "):
		return nil, s.failures.dropErr
	default:
		return s.conn.ExecContext(ctx, query, args...)
	}
}

type sourceWorkCleanupAdmission struct{}

func (sourceWorkCleanupAdmission) AdmitExtension(_ context.Context, name string) (AdmittedExtension, error) {
	return AdmittedExtension{
		Name: name, Identity: "test-identity", Version: "test-version", Platform: "test-platform",
		Digest: "sha256:" + strings.Repeat("a", 64), Path: "/tmp/" + extensiondomain.ArtifactFilenameStem(name) + ".duckdb_extension",
	}, nil
}

func (s *sourceWorkCleanupSession) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.conn.QueryContext(ctx, query, args...)
}

func (s *sourceWorkCleanupSession) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.conn.QueryRowContext(ctx, query, args...)
}

func (s *sourceWorkCleanupSession) Close() error {
	connErr := s.conn.Close()
	return errors.Join(s.failures.closeErr, connErr)
}

func errorWhen(condition bool, message string) error {
	if !condition {
		return nil
	}
	return errors.New(message)
}

func requireSourceStatementPrefix(t *testing.T, statements []string, prefix string) {
	t.Helper()
	for _, statement := range statements {
		if strings.HasPrefix(statement, prefix) {
			return
		}
	}
	t.Fatalf("statements %#v do not include prefix %q", statements, prefix)
}

func requireSourceWorkDrains(t *testing.T, gate *sourcework.Gate) {
	t.Helper()
	pause, err := gate.Pause()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, pause.WaitDrained(ctx))
	require.NoError(t, pause.Resume())
}

func requireSourceWorkRemainsActive(t *testing.T, gate *sourcework.Gate) {
	t.Helper()
	pause, err := gate.Pause()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, pause.WaitDrained(ctx), context.DeadlineExceeded)
}
