package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/config"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

func TestLocalSQLiteCompositionUsesSQLiteBootstrapPersistence(t *testing.T) {
	contents, err := os.ReadFile("composition.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	if !strings.Contains(source, "deploymentmodule.NewSQLiteBootstrapPersistence(") {
		t.Fatal("local SQLite composition is missing the explicit SQLite bootstrap persistence constructor")
	}
	genericConstructor := "deploymentmodule.New" + "BootstrapPersistence("
	if strings.Contains(source, genericConstructor) {
		t.Fatal("local SQLite composition retains the generic bootstrap persistence constructor")
	}
}

func TestLocalSQLiteCompositionUsesExplicitSQLiteAuditStore(t *testing.T) {
	contents, err := os.ReadFile("audit_runtime.go")
	if err != nil {
		t.Fatalf("read audit_runtime.go: %v", err)
	}
	source := string(contents)
	if !strings.Contains(source, "accessmodule.NewSQLiteAuditStore(") {
		t.Fatal("audit_runtime.go is missing the explicit SQLite audit-store constructor")
	}
	// The legacy admin/offline adapter no longer owns audit composition. Its
	// SQLite audit-store wiring was removed with the pre-live admin surface;
	// app auditRuntime is the sole local SQLite audit authority.
	genericConstructor := "accessmodule.New" + "AuditStore("
	if strings.Contains(source, genericConstructor) {
		t.Fatal("audit_runtime.go retains the generic access audit-store constructor")
	}
}

func TestLocalSQLiteCompositionUsesExplicitAPIProtocolAuthorities(t *testing.T) {
	contents, err := os.ReadFile("composition.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, constructor := range []string{
		"idempotencysqlite.NewStore(store.SQLDB())",
		"cursorsigningsqlite.NewInitializer(store.SQLDB())",
	} {
		if !strings.Contains(source, constructor) {
			t.Fatalf("local SQLite composition is missing explicit API protocol authority %q", constructor)
		}
	}
}

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
