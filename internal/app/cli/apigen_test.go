package cli

import (
	"testing"

	cligen "github.com/flidai/leapview/internal/app/cli/gen"
)

func TestAPIGenOperationURLUsesGeneratedContracts(t *testing.T) {
	u, err := apiOperationURL("https://leapview.example/", "rollbackDeliveryGeneration", map[string]string{"project": "sales project", "generation": "generation 1"}, nil)
	if err != nil {
		t.Fatalf("operation URL: %v", err)
	}
	if u != "https://leapview.example/api/v1/projects/sales%20project/delivery/generations/generation%201/rollback" {
		t.Fatalf("url = %q", u)
	}

	u, err = apiOperationURL("https://leapview.example", "finalizeRelease", map[string]string{"project": "demo project", "release": "release 1"}, nil)
	if err != nil {
		t.Fatalf("operation URL: %v", err)
	}
	if u != "https://leapview.example/api/v1/projects/demo%20project/releases/release%201/finalize" {
		t.Fatalf("url = %q", u)
	}

	u, err = apiOperationURL("https://leapview.example", "queryDashboardPage", map[string]string{"dashboard": "sales dash", "page": "overview"}, nil)
	if err != nil {
		t.Fatalf("operation URL: %v", err)
	}
	if u != "https://leapview.example/api/v1/dashboards/sales%20dash/pages/overview/query" {
		t.Fatalf("url = %q", u)
	}
}

func TestGeneratedCLIRegistryContainsCoreCommands(t *testing.T) {
	commands := map[string]bool{}
	for _, spec := range cligen.APIGeneratedCommandSpecs {
		commands[spec.OperationID] = true
	}
	for _, operationID := range []string{"listAgentConversations", "listDashboards", "getDashboard", "queryDashboardPage", "queryDashboardVisualData", "listSemanticModels", "getSemanticModel"} {
		if !commands[operationID] {
			t.Fatalf("generated CLI registry missing %s", operationID)
		}
	}
}

func TestGeneratedVisualCLIUsesUnionCollectionMetadata(t *testing.T) {
	for _, spec := range cligen.APIGeneratedCommandSpecs {
		if spec.OperationID != "queryDashboardVisualData" {
			continue
		}
		if spec.Output.Mode != "raw" || spec.Pagination != nil {
			t.Fatalf("visual CLI metadata = output %#v pagination %#v, want raw union output", spec.Output, spec.Pagination)
		}
		return
	}
	t.Fatal("queryDashboardVisualData CLI metadata missing")
}
