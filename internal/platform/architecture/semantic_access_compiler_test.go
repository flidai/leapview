package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FAI-639 owns portable policy lowering, target qualification, and typed
// evaluation. FAI-641 may consume that handoff only inside the planner;
// FAI-642 owns the shared query consumer composition and discovery boundary.
func TestSemanticAccessCompilerBoundaryRemainsClosedToConsumers(t *testing.T) {
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

	var compileCalls, evaluateCalls, admissionCalls int
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
		compileCount := strings.Count(string(body), "CompileSemanticAccessPolicy(")
		evaluateCount := strings.Count(string(body), "EvaluateSemanticAccess(")
		admissionCalls += strings.Count(string(body), "WithSemanticAccess(")
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch filepath.ToSlash(relative) {
		case "internal/analytics/query/semantic_access_compile.go", "internal/analytics/query/semantic_access_evaluate.go", "internal/analytics/query/semantic_access_planner.go", "internal/analytics/query/semantic_consumer.go":
		default:
			if compileCount != 0 || evaluateCount != 0 {
				t.Errorf("semantic access bypasses the planner handoff in %s", relative)
			}
		}
		compileCalls += compileCount
		evaluateCalls += evaluateCount
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if compileCalls != 4 || evaluateCalls != 3 {
		t.Fatalf("expected compiler calls only from its activation facade, planner, and shared consumer: compile=%d evaluate=%d", compileCalls, evaluateCalls)
	}
	if admissionCalls != 1 {
		t.Fatalf("expected planner admission declaration only; consumer retains the same private planner policy/provider: declarations/calls=%d", admissionCalls)
	}
}
