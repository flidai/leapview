package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Pure evidence helpers preserve historical derivation but do not resolve
// live Access authority. New publication must enter the transactional adapter.
func TestPolicyEvidenceDerivationRemainsBehindPublicationAdmission(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.path == "internal/project/identityledger/policy_evidence.go" || file.path == "internal/project/identityledger/postgres/policy_evidence.go" {
			continue
		}
		if !strings.Contains(file.body, "DerivePolicyEvidence") && !strings.Contains(file.body, "NewPolicyEvidence") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if ok && (identifier.Name == "DerivePolicyEvidence" || identifier.Name == "NewPolicyEvidence") {
				t.Errorf("%s references pure %s outside the publication admission owner", file.path, identifier.Name)
			}
			return true
		})
	}
}
