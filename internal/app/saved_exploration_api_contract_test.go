package app

import (
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
)

func TestSavedExplorationCommandsAuthenticateBeforeDomainAuthorization(t *testing.T) {
	for _, operationID := range []string{
		"createSavedExploration", "updateSavedExploration", "duplicateSavedExploration", "archiveSavedExploration",
	} {
		contract, ok := analyticsgen.GetAPIGenOperationContract(operationID)
		if !ok {
			t.Fatalf("missing generated operation contract %q", operationID)
		}
		// Ownership and exact semantic/token checks are exercised by the saved
		// authorizer tests; the transport must retain authentication and durable
		// command targeting without an unrelated project-management shortcut.
		if contract.AuthzMode != "authenticated" || contract.Command == nil || contract.Command.Privilege != "" {
			t.Fatalf("%s authz/command = mode %q command %#v", operationID, contract.AuthzMode, contract.Command)
		}
		if scope := contract.Extensions["x-leapview-object-scope"]; scope != nil {
			t.Fatalf("%s object scope = %#v, want domain-resolved ownership", operationID, scope)
		}
		if contract.Command.Target == nil || contract.Command.Target.Parameter != "project" {
			t.Fatalf("%s target = %#v, want service-level project target metadata", operationID, contract.Command.Target)
		}
	}
}

func TestSavedExplorationEndpointShapesAreExplicitlyCovered(t *testing.T) {
	shapes := map[string]map[string]string{
		"/api/v1/projects/{project}/saved-explorations": {
			"GET":  "listSavedExplorations",
			"POST": "createSavedExploration",
		},
		"/api/v1/projects/{project}/saved-explorations/{exploration}": {
			"GET":   "getSavedExploration",
			"PATCH": "updateSavedExploration",
		},
		"/api/v1/projects/{project}/saved-explorations/{exploration}/duplicate": {
			"POST": "duplicateSavedExploration",
		},
		"/api/v1/projects/{project}/saved-explorations/{exploration}/archive": {
			"POST": "archiveSavedExploration",
		},
		"/api/v1/projects/{project}/saved-explorations/{exploration}/export": {
			"POST": "exportSavedExploration",
		},
		"/api/v1/projects/{project}/saved-explorations/url-export": {
			"POST": "exportSavedExplorationURL",
		},
	}
	for path, methods := range shapes {
		for method, operationID := range methods {
			contract, ok := analyticsgen.GetAPIGenOperationContract(operationID)
			if !ok {
				t.Fatalf("missing generated operation contract %q", operationID)
			}
			if contract.Method != method || contract.Path != path {
				t.Fatalf("%s %s contract = method %s path %s, want %s %s", method, path, contract.Method, contract.Path, method, path)
			}
			requestContract, ok := analyticsgen.GetAPIGenOperationContractForRequest(method, path)
			if !ok || requestContract.OperationID != operationID {
				t.Fatalf("%s %s request lookup = %#v, want operation %s", method, path, requestContract, operationID)
			}
		}
	}
}
