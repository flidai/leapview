package module

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBuildConstructsAgentServiceAndPersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{
		Database:            store.SQLDB(),
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
	})
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence:         &persistence,
		ProjectID:           projectgraph.ResourceID("project:agent-test"),
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
		RecordAudit: func(context.Context, access.AuditEventInput) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if module.service == nil || module.HTTP() == nil {
		t.Fatal("agent module did not construct its owned service and transport")
	}
}

func TestBuildRejectsEnabledAgentCommandsWithoutAuditRecorder(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{Database: store.SQLDB()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(t.Context(), Config{Persistence: &persistence, ProjectID: projectgraph.ResourceID("project:agent-test")}); err == nil {
		t.Fatal("agent module accepted an enabled command service without an audit recorder")
	}
}

func TestNewSQLitePersistenceRequiresDatabase(t *testing.T) {
	if _, err := NewSQLitePersistence(SQLitePersistenceConfig{}); err == nil {
		t.Fatal("agent SQLite persistence accepted a missing database")
	}
}

func TestBuildProductionRejectsSQLitePersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "agent-legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{Database: store.SQLDB()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(t.Context(), Config{Persistence: &persistence, Production: true, ProjectID: projectgraph.ResourceID("project:agent-test")}); err == nil {
		t.Fatal("production agent module accepted SQLite persistence")
	}
}

func TestBuildAllowsUnboundProjectUntilActiveResolverBinds(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{
		Database:            store.SQLDB(),
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var active projectgraph.ResourceID
	module, err := Build(t.Context(), Config{
		Persistence:         &persistence,
		AuditIntentRecorder: accesssqlite.NewRepository(store.SQLDB()),
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return active, nil
		},
		RecordAudit: func(context.Context, access.AuditEventInput) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("unbound build failed: %v", err)
	}
	if _, err := module.activeProjectID(t.Context()); err == nil {
		t.Fatal("unbound project-dependent operation unexpectedly succeeded")
	}

	active = projectgraph.ResourceID("project:activated")
	if got, err := module.activeProjectID(t.Context()); err != nil || got != active.String() {
		t.Fatalf("resolved active project = %q, err=%v; want %q", got, err, active)
	}
}
