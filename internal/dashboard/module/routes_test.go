package module

import (
	"net/http"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestMountAuthenticatedRegistersDashboardBuilderBrowserSurface(t *testing.T) {
	router := chi.NewRouter()
	var capabilities []access.Capability
	identityResources := func(capability access.Capability, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
		capabilities = append(capabilities, capability)
		return next
	}
	(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{ProtectWithResources: identityResources})

	want := map[string]bool{
		"GET /dashboards/new":                               false,
		"POST /dashboards/new":                              false,
		"GET /dashboards/{dashboard}/fork":                  false,
		"POST /dashboards/{dashboard}/fork":                 false,
		"GET /dashboards/{dashboard}/edit":                  false,
		"GET /dashboards/{dashboard}/preview":               false,
		"GET /dashboards/{dashboard}/export.yaml":           false,
		"POST /dashboards/{dashboard}/draft/command":        false,
		"POST /dashboards/{dashboard}/draft/filter":         false,
		"POST /dashboards/{dashboard}/draft/filter-options": false,
		"POST /dashboards/{dashboard}/commands/select":      false,
	}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("missing route %s", route)
		}
	}
	if len(capabilities) < 10 {
		t.Fatalf("captured %d route capabilities, want at least 10", len(capabilities))
	}
	for index, wantCapability := range []access.Capability{
		access.CapabilityResourceRead,
		access.CapabilityResourceRead,
		access.CapabilityResourceEdit,
		access.CapabilityResourceEdit,
		// Forking requires VIEW of the source and EDIT on the target project.
		access.CapabilityResourceRead,
		access.CapabilityResourceEdit,
	} {
		if capabilities[index] != wantCapability {
			t.Errorf("route index %d capability = %q, want %q", index, capabilities[index], wantCapability)
		}
	}
}
