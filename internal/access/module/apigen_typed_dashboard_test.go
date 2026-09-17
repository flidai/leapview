package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAPIGenTypedDashboardReadRequiresExactPermissionPair(t *testing.T) {
	for _, test := range []struct {
		name       string
		resourceID string
		credential access.PermissionPair
		wantStatus int
	}{
		{
			name:       "dashboard read",
			resourceID: "dashboard_a",
			credential: mustAPIGenDashboardPair(t, access.ActionDashboardRead, "dashboard_a"),
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "semantic-only token",
			resourceID: "dashboard_a",
			credential: mustAPIGenSemanticPair(t),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "dashboard A cannot read dashboard B",
			resourceID: "dashboard_b",
			credential: mustAPIGenDashboardPair(t, access.ActionDashboardRead, "dashboard_a"),
			wantStatus: http.StatusForbidden,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, snapshot := apigenSnapshot(t, "principal", "", projectgraph.ResourceID(test.resourceID), projectgraph.KindDashboard, true, false)
			module := browserGuardModule(browserGuardRepository{}, Principal{ID: "principal"}, true)
			authorizer, err := module.APIGenAuthorizer(
				apigenRuntimeFake{project: "project_demo", lease: apigenLeaseFake{identity: identity, snapshot: snapshot}},
				map[string]APIGenOperationContract{"getDashboard": {
					OperationID: "getDashboard", Method: http.MethodGet,
					Path: "/api/v1/dashboards/{dashboard}", Protected: true, AuthzMode: "privilege",
					Extensions: map[string]any{
						"x-authz":                  map[string]any{"mode": "privilege", "privilege": "RESOURCE_READ"},
						apiGenObjectScopeExtension: "dashboard",
					},
				}},
				APIGenResourceResolvers{Dashboard: apigenResolver("dashboard", projectgraph.KindDashboard)},
			)
			if err != nil {
				t.Fatal(err)
			}
			protected, ok := authorizer.Protect("getDashboard", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok || protected == nil {
				t.Fatal("typed dashboard operation was not protected")
			}
			request := apigenRequest(http.MethodGet, "/api/v1/dashboards/"+test.resourceID, map[string]string{"dashboard": test.resourceID})
			request = request.WithContext(WithAPICredential(request.Context(), access.APICredential{
				Principal: access.Principal{ID: "principal"},
				Token:     access.APIToken{ID: "typed", PrincipalID: "principal", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{test.credential}},
			}))
			recorder := httptest.NewRecorder()
			protected.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d body = %q, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestAPIGenTypedDashboardMappingDoesNotAuthorizeMutationOrOtherResources(t *testing.T) {
	dashboard, err := access.NewResourceRef("dashboard_a", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := access.NewResourceRef("semantic_a", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, operation string
		resource        access.ResourceRef
		wantMapped      bool
	}{
		{name: "viewer read", operation: "getDashboard", resource: dashboard, wantMapped: true},
		{name: "appearance mutation", operation: "updateDashboardAppearance", resource: dashboard},
		{name: "authoring read", operation: "getDashboardAuthoringDashboard", resource: dashboard},
		{name: "semantic resource", operation: "getDashboard", resource: semantic},
	} {
		t.Run(test.name, func(t *testing.T) {
			action, mapped := apiGenTypedAction(test.operation, test.resource)
			if mapped != test.wantMapped {
				t.Fatalf("mapped = %t action = %q, want mapped=%t", mapped, action, test.wantMapped)
			}
			if test.wantMapped && action != access.ActionDashboardRead {
				t.Fatalf("action = %q, want %q", action, access.ActionDashboardRead)
			}
		})
	}
}

func mustAPIGenDashboardPair(t *testing.T, action access.Action, id string) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID(id), projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, "project_demo", resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func mustAPIGenSemanticPair(t *testing.T) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef("semantic_a", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionSemanticRead, "project_demo", resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
