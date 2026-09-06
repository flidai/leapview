package query

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSemanticAccessCompilerReusesExistingAuthorities(t *testing.T) {
	_, sourceFile, _, _ := runtime.Caller(0)
	source, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "semantic_access.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{
		`"crypto/`,
		`"database/sql"`,
		`"text/template"`,
		`"html/template"`,
		`internal/access/policy`,
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("semantic access compiler introduces a parallel authority through %q", forbidden)
		}
	}
	if got := strings.Count(text, "type CompiledSemanticAccessPolicy struct"); got != 1 {
		t.Errorf("compiled semantic access policy definitions = %d, want exactly one", got)
	}
	if got := strings.Count(text, "func (policy *CompiledSemanticAccessPolicy) Evaluate("); got != 1 {
		t.Errorf("unified semantic access evaluators = %d, want exactly one", got)
	}
}

func TestSemanticAccessPlannerReusesTypedPolicyAndRenderer(t *testing.T) {
	_, sourceFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(sourceFile)
	source, err := os.ReadFile(filepath.Join(root, "security_plan.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{`"crypto/`, `"database/sql"`, `"text/template"`, `"html/template"`, `SELECT `, ` WHERE `, `isAdmin`, `ServicePrincipal`, `type Predicate struct`, `CanonicalSemanticAttributeValues`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("planner security introduces an escape hatch or parallel authority: %q", forbidden)
		}
	}
	for _, required := range []string{"policy.Evaluate(", "planir.NewRoutedSecurityBarrier(", "g.SealSecurity()"} {
		if !strings.Contains(text, required) {
			t.Errorf("missing existing authority integration: %q", required)
		}
	}
	for _, file := range []string{"planner.go", "multi_dataset.go", "bundle.go", "spatial_plan_ir.go"} {
		source, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(source), "p.securePlanGraph(") {
			t.Errorf("source-producing planner %s has no security placement boundary", file)
		}
	}
}
