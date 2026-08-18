package query

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSemanticDatasetRuntimeDoesNotDualRead guards the serving boundary that
// keeps semantic aliases in CompiledDataset. Authoring/compiler code and
// explicitly physical project-model helpers are intentionally outside this
// check; query/runtime/API projections must resolve aliases through compiled
// accessors instead.
func TestSemanticDatasetRuntimeDoesNotDualRead(t *testing.T) {
	_, sourceFile, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	files := []string{
		"internal/analytics/query",
		"internal/analytics/materialize",
		"internal/analytics/duckdb",
		"internal/refresh/plan",
		"internal/project/http",
		"internal/dashboard/semanticapi",
	}
	physicalBoundary := map[string]bool{
		"internal/analytics/query/compiled.go":          true,
		"internal/analytics/materialize/materialize.go": true,
		"internal/analytics/materialize/runtime.go":     true, // ModelTableQuery boundary
		"internal/analytics/duckdb/materialize.go":      true,
		"internal/analytics/duckdb/read_planner.go":     true,
		"internal/analytics/duckdb/schema.go":           true,
	}
	// Verification intentionally receives the authored model as a deployment
	// validation input. It is not request-time planner execution and therefore
	// remains outside this serving-boundary scan.
	verificationBoundary := map[string]bool{"internal/analytics/query/verification.go": true}
	for _, guarded := range []string{"internal/analytics/query/resolver.go", "internal/analytics/query/compiled.go"} {
		path := filepath.Join(root, guarded)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, typeName := range []string{"type Planner struct", "type CompiledModel struct"} {
			start := strings.Index(text, typeName)
			if start < 0 {
				continue
			}
			end := strings.Index(text[start:], "}")
			if end >= 0 && strings.Contains(text[start:start+end], "*semanticmodel.Model") {
				t.Errorf("%s retains a semanticmodel.Model field in %s; planners must own compiled facts only", typeName, guarded)
			}
		}
	}
	for _, relative := range files {
		matches, err := filepath.Glob(filepath.Join(root, relative, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range matches {
			fileKey := filepath.ToSlash(filepath.Join(relative, filepath.Base(path)))
			if strings.HasSuffix(path, "_test.go") || physicalBoundary[fileKey] || verificationBoundary[fileKey] {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			semanticRead := strings.Contains(text, "model.Tables") || strings.Contains(text, "model.Datasets") || strings.Contains(text, "semantic.Tables") || strings.Contains(text, "semantic.Datasets") || strings.Contains(text, "p.model.Tables") || strings.Contains(text, "p.model.Datasets") || strings.Contains(text, "p.model.Dimensions") || strings.Contains(text, "p.model.Relationships") || strings.Contains(text, "p.model.Filters") || strings.Contains(text, "p.model.Metrics") || strings.Contains(text, "r.model.Tables") || strings.Contains(text, "r.model.Datasets") || strings.Contains(text, "r.model.Dimensions") || strings.Contains(text, "r.model.Relationships") || strings.Contains(text, "r.model.Filters") || strings.Contains(text, "r.model.Metrics")
			if semanticRead {
				t.Errorf("%s reads semantic authoring maps (Tables, Datasets, Dimensions, Relationships, Filters, or Metrics) directly; use activation-owned compiled facts", filepath.ToSlash(filepath.Join(relative, filepath.Base(path))))
			}
		}
	}
}
