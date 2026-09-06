package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FAI-639 owns portable policy lowering, target qualification, and typed
// evaluation. Planner placement remains a separate FAI-641 boundary, so this
// guard keeps executable SQL/templates and early consumer wiring out of the
// compiler slice.
func TestSemanticAccessCompilerBoundaryRemainsClosedAndUnwired(t *testing.T) {
	root := repoRoot(t)
	compiler := readArchitectureFixture(t, root, "internal/analytics/query/semantic_access_compile.go")
	evaluator := readArchitectureFixture(t, root, "internal/analytics/query/semantic_access_evaluate.go")
	lowering := readArchitectureFixture(t, root, "internal/project/compiler/data_resources.go")
	model := readArchitectureFixture(t, root, "internal/analytics/model/types.go")

	for fragment, body := range map[string]string{
		"portable policy retained on Model":    model,
		"portable generated-contract lowering": lowering,
		"target-qualified policy compiler":     compiler,
		"typed PlanIR predicate evaluator":     evaluator,
	} {
		if !strings.Contains(body, map[string]string{
			"portable policy retained on Model":    "AccessPolicy",
			"portable generated-contract lowering": "lowerSemanticAccessPolicy",
			"target-qualified policy compiler":     "CompileSemanticAccessPolicy",
			"typed PlanIR predicate evaluator":     "planir.Predicate",
		}[fragment]) {
			t.Errorf("semantic access boundary is missing %s", fragment)
		}
	}
	for _, forbidden := range []string{"database/sql", "internal/analytics/duckdb", "text/template", "html/template"} {
		if strings.Contains(compiler, forbidden) || strings.Contains(evaluator, forbidden) {
			t.Errorf("semantic access compiler/evaluator acquired executable target dependency %q", forbidden)
		}
	}

	var compileCalls, evaluateCalls int
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		compileCalls += strings.Count(string(body), "CompileSemanticAccessPolicy(")
		evaluateCalls += strings.Count(string(body), "EvaluateSemanticAccess(")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if compileCalls != 1 || evaluateCalls != 1 {
		t.Fatalf("FAI-639 boundary is wired before FAI-641: compile declarations/calls=%d evaluation declarations/calls=%d", compileCalls, evaluateCalls)
	}
}
