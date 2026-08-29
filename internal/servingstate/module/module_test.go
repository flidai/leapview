package module

import (
	"database/sql"
	"testing"

	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

func TestBuildRequiresDatabase(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("serving-state module accepted a missing database")
	}
}

func TestBuildProductionRejectsSQLiteAndUnconfiguredNative(t *testing.T) {
	if _, err := Build(t.Context(), Config{Production: true, Database: &sql.DB{}}); err == nil {
		t.Fatal("production serving-state module accepted SQLite database")
	}
	if _, err := Build(t.Context(), Config{Production: true, Native: servingstatepostgres.New(nil)}); err == nil {
		t.Fatal("production serving-state module accepted unconfigured native persistence")
	}
}

func TestBuildDevRejectsNativeSelection(t *testing.T) {
	if _, err := Build(t.Context(), Config{Native: servingstatepostgres.New(nil)}); err == nil {
		t.Fatal("development serving-state module accepted native persistence")
	}
}
