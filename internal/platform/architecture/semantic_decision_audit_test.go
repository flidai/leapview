package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestSemanticDecisionAuditReusesConsumerAndDigestAuthorities(t *testing.T) {
	boundaries := map[string]bool{
		"internal/analytics/materialize/semantic_consumer.go":  false,
		"internal/dashboard/queryauthz/semantic_audit.go":      false,
		"internal/project/module/semantic_catalog.go":          false,
		"internal/project/http/data_explorer_authorization.go": false,
	}
	for _, file := range productionGoFiles(t) {
		_, boundary := boundaries[file.path]
		auditFile := strings.Contains(file.path, "semantic_audit.go") || strings.Contains(file.path, "semantic_decision_audit.go")
		if !boundary && !auditFile {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(path, "crypto/") || strings.Contains(path, "canonicalization") {
				t.Errorf("%s introduces independent audit digest/canonicalization dependency %s", file.path, path)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if boundary && selector.Sel.Name == "NewSemanticAuditObserver" {
				boundaries[file.path] = true
			}
			if boundary && selector.Sel.Name == "Allows" {
				if owner, ok := selector.X.(*ast.Ident); ok && owner.Name == "policy" {
					t.Errorf("%s bypasses decision observation with direct policy.Allows", file.path)
				}
			}
			return true
		})
	}
	for file, wired := range boundaries {
		if !wired {
			t.Errorf("%s does not use the shared decision audit projection", file)
		}
	}
}
