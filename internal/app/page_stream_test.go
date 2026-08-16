package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
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
		{name: "builder resource", url: "/updates?route=dashboard_builder&dashboard=dashboard_sales", want: "dashboard_sales"},
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

func TestDashboardPageStreamResourceFailsClosedForMissingOrInvalidID(t *testing.T) {
	for _, url := range []string{
		"/updates?route=dashboard",
		"/updates?route=dashboard&dashboard=",
		"/updates?route=dashboard&dashboard=not a resource",
		"/updates?route=dashboard&dashboard=dashboard with spaces",
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
