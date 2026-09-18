package module

import (
	"net/http"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

// TestDashboardAuthoringPrivateAuthorizationMatrix is the route-level
// qualification table for the supported browser authoring surface. The
// middleware wrappers are the enforcement points, so this records the typed
// action each fixed route is mounted with and separately accounts for the
// body-dependent command route.
func TestDashboardAuthoringPrivateAuthorizationMatrix(t *testing.T) {
	router := chi.NewRouter()
	var resourceActions []access.Action
	var authoringActions []access.Action
	commandGuards := 0
	identityResources := func(capability access.Capability, action access.Action, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
		if capability == access.CapabilityResourceRead || capability == access.CapabilityResourceEdit || capability == access.CapabilityResourceManage {
			resourceActions = append(resourceActions, action)
		}
		return next
	}
	authoringResources := func(_ access.Capability, action access.Action, next http.HandlerFunc) http.HandlerFunc {
		authoringActions = append(authoringActions, action)
		return next
	}
	(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{
		ProtectWithResources:       identityResourcesLegacy,
		ProtectWithResourceAction:  identityResources,
		ProtectWithAuthoringAction: authoringResources,
		ProtectWithAuthoringCommand: func(next http.HandlerFunc) http.HandlerFunc {
			commandGuards++
			return next
		},
	})

	// Three fixed create wrappers (new dashboard and the shared fork wrapper),
	// eleven dashboard read wrappers (including the nested fork source read), five update routes,
	// one read-only export route, and one archive route are registered. The command route is intentionally
	// body-dependent and is checked by the HTTP qualification test.
	if countAction(resourceActions, access.ActionDashboardCreate) != 3 {
		t.Fatalf("browser create action count = %d, want 3 (%v)", countAction(resourceActions, access.ActionDashboardCreate), resourceActions)
	}
	if countAction(resourceActions, access.ActionDashboardRead) != 11 {
		t.Fatalf("browser dashboard-read action count = %d, want 11 (%v)", countAction(resourceActions, access.ActionDashboardRead), resourceActions)
	}
	if countAction(authoringActions, access.ActionDashboardUpdate) != 5 || countAction(authoringActions, access.ActionDashboardRead) != 1 || countAction(authoringActions, access.ActionDashboardDelete) != 1 {
		t.Fatalf("browser authoring action matrix = %v, want five update, one read, and one delete", authoringActions)
	}
	if commandGuards != 1 {
		t.Fatalf("body-dependent command guards = %d, want 1", commandGuards)
	}
}

// identityResourcesLegacy is retained as a distinct callback so the matrix
// test exercises the typed route wrapper rather than its compatibility
// fallback. It is intentionally equivalent to a no-op registration guard.
func identityResourcesLegacy(_ access.Capability, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
	return next
}

func countAction(actions []access.Action, want access.Action) int {
	count := 0
	for _, action := range actions {
		if action == want {
			count++
		}
	}
	return count
}

func TestMountAuthenticatedRegistersDashboardBuilderBrowserSurface(t *testing.T) {
	router := chi.NewRouter()
	var capabilities []access.Capability
	commandGuards := 0
	identityResources := func(capability access.Capability, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
		capabilities = append(capabilities, capability)
		return next
	}
	(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{
		ProtectWithResources: identityResources,
		ProtectWithAuthoringCommand: func(next http.HandlerFunc) http.HandlerFunc {
			commandGuards++
			return next
		},
	})

	want := map[string]bool{
		"GET /dashboards/new":                               false,
		"POST /dashboards/new":                              false,
		"GET /dashboards/{dashboard}/fork":                  false,
		"POST /dashboards/{dashboard}/fork":                 false,
		"GET /dashboards/{dashboard}/edit":                  false,
		"POST /dashboards/{dashboard}/archive":              false,
		"GET /dashboards/{dashboard}/preview":               false,
		"GET /dashboards/{dashboard}/export.yaml":           false,
		"POST /dashboards/{dashboard}/draft/command":        false,
		"POST /dashboards/{dashboard}/draft/filter":         false,
		"POST /dashboards/{dashboard}/draft/filter-options": false,
		"POST /dashboards/{dashboard}/draft/visual-window":  false,
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
	if commandGuards != 1 {
		t.Fatalf("body-dependent command guards = %d, want 1", commandGuards)
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
