package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestFunctionComplexityCountsDecisionPointsWithoutNestedFunctions(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
func example(values []bool) bool {
	for _, value := range values {
		if value && len(values) > 1 { return true }
	}
	nested := func() bool { if true { return true }; return false }
	return nested()
}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	if got := functionComplexity(function.Body); got != 4 {
		t.Fatalf("complexity = %d, want 4", got)
	}
}

func TestParseOwnershipLogRanksChangesThenAuthors(t *testing.T) {
	log := strings.Join([]string{
		"commit:one\tAlice", "internal/app/composition.go", "web/app.ts", "",
		"commit:two\tBob", "internal/app/composition.go", "",
		"commit:three\tAlice", "docs/readme.md", "web/app.ts", "",
	}, "\n")
	records, commits := parseOwnershipLog(log)
	if commits != 3 {
		t.Fatalf("commits = %d, want 3", commits)
	}
	if len(records) != 2 || records[0].Path != "internal/app/composition.go" || records[0].Changes != 2 || len(records[0].Authors) != 2 {
		t.Fatalf("ownership records = %#v", records)
	}
}

func TestAuthoredSourceExcludesGeneratedAndDependencyTrees(t *testing.T) {
	for _, path := range []string{"web/generated/contracts.ts", "vendor/example.go", "web/types.d.ts", "docs/readme.md"} {
		if authoredSource(path) {
			t.Fatalf("authoredSource(%q) = true", path)
		}
	}
	if !authoredSource("internal/app/composition.go") || !authoredSource("web/components/app.ts") {
		t.Fatal("authored source was excluded")
	}
}
