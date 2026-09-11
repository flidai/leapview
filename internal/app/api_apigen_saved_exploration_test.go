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

var savedExplorationOperationIDs = map[string]struct{}{
	"archiveSavedExploration":   {},
	"createSavedExploration":    {},
	"duplicateSavedExploration": {},
	"exportSavedExploration":    {},
	"exportSavedExplorationURL": {},
	"getSavedExploration":       {},
	"listSavedExplorations":     {},
	"updateSavedExploration":    {},
}

func TestAPIGenSavedExplorationOperationSurface(t *testing.T) {
	contracts := gen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 19; got != want {
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
	wantContracts := map[string]struct {
		kind, role, surface string
	}{
		"SavedExplorationCommandSignal":   {kind: "ui-signal", role: "command", surface: "saved_explorations"},
		"SavedExplorationCurrentSignal":   {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"SavedExplorationListItemSignal":  {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"SavedExplorationListSignal":      {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"SavedExplorationRevisionSignal":  {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"SavedExplorationSaveStateSignal": {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"SavedExplorationStateSignal":     {kind: "ui-signal", role: "signal", surface: "saved_explorations"},
		"DataExplorerDashboardPageSignal": {kind: "ui-signal", role: "signal", surface: "data"},
		"DataExplorerDashboardTargetSignal": {
			kind: "ui-signal", role: "signal", surface: "data",
		},
		"DataExplorerDashboardForkTargetSignal": {
			kind: "ui-signal", role: "signal", surface: "data",
		},
		"DataExplorerDashboardSignal": {kind: "ui-signal", role: "signal", surface: "data"},
		"DataExplorerDashboardSelectTargetCommandSignal": {
			kind: "ui-command", role: "command", surface: "data_dashboard",
		},
		"DataExplorerDashboardAppendCommandSignal": {
			kind: "ui-command", role: "command", surface: "data_dashboard",
		},
		"DataExplorerDashboardAppendEnvelope": {
			kind: "ui-envelope", role: "envelope", surface: "data_dashboard",
		},
	}
	found := make(map[string]bool, len(wantContracts))
	for _, contract := range document.Contracts {
		want, ok := wantContracts[contract.Name]
		if !ok {
			continue
		}
		if found[contract.Name] {
			t.Errorf("UI signal IR emits duplicate contract %s", contract.Name)
		}
		found[contract.Name] = true
		if contract.Kind != want.kind {
			t.Errorf("%s kind = %q, want %s", contract.Name, contract.Kind, want.kind)
		}
		if contract.Extensions["x-leapview-contract-role"] != want.role {
			t.Errorf("%s contract role = %v, want %q", contract.Name, contract.Extensions["x-leapview-contract-role"], want.role)
		}
		if contract.Extensions["x-leapview-surface"] != want.surface {
			t.Errorf("%s surface = %v, want %s", contract.Name, contract.Extensions["x-leapview-surface"], want.surface)
		}
	}
	for name := range wantContracts {
		if !found[name] {
			t.Errorf("UI signals do not emit required contract %s", name)
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
		"/api/v1/projects/{project}/saved-explorations/{exploration}/export",
		"/api/v1/projects/{project}/saved-explorations/url-export",
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
