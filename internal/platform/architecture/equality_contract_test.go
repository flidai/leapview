package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fai907EqualityInventory is the reviewed inventory of helpers whose contract
// is ordered, element-wise equality. Helpers that sort before comparing keep
// that normalization in their bodies; the final comparison is still required
// to use slices.Equal.
var fai907EqualityInventory = map[string][]string{
	"internal/project/bundle/bundle.go":                    {"equalStringSlices"},
	"internal/project/compiler/project_build.go":           {"sameOrderedFields", "sameStringList"},
	"internal/project/contracts/generate/main.go":          {"sameStrings"},
	"internal/refresh/module/postgres_persistence.go":      {"sameStringSlice"},
	"internal/refresh/postgres/repository.go":              {"slicesEqual"},
	"internal/refresh/sqlite/runs.go":                      {"sameStrings"},
	"internal/analytics/query/aggregate_plan_ir.go":        {"sameStringSlice"},
	"internal/analytics/query/semantic_access_planner.go":  {"sameSemanticAccessRoute"},
	"internal/analytics/query/planir/graph_validation.go":  {"sameJoinKeys", "sameOrdered", "sameFields", "sameMetrics"},
	"internal/analytics/query/planir/types.go":             {"equal"},
	"internal/analytics/model/model.go":                    {"sameStringSet"},
	"internal/analytics/model/ossie/adapter.go":            {"sameFields"},
	"internal/analytics/duckdb/read_planner.go":            {"sameStringSet"},
	"internal/analytics/ducklake/postgres/repository.go":   {"sameMarkerQuarantine"},
	"internal/project/catalog/catalog.go":                  {"sameKinds"},
	"internal/app/securitypolicy/policy.go":                {"compareStrings"},
	"internal/app/tools/securitydependencies/evidence.go":  {"equalStringSlices"},
	"internal/app/tools/securitydependencies/main_test.go": {"equalStrings"},
	"internal/recoveryset/recoveryset.go":                  {"equalPoints", "equalRoots"},
	"internal/recoveryset/successor/set.go":                {"sameOwnerProjection"},
	"internal/refresh/openlineage/facets.go":               {"sameLineage"},
	"pkg/workload/workload_fairness.go":                    {"sameAdmission"},
	"internal/manageddata/cli/data_plan_test.go":           {"equalDataPlanFiles"},
	"internal/manageddata/localplan/service_test.go":       {"equalFiles", "equalStrings"},
	"internal/analytics/model/registry_test.go":            {"equalStrings"},
	"internal/analytics/connectors/registry_test.go":       {"equalStrings"},
	"internal/dashboard/http/arrow_contract_test.go":       {"equalStrings"},
}

// sameOwnerProjection intentionally retains a field-by-field object-root
// comparison: provider metadata outside the selected ownership projection is
// deliberately ignored. Its ClusterPoints comparison is still element-wise.
var fai907StructuralEqualityExceptions = map[string]map[string]string{
	"internal/recoveryset/successor/set.go": {
		"sameOwnerProjection": "ObjectRoots compares only the ownership projection; the remaining loop is not whole-element equality",
	},
}

func TestFAI907EqualityInventoryUsesSlicesEqual(t *testing.T) {
	root := repoRoot(t)
	for relativePath, names := range fai907EqualityInventory {
		relativePath, names := relativePath, names
		t.Run(relativePath, func(t *testing.T) {
			path := filepath.Join(root, filepath.FromSlash(relativePath))
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", relativePath, err)
			}
			for _, name := range names {
				found := false
				manualLoop := false
				ast.Inspect(file, func(node ast.Node) bool {
					declaration, ok := node.(*ast.FuncDecl)
					if !ok || declaration.Name.Name != name || declaration.Body == nil {
						return true
					}
					ast.Inspect(declaration.Body, func(node ast.Node) bool {
						switch loop := node.(type) {
						case *ast.RangeStmt:
							manualLoop = manualLoop || hasDirectIndexedEqualityLoop(loop.Body)
						case *ast.ForStmt:
							manualLoop = manualLoop || hasDirectIndexedEqualityLoop(loop.Body)
						}
						call, ok := node.(*ast.CallExpr)
						if !ok {
							return true
						}
						selector, ok := call.Fun.(*ast.SelectorExpr)
						if ok && selector.Sel.Name == "Equal" {
							if packageName, ok := selector.X.(*ast.Ident); ok && packageName.Name == "slices" {
								found = true
							}
						}
						return true
					})
					return false
				})
				if !found {
					t.Errorf("%s.%s must use slices.Equal for its final element-wise comparison", relativePath, name)
				}
				if manualLoop {
					if reason := fai907StructuralEqualityExceptions[relativePath][name]; reason == "" {
						t.Errorf("%s.%s retains a direct indexed equality loop; use slices.Equal or document a structural exception", relativePath, name)
					}
				}
			}
		})
	}
}

func TestFAI907HasOneTypedNilReflectionBoundary(t *testing.T) {
	root := repoRoot(t)
	helperPath := filepath.Join(root, "internal", "platform", "typednil", "typednil.go")
	for _, relativeRoot := range []string{"internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, relativeRoot), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Clean(path) == filepath.Clean(helperPath) || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(body)
			if containsEveryReflectNilKind(text) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				t.Errorf("%s duplicates the typed-nil reflection boundary; use internal/platform/typednil", filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s for duplicate typed-nil helpers: %v", relativeRoot, err)
		}
	}
}

func containsEveryReflectNilKind(text string) bool {
	for _, kind := range []string{"Chan", "Func", "Interface", "Map", "Slice", "UnsafePointer"} {
		if !strings.Contains(text, "reflect."+kind) {
			return false
		}
	}
	return strings.Contains(text, "reflect.Pointer") || strings.Contains(text, "reflect.Ptr")
}

func hasDirectIndexedEqualityLoop(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		binary, ok := node.(*ast.BinaryExpr)
		if !ok || (binary.Op != token.EQL && binary.Op != token.NEQ) {
			return true
		}
		left, leftOK := binary.X.(*ast.IndexExpr)
		right, rightOK := binary.Y.(*ast.IndexExpr)
		if !leftOK || !rightOK {
			return true
		}
		leftIndex, leftOK := left.Index.(*ast.Ident)
		rightIndex, rightOK := right.Index.(*ast.Ident)
		if leftOK && rightOK && leftIndex.Name == rightIndex.Name {
			found = true
		}
		return true
	})
	return found
}
