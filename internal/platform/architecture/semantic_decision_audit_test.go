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

// Every production protected-consumer constructor must explicitly attach the
// existing observer. Route adapters delegate to these owners; they must not
// create a second, unaudited execution consumer. Pure compiler planners are
// deliberately not consumers and remain outside this inventory.
func TestSemanticDecisionAuditCoversEveryProductionConsumerConstructor(t *testing.T) {
	owners := map[string]int{
		"internal/analytics/materialize/semantic_consumer.go":  1,
		"internal/dashboard/queryauthz/semantic_discovery.go":  2,
		"internal/project/module/semantic_catalog.go":          1,
		"internal/project/http/data_explorer_authorization.go": 1,
	}
	seen := make(map[string]int)
	for _, file := range productionGoFiles(t) {
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch function := call.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			if name != "NewSemanticAccessConsumer" {
				return true
			}
			seen[file.path]++
			if owners[file.path] == 0 {
				t.Errorf("%s introduces an unqualified semantic decision consumer", file.path)
			}
			if len(call.Args) != 5 || call.Ellipsis.IsValid() {
				t.Errorf("%s must bind exactly one explicit decision observer", file.path)
			} else if observer, ok := call.Args[4].(*ast.Ident); ok && observer.Name == "nil" {
				t.Errorf("%s supplies a nil decision observer", file.path)
			}
			return true
		})
	}
	for owner, count := range owners {
		if seen[owner] != count {
			t.Errorf("%s has %d consumer constructors, want reviewed inventory %d", owner, seen[owner], count)
		}
	}
}
