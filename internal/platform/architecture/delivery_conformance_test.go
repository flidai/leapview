package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPlanDeliveryPhysicalAuthorityGuards keeps the immutable DuckLake
// boundary reviewable in source. It intentionally scans authored production
// code and schema only; qualification tests may mention forbidden statements
// as adversarial inputs without making them reachable. PostgreSQL-native
// snapshot seals own delivery identity; the removed generic catalog-seal
// package is not part of this production closure.
func TestPlanDeliveryPhysicalAuthorityGuards(t *testing.T) {
	root := repoRoot(t)
	for _, retired := range []string{
		"internal/analytics/candidatecatalog",
		"internal/deployment/gc",
		"internal/app/gcadapter/inspector.go",
		"internal/app/gcadapter/maintenance.go",
		"internal/app/gcadapter/repair.go",
		"internal/app/gcadapter/runner.go",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(retired))); err == nil {
			t.Errorf("retired file-catalog authority %s must not exist", retired)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat retired file-catalog authority %s: %v", retired, err)
		}
	}
	// build.go dispatches every app entrypoint to postgres_build.go. Local
	// SQLite authority is intentionally absent from the application graph.
	productionRoots := []string{
		"internal/deployment", "internal/app/runtimefactory", "internal/app/build.go", "internal/app/postgres_build.go",
	}
	forbidden := []string{
		"file_membership", "table_membership", "reference_count",
		"CREATE TABLE data_file", "CREATE TABLE delete_file",
		"physical_manifest", "physical publication",
	}
	dangerousNative := []string{"CALL ducklake_cleanup_old_files", "CALL ducklake_delete_orphaned_files", "CHECKPOINT "}
	for _, relative := range productionRoots {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		visit := func(filePath string, body string) {
			if strings.HasSuffix(filePath, "_test.go") || filepath.Ext(filePath) == ".gen.go" {
				return
			}
			lower := strings.ToLower(body)
			for _, token := range forbidden {
				if strings.Contains(lower, strings.ToLower(token)) {
					t.Errorf("%s retains forbidden SQLite/legacy physical authority token %q", filePath, token)
				}
			}
			if relative == "internal/deployment" || relative == "internal/app/runtimefactory" {
				for _, token := range dangerousNative {
					if strings.Contains(lower, strings.ToLower(token)) {
						t.Errorf("%s reaches native DuckLake maintenance %q from delivery/serving code", filePath, token)
					}
				}
			}
		}
		if info.IsDir() {
			err := filepath.Walk(path, func(filePath string, fileInfo os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if fileInfo != nil && !fileInfo.IsDir() && (strings.HasSuffix(filePath, ".go") || strings.HasSuffix(filePath, ".sql")) {
					body, readErr := os.ReadFile(filePath)
					if readErr != nil {
						return readErr
					}
					visit(filePath, string(body))
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		} else {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			visit(relative, string(body))
		}
	}

	productionBuild, err := os.ReadFile(filepath.Join(root, "internal/app/build.go"))
	if err != nil {
		t.Fatal(err)
	}
	productionBuildText := string(productionBuild)
	for _, required := range []string{"BuildProduction", "buildPostgresTarget"} {
		if !strings.Contains(productionBuildText, required) {
			t.Errorf("production entrypoint missing PostgreSQL delivery gate %q", required)
		}
	}
	if strings.Contains(productionBuildText, "NewSQLiteSealedFactory") {
		t.Error("production entrypoint retains the legacy NewSQLiteSealedFactory")
	}

	postgresBuild, err := os.ReadFile(filepath.Join(root, "internal/app/postgres_build.go"))
	if err != nil {
		t.Fatal(err)
	}
	postgresBuildText := string(postgresBuild)
	for _, required := range []string{"NewPostgresSealedFactory", "RequireSealedCatalog: true", "ResolveSealedActiveState"} {
		if !strings.Contains(postgresBuildText, required) {
			t.Errorf("PostgreSQL production composition missing sealed delivery/startup gate %q", required)
		}
	}
	if strings.Contains(postgresBuildText, "NewSQLiteSealedFactory") {
		t.Error("PostgreSQL production composition retains the legacy NewSQLiteSealedFactory")
	}
}

// TestPostgresRuntimeRootsDoNotReachLocalCatalogConstructors keeps the
// production assembly boundary explicit. Only the BuildProduction dispatch,
// PostgreSQL composition, and PostgreSQL serving runtime are roots here; the
// removed local SQLite authority graph is not a production closure.
func TestPostgresRuntimeRootsDoNotReachLocalCatalogConstructors(t *testing.T) {
	root := repoRoot(t)
	roots := []string{
		"internal/app/build.go",
		"internal/app/composition.go",
		"internal/app/postgres_build.go",
		"internal/app/runtimefactory/postgres.go",
	}
	forbiddenCalls := map[string]struct{}{
		"NewSQLiteSealedFactory":      {},
		"NewSQLiteSealedRootResolver": {},
		"RunSQLiteGC":                 {},
		"OpenReadOnlyCatalog":         {},
	}
	for _, relative := range roots {
		path := filepath.Join(root, filepath.FromSlash(relative))
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath == "database/sql" || strings.Contains(importPath, "/sqlite") {
				t.Errorf("%s imports local persistence implementation %q", relative, importPath)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch function := call.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			if _, forbidden := forbiddenCalls[name]; forbidden {
				t.Errorf("%s reaches local/file-catalog constructor %s", relative, name)
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			selector, ok := literal.Type.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "FileCatalog" {
				t.Errorf("%s constructs a file-backed catalog", relative)
			}
			return true
		})
	}

	localCompositionPath := filepath.Join(root, "internal/app/local_composition.go")
	if _, err := os.Stat(localCompositionPath); err == nil {
		t.Fatal("local SQLite application composition must not exist")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat removed local SQLite composition: %v", err)
	}
	buildSource, err := os.ReadFile(filepath.Join(root, "internal/app/build.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(buildSource), "assembleLocalSQLite(") {
		t.Fatal("Build retains a local SQLite authority graph")
	}
	compositionSource, err := os.ReadFile(filepath.Join(root, "internal/app/composition.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"localruntimefactory", "NewSQLiteSealedFactory("} {
		if strings.Contains(string(compositionSource), forbidden) {
			t.Fatalf("production app composition retains local authority %q", forbidden)
		}
	}
}

// TestLEA414ProductionUsesSealedCanonicalPath keeps the cutover boundary
// explicit. Production composition must select the target-owned PostgreSQL
// sealed delivery factory, with no local SQLite fallback in the app graph.
func TestLEA414ProductionUsesSealedCanonicalPath(t *testing.T) {
	root := repoRoot(t)
	productionBuildBytes, err := os.ReadFile(filepath.Join(root, "internal/app/build.go"))
	if err != nil {
		t.Fatal(err)
	}
	productionBuild := string(productionBuildBytes)
	for _, required := range []string{"BuildProduction", "buildPostgresTarget"} {
		if !strings.Contains(productionBuild, required) {
			t.Errorf("production entrypoint missing FAI-575 gate %q", required)
		}
	}
	if strings.Contains(productionBuild, "NewSQLiteSealedFactory") {
		t.Error("production entrypoint selects the legacy NewSQLiteSealedFactory")
	}

	postgresBuildBytes, err := os.ReadFile(filepath.Join(root, "internal/app/postgres_build.go"))
	if err != nil {
		t.Fatal(err)
	}
	postgresBuild := string(postgresBuildBytes)
	normalizedPostgresBuild := strings.Join(strings.Fields(postgresBuild), " ")
	for _, required := range []string{"NewPostgresSealedFactory", "RequireSealedCatalog: true", "ResolveSealedActiveState", "NativeDeliveryMutations: nativeDelivery", "NativeDeliveryReader:    nativeDeliveryReader"} {
		if !strings.Contains(normalizedPostgresBuild, strings.Join(strings.Fields(required), " ")) {
			t.Errorf("native PostgreSQL composition missing FAI-575 target contract %q", required)
		}
	}
	if strings.Contains(postgresBuild, "NewSQLiteSealedFactory") {
		t.Error("native PostgreSQL composition selects the legacy NewSQLiteSealedFactory")
	}

	syncBytes, err := os.ReadFile(filepath.Join(root, "internal/deployment/module/candidate_sync.go"))
	if err != nil {
		t.Fatal(err)
	}
	syncSource := string(syncBytes)
	for _, required := range []string{
		"func (m *Module) RetainProjectCandidateSource",
		"request.SourceOnly = true",
		"m.candidateSources.Commit(",
	} {
		if !strings.Contains(syncSource, required) {
			t.Errorf("source retention path is missing canonical evidence %q", required)
		}
	}
	if strings.Contains(syncSource, "m.candidates.Start(") || strings.Contains(syncSource, "m.deliveryCandidateBuilder") {
		t.Fatal("source synchronization path retains a candidate-construction fallback")
	}

}
