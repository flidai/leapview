package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/app/config"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestLocalSQLiteAssemblyRejectsNonEvaluationProductionBeforeState(t *testing.T) {
	home := t.TempDir()
	cfg := config.Config{
		Production: true,
		HomeDir:    home,
	}
	_, _, _, err := assembleLocalSQLite(context.Background(), cfg)
	if !errors.Is(err, errSQLiteAuthorityProduction) {
		t.Fatalf("local SQLite production assembly error = %v, want errSQLiteAuthorityProduction", err)
	}
	_, _, _, err = buildLocalSQLiteRuntime(context.Background(), cfg, true, servingstatemodule.Environment("prod"))
	if !errors.Is(err, errSQLiteAuthorityProduction) {
		t.Fatalf("local SQLite runtime assembly error = %v, want errSQLiteAuthorityProduction", err)
	}
	_, _, _, err = buildLocalSQLiteRuntime(context.Background(), cfg, false, servingstatemodule.Environment("prod"))
	if !errors.Is(err, errSQLiteAuthorityProduction) {
		t.Fatalf("mismatched local SQLite runtime assembly error = %v, want errSQLiteAuthorityProduction", err)
	}
	entries, readErr := os.ReadDir(home)
	if readErr != nil {
		t.Fatalf("read home after rejected assembly: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected production assembly created local state: %v", entries)
	}
}

func TestSQLiteAuthorityCompositionGuardAllowsDevelopmentAndEvaluation(t *testing.T) {
	for _, test := range []struct {
		name       string
		production bool
		evaluation bool
	}{
		{name: "development"},
		{name: "disposable evaluation", production: true, evaluation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := guardSQLiteAuthorityComposition(test.production, test.evaluation); err != nil {
				t.Fatalf("guardSQLiteAuthorityComposition(%v, %v) = %v", test.production, test.evaluation, err)
			}
		})
	}
}
