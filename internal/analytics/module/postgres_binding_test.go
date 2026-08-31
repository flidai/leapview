package module

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	analyticssqlite "github.com/flidai/leapview/internal/analytics/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestBuildProductionRejectsSQLiteConnectionBinding(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true, Database: &sql.DB{}})
	if err == nil || !strings.Contains(err.Error(), "rejects SQLite") {
		t.Fatalf("production SQLite build error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionRequiresInjectedConnectionBindingAuthority(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true})
	if err == nil || !strings.Contains(err.Error(), "requires PostgreSQL connection binding authority") {
		t.Fatalf("production authority error = %v, want explicit requirement", err)
	}
}

func TestBuildProductionRejectsSQLiteBindingAuthority(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true, ConnectionBindings: analyticssqlite.NewConnectionBindingRepository(nil)})
	if err == nil || !strings.Contains(err.Error(), "native PostgreSQL") {
		t.Fatalf("production SQLite authority error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionRejectsProcessEnvironmentCredentials(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true, ConnectionBindings: connectionbindingpostgres.New(nil), CredentialMode: CredentialModeDevelopmentEnvironment})
	if err == nil || !strings.Contains(err.Error(), "process-environment") {
		t.Fatalf("production credential mode error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionRejectsUnauditedPostgreSQLAuthority(t *testing.T) {
	_, err := Build(context.Background(), Config{Production: true, ConnectionBindings: connectionbindingpostgres.New(nil)})
	if err == nil || !strings.Contains(err.Error(), "audit-capable") {
		t.Fatalf("production unaudited authority error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionRejectsUnmarkedQueryAuditStore(t *testing.T) {
	_, err := Build(context.Background(), productionAnalyticsConfig(queryAuditStoreStub{}))
	if err == nil || !strings.Contains(err.Error(), "configured native PostgreSQL query-audit authority") {
		t.Fatalf("production unmarked query-audit error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionRejectsUnconfiguredQueryAuditStore(t *testing.T) {
	_, err := Build(context.Background(), productionAnalyticsConfig(queryauditpostgres.New(nil)))
	if err == nil || !strings.Contains(err.Error(), "configured native PostgreSQL query-audit authority") {
		t.Fatalf("production unconfigured query-audit error = %v, want explicit rejection", err)
	}
}

func TestBuildProductionAcceptsConfiguredPostgreSQLQueryAuditStore(t *testing.T) {
	module, err := Build(context.Background(), productionAnalyticsConfig(queryauditpostgres.New(analyticsBuildDBStub{})))
	if err != nil {
		t.Fatalf("production configured query-audit build error = %v", err)
	}
	if module == nil || module.QueryAuditReader() == nil {
		t.Fatal("production configured query-audit authority was not retained")
	}
	if err := module.Close(); err != nil {
		t.Fatalf("close production analytics module: %v", err)
	}
}

// productionAnalyticsConfig keeps the query-audit admission tests isolated
// from DuckLake and process credentials while still exercising the native
// connection-binding production gate.
func productionAnalyticsConfig(queryAuditStore queryaudit.Store) Config {
	db := analyticsBuildDBStub{}
	binding, _ := connectionbindingpostgres.NewProduction(db, analyticsBuildAuditStub{})
	return Config{
		Production: true, ConnectionBindings: binding, QueryAuditStore: queryAuditStore,
		CredentialMode: CredentialModeNonSecret, DisableProcessEnvironment: true,
		RuntimeCacheEntries: 1, RuntimeCacheBytes: 1, NodeCacheEntries: 1, NodeCacheBytes: 1,
	}
}

type analyticsBuildDBStub struct{}

func (analyticsBuildDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (analyticsBuildDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (analyticsBuildDBStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (analyticsBuildDBStub) Begin(context.Context) (pgx.Tx, error)            { return nil, nil }

type analyticsBuildAuditStub struct{}

func (analyticsBuildAuditStub) RecordAuditEvent(context.Context, connectionbindingpostgres.Tx, access.AuditIntent) error {
	return nil
}

type queryAuditStoreStub struct{ queryaudit.Store }
