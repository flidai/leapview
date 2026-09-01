package module

import (
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/platform"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

func TestBuildRequiresDatabase(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("serving-state module accepted missing persistence")
	}
}

func TestBuildProductionRejectsSQLiteAndUnconfiguredNative(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	persistence, err := NewSQLitePersistence(store.SQLDB())
	if err == nil {
		if _, err := Build(t.Context(), Config{Production: true, Persistence: &persistence}); err == nil {
			t.Fatal("production serving-state module accepted SQLite database")
		}
	} else {
		t.Fatal(err)
	}
	native := servingstatepostgres.New(nil)
	if _, err := Build(t.Context(), Config{Production: true, Persistence: &Persistence{native: native, backend: backendPostgres}}); err == nil {
		t.Fatal("production serving-state module accepted unconfigured native persistence")
	}
}

func TestBuildDevRejectsNativeSelection(t *testing.T) {
	native := servingstatepostgres.New(nil)
	if _, err := Build(t.Context(), Config{Persistence: &Persistence{native: native, backend: backendPostgres}}); err == nil {
		t.Fatal("development serving-state module accepted native persistence")
	}
}
