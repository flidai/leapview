package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRuntimeFactoryHasNoControlPlaneSQLiteDependencies keeps the production
// runtime factory independent from the local control-plane implementation. The
// walk intentionally discovers every authored non-test Go file rather than
// maintaining a list of known files, so a newly added adapter cannot evade the
// boundary by choosing a different filename.
func TestRuntimeFactoryHasNoControlPlaneSQLiteDependencies(t *testing.T) {
	root := repoRoot(t)
	runtimeFactoryRoot := filepath.Join(root, "internal", "app", "runtimefactory")
	if info, err := os.Stat(runtimeFactoryRoot); err != nil {
		t.Fatalf("stat runtimefactory package: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("runtimefactory path %s is not a directory", runtimeFactoryRoot)
	}

	err := filepath.WalkDir(runtimeFactoryRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			switch {
			case importPath == "database/sql":
				t.Errorf("%s imports database/sql; production runtimefactory must not depend on the local control-plane database API", relative)
			case isRuntimeFactorySQLiteAdapterImport(importPath):
				t.Errorf("%s imports control-plane SQLite adapter %q; move local persistence behind localruntimefactory", relative, importPath)
			case isRuntimeFactoryLocalCompositionImport(importPath):
				t.Errorf("%s imports localruntimefactory %q; local composition must remain outside the production-facing runtimefactory package", relative, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan runtimefactory production sources: %v", err)
	}

	buildSource, err := os.ReadFile(filepath.Join(root, "internal", "app", "build.go"))
	if err != nil {
		t.Fatalf("read production build entrypoint: %v", err)
	}
	buildText := string(buildSource)
	for _, required := range []string{"func BuildProduction(", "buildPostgresProductionTarget("} {
		if !strings.Contains(buildText, required) {
			t.Errorf("production entrypoint missing %q", required)
		}
	}

	localText := readAppProductionSources(t, filepath.Join(root, "internal", "app"))
	for _, required := range []string{"guardSQLiteAuthorityComposition(", "localruntimefactory.NewSQLiteSealedFactory("} {
		if !strings.Contains(localText, required) {
			t.Errorf("local composition is missing explicit SQLite guard/factory boundary %q", required)
		}
	}
}

func readAppProductionSources(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read app package: %v", err)
	}
	var sources strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatalf("read app source %s: %v", entry.Name(), err)
		}
		sources.Write(source)
	}
	return sources.String()
}

// isRuntimeFactorySQLiteAdapterImport recognizes capability-owned SQLite
// adapters without rejecting generic runtimefactory dependencies. SQLite is a
// package segment under LeapView's internal tree, not a substring allowlist.
func isRuntimeFactorySQLiteAdapterImport(importPath string) bool {
	if !strings.HasPrefix(importPath, modulePath+"/internal/") {
		return false
	}
	internalPath := strings.TrimPrefix(importPath, modulePath+"/internal/")
	for _, segment := range strings.Split(internalPath, "/") {
		if segment == "sqlite" {
			return true
		}
	}
	return false
}

func isRuntimeFactoryLocalCompositionImport(importPath string) bool {
	const localPrefix = modulePath + "/internal/app/localruntimefactory"
	return importPath == localPrefix || strings.HasPrefix(importPath, localPrefix+"/")
}
