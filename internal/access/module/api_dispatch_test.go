package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	"github.com/go-chi/chi/v5"
)

func TestDispatchAPIGenOperationCoversProjectRoleSurface(t *testing.T) {
	module := &Module{handler: accesshttp.Handler{AuthorizationPolicyTargetID: "target-1", AuthorizationPolicyEnvironment: "prod"}}
	for _, operation := range []string{"listProjectRoles"} {
		t.Run(operation, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-1/roles", nil)
			ctx := chi.NewRouteContext()
			ctx.URLParams.Add("project", "project-1")
			request = request.WithContext(contextWithRoute(request, ctx))
			response := httptest.NewRecorder()
			if !module.DispatchAPIGenOperation(operation, response, request) {
				t.Fatalf("operation %q was not dispatched", operation)
			}
			wantStatus := http.StatusOK
			if response.Code != wantStatus {
				t.Fatalf("operation %q status = %d, want %d", operation, response.Code, wantStatus)
			}
		})
	}
	if module.DispatchAPIGenOperation("unknownProjectAccessOperation", httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("unknown operation was dispatched")
	}
}

func TestDispatchAPIGenOperationRejectsUnsupportedProjectGrantSurface(t *testing.T) {
	module := &Module{handler: accesshttp.Handler{}}
	for _, operation := range []string{"listGrants", "createGrant", "getGrant", "updateGrant", "deleteGrant"} {
		if module.DispatchAPIGenOperation(operation, httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)) {
			t.Fatalf("unsupported operation %q was dispatched", operation)
		}
	}
}

func TestDispatchAPIGenOperationReachesProjectRoleBindingDeleteHandler(t *testing.T) {
	module := &Module{handler: accesshttp.Handler{
		AuthorizationPolicyTargetID: "target-1", AuthorizationPolicyEnvironment: "prod",
		Repository: func() (access.Repository, error) { return nil, errors.New("test repository unavailable") },
	}}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project-1/role-bindings/binding-1", strings.NewReader(`{"expectedRevision":1}`))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", "project-1")
	ctx.URLParams.Add("binding", "binding-1")
	request = request.WithContext(contextWithRoute(request, ctx))
	request.Header.Set("Idempotency-Key", "delete-1")
	response := httptest.NewRecorder()
	if !module.DispatchAPIGenOperation("deleteProjectRoleBinding", response, request) {
		t.Fatal("deleteProjectRoleBinding was not dispatched")
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s; want handler error", response.Code, response.Body.String())
	}
}

func TestDispatchAPIGenOperationReachesEffectiveAccessHandlers(t *testing.T) {
	module := &Module{handler: accesshttp.Handler{
		CurrentPrincipal: func(*http.Request) (accesshttp.Principal, bool) {
			return accesshttp.Principal{ID: "principal-alice", Kind: access.PrincipalKindUser}, true
		},
		EffectiveAccess: func(context.Context, string) ([]access.AuthorizationDecision, error) {
			return []access.AuthorizationDecision{{
				Allowed: true, Capability: access.CapabilityResourceRead, Reason: "direct grant",
				ResourceKind: "dashboard", ResourceID: "dashboard_sales", GrantID: "grant-1",
			}}, nil
		},
	}}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-1/effective-capabilities?resourceKind=dashboard&resourceId=dashboard_sales", nil)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", "project-1")
	request = request.WithContext(contextWithRoute(request, ctx))
	response := httptest.NewRecorder()
	if !module.DispatchAPIGenOperation("listEffectiveCapabilities", response, request) {
		t.Fatal("listEffectiveCapabilities was not dispatched")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("list effective access status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-1/authorization-checks", strings.NewReader(`{"checks":[]}`))
	ctx = chi.NewRouteContext()
	ctx.URLParams.Add("project", "project-1")
	request = request.WithContext(contextWithRoute(request, ctx))
	response = httptest.NewRecorder()
	if !module.DispatchAPIGenOperation("checkAuthorizationBatch", response, request) {
		t.Fatal("checkAuthorizationBatch was not dispatched")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("check authorization status=%d body=%s", response.Code, response.Body.String())
	}
}

// Kept local to avoid coupling this conformance test to access/http's test
// helpers while still constructing the same Chi route context as the runtime.
func contextWithRoute(request *http.Request, route *chi.Context) context.Context {
	return context.WithValue(request.Context(), chi.RouteCtxKey, route)
}
