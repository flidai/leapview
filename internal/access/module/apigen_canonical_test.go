package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAPIGenPlatformScopeUsesPlatformRoleEvenWhenAuthenticated(t *testing.T) {
	repo := browserGuardRepository{admin: false}
	module := browserGuardModule(repo, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		operations: map[string]APIGenOperationContract{
			"platformStatus": {OperationID: "platformStatus", Protected: true, AuthzMode: "authenticated", Extensions: map[string]any{apiGenObjectScopeExtension: "platform"}},
		},
	}
	protected, ok := authorizer.Protect("platformStatus", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("platform operation was not protected")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/platform/status", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestAPIGenAuthenticatedOperationUsesPrincipalAuthentication(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		operations: map[string]APIGenOperationContract{
			"currentPrincipal": {OperationID: "currentPrincipal", Protected: true, AuthzMode: "authenticated"},
		},
	}
	protected, ok := authorizer.Protect("currentPrincipal", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if !ok || protected == nil {
		t.Fatal("authenticated operation was not protected")
	}
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/current-principal", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAPIGenResourceResolverRejectsMismatchedCanonicalScope(t *testing.T) {
	resolver := func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }
	authorizer := &APIGenAuthorizer{
		scopes: map[string]apiGenResourceScope{
			"dashboard": {pathParameter: "dashboard", resolver: resolver, kind: projectgraph.KindDashboard},
		},
	}
	for name, contract := range map[string]APIGenOperationContract{
		"wrong parameter": {
			Path:       "/api/v1/projects/{project}/dashboards/{model}",
			Command:    &APIGenCommandContract{Target: &APIGenCommandTarget{Parameter: "model", Type: "dashboard"}},
			Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
		},
		"legacy scope": {
			Path:       "/api/v1/projects/{project}/dashboards/{dashboard}",
			Extensions: map[string]any{apiGenObjectScopeExtension: "project-environment"},
		},
	} {
		if _, ok := authorizer.resourceResolverForContract(contract); ok {
			t.Errorf("%s accepted a mismatched or legacy scope", name)
		}
	}
}

func TestAPIGenProtectNeverDowngradesScopedCapabilityToAuthentication(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "principal"}, true)
	authorizer := &APIGenAuthorizer{
		module: module,
		scopes: map[string]apiGenResourceScope{
			"dashboard": {pathParameter: "dashboard", resolver: func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }, kind: projectgraph.KindDashboard},
		},
		operations: map[string]APIGenOperationContract{
			"unscopedCapability":   {OperationID: "unscopedCapability", Protected: true, AuthzMode: "privilege", Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"}},
			"selfCapability":       {OperationID: "selfCapability", Protected: true, AuthzMode: "privilege", Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"}, Extensions: map[string]any{apiGenObjectScopeExtension: "principal"}},
			"scopedAuthentication": {OperationID: "scopedAuthentication", Protected: true, AuthzMode: "authenticated", Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"}},
		},
	}
	for operationID := range authorizer.operations {
		if protected, ok := authorizer.Protect(operationID, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})); ok || protected != nil {
			t.Errorf("%s was accepted without its required canonical resource guard", operationID)
		}
	}
}

func TestAPIGenValidateRejectsInvalidAuthzScopeCombinations(t *testing.T) {
	authorizer := &APIGenAuthorizer{scopes: map[string]apiGenResourceScope{
		"dashboard": {pathParameter: "dashboard", resolver: func(*http.Request, projectgraph.ResourceID) []access.ResourceRef { return nil }, kind: projectgraph.KindDashboard},
	}}
	tests := map[string]APIGenOperationContract{
		"authenticated resource": {
			OperationID: "authenticatedResource", Protected: true, AuthzMode: "authenticated",
			Extensions: map[string]any{apiGenObjectScopeExtension: "dashboard"},
		},
		"privilege principal": {
			OperationID: "privilegePrincipal", Protected: true, AuthzMode: "privilege",
			Command:    &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
			Extensions: map[string]any{apiGenObjectScopeExtension: "principal"},
		},
		"privilege unscoped": {
			OperationID: "privilegeUnscoped", Protected: true, AuthzMode: "privilege",
			Command: &APIGenCommandContract{AuthzMode: "privilege", Privilege: "RESOURCE_READ"},
		},
	}
	for name, contract := range tests {
		if err := authorizer.validateOperation(contract.OperationID, contract); err == nil {
			t.Errorf("%s contract unexpectedly validated", name)
		}
	}
	public := APIGenOperationContract{OperationID: "public", Protected: false, AuthzMode: "none", Extensions: map[string]any{apiGenObjectScopeExtension: "platform"}}
	if err := authorizer.validateOperation("public", public); err == nil {
		t.Fatal("unprotected operation with scope metadata unexpectedly validated")
	}
}

func TestAPIGenMetadataRejectsWhitespace(t *testing.T) {
	if scope, ok := apiGenScope(APIGenOperationContract{Extensions: map[string]any{apiGenObjectScopeExtension: " platform"}}); ok || scope != "" {
		t.Fatalf("whitespace-prefixed scope accepted: %q, %t", scope, ok)
	}
	if capability, ok := apiGenOperationCapability(APIGenOperationContract{AuthzMode: "privilege", Extensions: map[string]any{"x-authz": map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ "}}}); ok || capability != "" {
		t.Fatalf("whitespace-suffixed capability accepted: %q, %t", capability, ok)
	}
}
