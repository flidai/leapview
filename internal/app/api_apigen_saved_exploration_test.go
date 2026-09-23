package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/analytics/api/gen"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
)

const expectedAPIGenAggregateOperationCount = 219

var savedExplorationOperationIDs = map[string]struct{}{
	"archiveSavedExploration":   {},
	"createSavedExploration":    {},
	"duplicateSavedExploration": {},
	"getSavedExploration":       {},
	"listSavedExplorations":     {},
	"updateSavedExploration":    {},
}

func TestAPIGenSavedExplorationOperationSurface(t *testing.T) {
	contracts := gen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 17; got != want {
		t.Fatalf("Analytics generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID := range savedExplorationOperationIDs {
		contract, ok := contracts[operationID]
		if !ok {
			t.Fatalf("saved-exploration operation %q is missing from generated package", operationID)
		}
		if len(contract.Tags) != 1 || contract.Tags[0] != "Saved Explorations" {
			t.Errorf("%s tags = %v, want [Saved Explorations]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Analytics-owned %s is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenSavedExplorationNamespaces(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}
	var document struct {
		Endpoints []struct {
			OperationID string   `json:"operation_id"`
			Namespace   string   `json:"namespace"`
			Tags        []string `json:"tags"`
		} `json:"endpoints"`
		Schemas map[string]struct {
			Namespace string `json:"namespace"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}
	for _, endpoint := range document.Endpoints {
		if _, ok := savedExplorationOperationIDs[endpoint.OperationID]; !ok {
			continue
		}
		if len(endpoint.Tags) != 1 || endpoint.Tags[0] != "Saved Explorations" {
			t.Errorf("endpoint %q tags = %v, want [Saved Explorations]", endpoint.OperationID, endpoint.Tags)
		}
		if endpoint.Namespace != "LeapViewAPI.Analytics" {
			t.Errorf("endpoint %q namespace = %q, want LeapViewAPI.Analytics", endpoint.OperationID, endpoint.Namespace)
		}
	}
	schema, ok := document.Schemas["ExplorationSpec"]
	if !ok || schema.Namespace != "LeapViewExploration" {
		t.Fatalf("APIGen IR ExplorationSpec namespace = %#v, want LeapViewExploration", schema)
	}
}

func TestAPIGenSavedExplorationUISignalContracts(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "api", "gen", "ui-signals-ir.json"))
	if err != nil {
		t.Fatalf("read UI signal contract IR: %v", err)
	}
	var document struct {
		Contracts []struct {
			Name       string         `json:"name"`
			Kind       string         `json:"kind"`
			Extensions map[string]any `json:"extensions"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode UI signal contract IR: %v", err)
	}
	if len(document.Contracts) != 134 {
		t.Fatalf("UI signal IR contracts = %d, want 134", len(document.Contracts))
	}
	wantRoles := map[string]string{
		"SavedExplorationCommandSignal":   "command",
		"SavedExplorationCurrentSignal":   "signal",
		"SavedExplorationListItemSignal":  "signal",
		"SavedExplorationListSignal":      "signal",
		"SavedExplorationRevisionSignal":  "signal",
		"SavedExplorationSaveStateSignal": "signal",
		"SavedExplorationStateSignal":     "signal",
	}
	found := make(map[string]bool, len(wantRoles))
	for _, contract := range document.Contracts {
		role, ok := wantRoles[contract.Name]
		if !ok {
			continue
		}
		found[contract.Name] = true
		if contract.Kind != "ui-signal" {
			t.Errorf("%s kind = %q, want ui-signal", contract.Name, contract.Kind)
		}
		if contract.Extensions["x-leapview-contract-role"] != role {
			t.Errorf("%s contract role = %v, want %q", contract.Name, contract.Extensions["x-leapview-contract-role"], role)
		}
		if contract.Extensions["x-leapview-surface"] != "saved_explorations" {
			t.Errorf("%s surface = %v, want saved_explorations", contract.Name, contract.Extensions["x-leapview-surface"])
		}
	}
	for name := range wantRoles {
		if !found[name] {
			t.Errorf("UI signals do not emit saved-exploration contract %s", name)
		}
	}
}

func TestAPIGenSavedExplorationRoutesAndAuthz(t *testing.T) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatalf("embedded openapi: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi paths missing: %#v", spec["paths"])
	}
	for _, path := range []string{
		"/api/v1/projects/{project}/saved-explorations",
		"/api/v1/projects/{project}/saved-explorations/{exploration}",
		"/api/v1/projects/{project}/saved-explorations/{exploration}/archive",
		"/api/v1/projects/{project}/saved-explorations/{exploration}/duplicate",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("generated OpenAPI missing saved-exploration path %s", path)
		}
	}

	contracts := apiaggregate.GetAPIGenOperationContracts()
	for operationID := range savedExplorationOperationIDs {
		contract, ok := contracts[operationID]
		if !ok {
			t.Fatalf("generated operation %q is missing", operationID)
		}
		authz, ok := contract.Extensions["x-authz"].(map[string]any)
		if !ok || authz["mode"] != "authenticated" {
			t.Errorf("%s x-authz = %#v, want authenticated", operationID, contract.Extensions["x-authz"])
		}
	}
}
