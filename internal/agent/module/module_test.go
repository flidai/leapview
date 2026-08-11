package module

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform"
)

func TestBuildConstructsAgentServiceAndPersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(),
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

	if _, err := Build(t.Context(), Config{Database: store.SQLDB()}); err == nil {
		t.Fatal("agent module accepted an enabled command service without an audit recorder")
	}
}
