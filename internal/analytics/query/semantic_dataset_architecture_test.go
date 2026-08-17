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
	for _, relative := range files {
		matches, err := filepath.Glob(filepath.Join(root, relative, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range matches {
			if strings.HasSuffix(path, "_test.go") || physicalBoundary[filepath.ToSlash(filepath.Join(relative, filepath.Base(path)))] {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			semanticRead := strings.Contains(text, "model.Tables") || strings.Contains(text, "model.Datasets") || strings.Contains(text, "semantic.Tables") || strings.Contains(text, "semantic.Datasets") || strings.Contains(text, "p.model.Tables") || strings.Contains(text, "p.model.Datasets") || strings.Contains(text, "r.model.Tables") || strings.Contains(text, "r.model.Datasets")
			if semanticRead {
				t.Errorf("%s reads semantic model Tables/Datasets directly; use CompiledDataset accessors", filepath.ToSlash(filepath.Join(relative, filepath.Base(path))))
			}
		}
	}
}
