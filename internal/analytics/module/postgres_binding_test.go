package module

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	analyticssqlite "github.com/flidai/leapview/internal/analytics/sqlite"
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
