package module

import (
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/platform"
)

func TestBuildRejectsIncompleteOwnedDeploymentComposition(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{Database: store.SQLDB()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(t.Context(), Config{Persistence: &persistence}); err == nil {
		t.Fatal("deployment module accepted a database without its required capability ports")
	}
}
