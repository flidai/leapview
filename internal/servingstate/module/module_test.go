package module

import (
	"testing"

	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

func TestBuildRequiresDatabase(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("serving-state module accepted missing persistence")
	}
}

func TestBuildProductionRejectsUnconfiguredNative(t *testing.T) {
	native := servingstatepostgres.New(nil)
	if _, err := Build(t.Context(), Config{Production: true, Persistence: &Persistence{native: native}}); err == nil {
		t.Fatal("production serving-state module accepted unconfigured native persistence")
	}
}

func TestBuildDevRejectsNativeSelection(t *testing.T) {
	native := servingstatepostgres.New(nil)
	if _, err := Build(t.Context(), Config{Persistence: &Persistence{native: native}}); err == nil {
		t.Fatal("development serving-state module accepted native persistence")
	}
}
