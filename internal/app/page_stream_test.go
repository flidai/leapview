package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPageStreamRouteInventoryIsProjectOwned(t *testing.T) {
	got := map[string]struct{}{
		routeLogin: {}, routeDashboard: {}, routeDashboardBuilder: {},
		routeChat: {}, routeAdmin: {},
	}
	want := []string{"login", "dashboard", "dashboard_builder", "chat", "admin"}
	for _, route := range want {
		if _, ok := got[route]; !ok {
			t.Fatalf("route %q is missing from the page-stream inventory", route)
		}
	}
	for _, legacy := range []string{
		"catalog", "pipelines", "workspace", "workspace_asset", "connections",
		"connection_asset", "data",
	} {
		if _, ok := got[legacy]; ok {
			t.Fatalf("legacy workspace page-stream route %q remains registered", legacy)
		}
	}
}

func TestDashboardPageStreamResourceRequiresExactDashboardID(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want projectgraph.ResourceID
	}{
		{name: "direct resource", url: "/updates?route=dashboard&dashboard=dashboard_sales", want: "dashboard_sales"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			resources := dashboardPageStreamResource(req, projectgraph.ResourceID("project_demo"))
			if len(resources) != 1 {
				t.Fatalf("resource count = %d, want 1", len(resources))
			}
			if got := resources[0].ID(); got != tt.want {
				t.Fatalf("resource id = %q, want %q", got, tt.want)
			}
			if got := resources[0].Kind(); got != projectgraph.KindDashboard {
				t.Fatalf("resource kind = %q, want %q", got, projectgraph.KindDashboard)
			}
			if err := resources[0].Validate(); err != nil {
				t.Fatalf("resource is not canonical: %v", err)
			}
		})
	}
}

func TestDashboardBuilderPageStreamAuthorizesPrivateDraftWithoutPublishedGraphResource(t *testing.T) {
	for _, test := range []struct {
		name       string
		url        string
		wantStatus int
		wantCalls  int
	}{
		{name: "private draft", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned", wantStatus: http.StatusNoContent, wantCalls: 1},
		{name: "missing dashboard", url: "/updates?route=dashboard_builder", wantStatus: http.StatusNotFound},
		{name: "duplicate dashboard", url: "/updates?route=dashboard_builder&dashboard=dashboard_owned&dashboard=dashboard_other", wantStatus: http.StatusNotFound},
		{name: "invalid dashboard", url: "/updates?route=dashboard_builder&dashboard=not%20a%20dashboard", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			authorizer := &repositoryDashboardAuthorizerFake{}
			guarded := protectProjectAuthoringResourceWithSelector(
				tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true},
				tusRuntime{project: "project_demo"},
				authorizer,
				access.CapabilityResourceEdit,
				dashboardBuilderPageStreamDashboardID,
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
			)
			recorder := httptest.NewRecorder()
			guarded(recorder, httptest.NewRequest(http.MethodGet, test.url, nil))
			if recorder.Code != test.wantStatus || authorizer.editCalls != test.wantCalls {
				t.Errorf("status = %d, edit calls = %d; want %d and %d", recorder.Code, authorizer.editCalls, test.wantStatus, test.wantCalls)
			}
		})
	}
}

func TestDashboardPageStreamResourceFailsClosedForMissingOrInvalidID(t *testing.T) {
	for _, url := range []string{
		"/updates?route=dashboard",
		"/updates?route=dashboard&dashboard=",
		"/updates?route=dashboard&dashboard=not%20a%20resource",
		"/updates?route=dashboard&dashboard=dashboard%20with%20spaces",
		"/updates?route=dashboard&dashboard=dashboard_sales&dashboard=dashboard_finance",
	} {
		t.Run(url, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, url, nil)
			if got := dashboardPageStreamResource(request, "project_demo"); got != nil {
				t.Fatalf("invalid dashboard selector resolved to %#v", got)
			}
		})
	}
}

func TestDashboardPageStreamResourceUsesDashboardCapabilityContract(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/updates?route=dashboard&dashboard=dashboard_sales", nil)
	resource := dashboardPageStreamResource(request, "project_demo")[0]
	if !access.SupportsCapability(resource.Kind(), access.CapabilityResourceRead) {
		t.Fatalf("dashboard resource does not support RESOURCE_READ")
	}
	if !access.SupportsCapability(resource.Kind(), access.CapabilityResourceEdit) {
		t.Fatalf("dashboard resource does not support RESOURCE_EDIT")
	}
}
