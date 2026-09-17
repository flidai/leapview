package module

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestBuildAPIGenOperationCoverageIsDeterministicAndExplicit(t *testing.T) {
	operations := map[string]APIGenOperationContract{
		"queryModel": {
			OperationID: "queryModel", Method: "GET", Path: "/api/v1/projects/{project}/models/{model}",
			Protected: true, AuthzMode: "privilege", Action: string(access.ActionSemanticQuery), Resolver: string(access.TypedOperationResolverSemanticModel),
			Extensions: map[string]any{apiGenObjectScopeExtension: "semantic-model"},
		},
		"legacyRoute": {
			OperationID: "legacyRoute", Method: "GET", Path: "/api/v1/projects/{project}/legacy",
			Protected: true, AuthzMode: "privilege", Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
		},
		"partial": {
			OperationID: "partial", Method: "GET", Path: "/api/v1/projects/{project}/partial",
			Protected: true, AuthzMode: "privilege", Action: string(access.ActionProjectSettingsRead),
			Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
		},
	}
	matrix := BuildAPIGenOperationCoverage(operations)
	if len(matrix.Operations) != 3 || matrix.Operations[0].OperationID != "legacyRoute" || matrix.Operations[1].OperationID != "partial" {
		t.Fatalf("operations are not sorted: %#v", matrix.Operations)
	}
	rows := make(map[string]APIGenOperationCoverage, len(matrix.Operations))
	for _, row := range matrix.Operations {
		rows[row.OperationID] = row
	}
	if row := rows["queryModel"]; row.SupportStatus != APIGenOperationSupported || row.Qualification != APIGenOperationMapped || row.LegacyMode != "typed-attenuated" || len(row.Dependencies) != 1 || row.Dependencies[0] != string(access.ActionSemanticConsume) {
		t.Fatalf("typed row = %#v", row)
	}
	if row := rows["legacyRoute"]; row.SupportStatus != APIGenOperationLegacyOnly || row.Qualification != APIGenOperationLegacy {
		t.Fatalf("legacy row = %#v", row)
	}
	if row := rows["partial"]; row.SupportStatus != APIGenOperationUnsupported || row.Qualification != APIGenOperationUnqualified || row.Reason == "" {
		t.Fatalf("partial row = %#v", row)
	}
	valid := BuildAPIGenOperationCoverage(map[string]APIGenOperationContract{
		"queryModel": operations["queryModel"], "legacyRoute": operations["legacyRoute"],
	})
	if err := ValidateAPIGenOperationCoverage(valid); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAPIGenOperationCoverage(matrix); err == nil {
		t.Fatal("matrix with ambiguous typed metadata unexpectedly validated")
	}
}

func TestBuildAPIGenOperationCoverageRejectsResolverScopeMismatch(t *testing.T) {
	matrix := BuildAPIGenOperationCoverage(map[string]APIGenOperationContract{
		"wrongScope": {
			OperationID: "wrongScope", Method: "GET", Path: "/api/v1/projects/{project}/dashboards/{dashboard}",
			Protected: true, AuthzMode: "privilege", Action: string(access.ActionDashboardRead), Resolver: string(access.TypedOperationResolverDashboard),
			Extensions: map[string]any{apiGenObjectScopeExtension: "project"},
		},
	})
	row := matrix.Operations[0]
	if row.SupportStatus != APIGenOperationUnsupported || row.Reason == "" {
		t.Fatalf("row = %#v", row)
	}
	if err := ValidateAPIGenOperationCoverage(matrix); err != nil {
		t.Fatal(err)
	}
}
