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
