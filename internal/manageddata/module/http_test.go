package module

import (
	"context"
	"database/sql"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"strings"
	"testing"
	"time"
)

func TestBuildRejectsMissingOwnedPersistence(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("managed-data module accepted missing database")
	}
}

func TestBuildProductionFailsClosedWithoutNativePostgres(t *testing.T) {
	if _, err := Build(t.Context(), Config{Production: true}); err == nil || !strings.Contains(err.Error(), "native PostgreSQL") {
		t.Fatalf("production build error = %v, want native PostgreSQL requirement", err)
	}
}

func TestBuildProductionRejectsSQLitePersistence(t *testing.T) {
	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{Database: new(sql.DB)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(t.Context(), Config{Production: true, Persistence: &persistence}); err == nil || !strings.Contains(err.Error(), "native PostgreSQL") {
		t.Fatalf("production SQLite build error = %v, want native PostgreSQL requirement", err)
	}
}

func TestBuildRejectsMissingCommandAuditSinkWhenEnabled(t *testing.T) {
	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{Database: new(sql.DB)})
	if err != nil {
		t.Fatal(err)
	}
	if module, err := Build(t.Context(), Config{Persistence: &persistence}); module != nil || err == nil {
		t.Fatalf("module = %v, err = %v", module, err)
	}
}

func TestBuildCanExposeExplicitlyDisabledSurface(t *testing.T) {
	module, err := Build(t.Context(), Config{Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if module.HTTP() == nil {
		t.Fatal("disabled managed-data module did not expose its unavailable handler")
	}
	if module.RuntimeResolution() == nil {
		t.Fatal("disabled managed-data module did not expose no-op runtime resolver")
	}
	module.Start(nil)
	if err := module.Stop(nil); err != nil {
		t.Fatalf("disabled Stop(nil): %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := module.Stop(stopCtx); err != nil {
		t.Fatalf("disabled Stop(timeout): %v", err)
	}
	if handler := module.TusHandler(); handler != nil {
		t.Fatal("disabled managed-data module exposed a TUS handler")
	}
	if got, err := module.RuntimeResolution().ResolveManagedData(context.Background(), projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "state"}); err != nil || len(got.Roots) != 0 {
		t.Fatalf("disabled runtime resolution = %#v, %v", got, err)
	}
}

func TestNilModuleLifecycleAndHTTPAreSafe(t *testing.T) {
	var module *Module
	module.Start(nil)
	if err := module.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if module.HTTP() != nil || module.TusHandler() != nil || module.RuntimeResolution() != nil {
		t.Fatal("nil module exposed capabilities")
	}
}
