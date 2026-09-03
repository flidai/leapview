package app

import (
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

var semanticAttributeAuthenticatedOperations = []string{
	"listSemanticAttributeDefinitions",
	"registerSemanticAttribute",
	"getSemanticAttributeDefinition",
	"updateSemanticAttributeMetadata",
	"disableSemanticAttribute",
	"restoreSemanticAttribute",
	"listPrincipalSemanticAttributeAssignments",
	"upsertPrincipalSemanticAttributeAssignment",
	"removePrincipalSemanticAttributeAssignment",
	"listGroupSemanticAttributeAssignments",
	"upsertGroupSemanticAttributeAssignment",
	"removeGroupSemanticAttributeAssignment",
	"listSemanticAttributeClaimMappings",
	"upsertSemanticAttributeClaimMapping",
	"removeSemanticAttributeClaimMapping",
	"previewSemanticAttributeImpact",
}

func TestSemanticAttributeAPIGenOperationContracts(t *testing.T) {
	contracts := accessgen.GetAPIGenOperationContracts()
	for _, operationID := range semanticAttributeAuthenticatedOperations {
		t.Run(operationID, func(t *testing.T) {
			contract, ok := contracts[operationID]
			if !ok {
				t.Fatalf("Access generated operations missing %q", operationID)
			}
			authz, ok := contract.Extensions["x-authz"].(map[string]any)
			if !ok {
				t.Fatalf("%s missing generated x-authz extension: %#v", operationID, contract.Extensions["x-authz"])
			}
			if got := authz["mode"]; got != "authenticated" {
				t.Fatalf("%s x-authz mode = %#v, want authenticated", operationID, got)
			}
			if got := contract.Extensions["x-leapview-object-scope"]; got != "platform" {
				t.Fatalf("%s object scope = %#v, want platform", operationID, got)
			}
		})
	}
}
