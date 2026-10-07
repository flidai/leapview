package app

import (
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

var semanticAttributePlatformOperations = []string{
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
	for _, operationID := range semanticAttributePlatformOperations {
		t.Run(operationID, func(t *testing.T) {
			contract, ok := contracts[operationID]
			if !ok {
				t.Fatalf("Access generated operations missing %q", operationID)
			}
			authz, ok := contract.Extensions["x-authz"].(map[string]any)
			if !ok {
				t.Fatalf("%s missing generated x-authz extension: %#v", operationID, contract.Extensions["x-authz"])
			}
			if got := authz["mode"]; got != "privilege" {
				t.Fatalf("%s x-authz mode = %#v, want privilege", operationID, got)
			}
			if got := authz["privilege"]; got != "PLATFORM_ADMIN" {
				t.Fatalf("%s privilege = %#v, want PLATFORM_ADMIN", operationID, got)
			}
			wantAction := "platform.access.manage"
			if operationID == "listSemanticAttributeDefinitions" || operationID == "getSemanticAttributeDefinition" || operationID == "listPrincipalSemanticAttributeAssignments" || operationID == "listGroupSemanticAttributeAssignments" || operationID == "listSemanticAttributeClaimMappings" || operationID == "previewSemanticAttributeImpact" {
				wantAction = "platform.access.read"
			}
			if got := authz["action"]; got != wantAction {
				t.Fatalf("%s action = %#v, want %s", operationID, got, wantAction)
			}
			if got := authz["resolver"]; got != "instance" {
				t.Fatalf("%s resolver = %#v, want instance", operationID, got)
			}
			if got := contract.Extensions["x-leapview-object-scope"]; got != "instance" {
				t.Fatalf("%s object scope = %#v, want instance", operationID, got)
			}
		})
	}
}
