package module

import (
	"testing"

	"github.com/flidai/leapview/internal/platform"
)

func TestSQLiteRecoveryConstructorsKeepDatabaseAuthorityLocal(t *testing.T) {
	if repository := NewSQLiteRecoveryRepository(nil); repository != nil {
		t.Fatal("nil SQLite recovery database produced a repository")
	}

	store, err := platform.Open(t.Context(), t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()

	repository := NewSQLiteRecoveryRepository(store.SQLDB())
	if repository == nil {
		t.Fatal("SQLite recovery database did not produce a repository")
	}
	lifecycle := RecoveryLifecycle{Repository: NewSQLiteRecoveryRepository(store.SQLDB())}
	if lifecycle.Repository == nil {
		t.Fatal("SQLite recovery lifecycle did not bind its local repository")
	}
}
