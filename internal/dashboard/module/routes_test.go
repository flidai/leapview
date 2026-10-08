package module

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestSavedVisualLibraryUsesTypedProjectAuthorization(t *testing.T) {
	for _, role := range []struct {
		name    string
		allowed bool
	}{{"author", true}, {"viewer", false}} {
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/visuals/saved"},
			{http.MethodPost, "/visuals/saved"},
			{http.MethodPost, "/visuals/saved/remove"},
		} {
			t.Run(route.method+route.path+"/"+role.name, func(t *testing.T) {
				router := chi.NewRouter()
				(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{
					// Production rejects routes without an explicit typed action.
					ProtectWithResources: func(_ access.Capability, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, _ http.HandlerFunc) http.HandlerFunc {
						return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }
					},
					ProtectWithResourceAction: func(capability access.Capability, action access.Action, resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, _ http.HandlerFunc) http.HandlerFunc {
						return func(w http.ResponseWriter, r *http.Request) {
							project := projectgraph.ResourceID("project_a")
							refs := resolve(r, project)
							if capability != access.CapabilityResourceEdit || action != access.ActionDashboardCreate || len(refs) != 1 || refs[0].ID() != project || refs[0].Kind() != projectgraph.KindProjectNamespace {
								t.Fatal("saved visuals must require authoring permission on the server-bound project")
							}
							if !role.allowed {
								w.WriteHeader(http.StatusForbidden)
								return
							}
							w.WriteHeader(http.StatusNoContent)
						}
					},
				})
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(route.method, route.path+"?project=project_b", nil))
				want := http.StatusNoContent
				if !role.allowed {
					want = http.StatusForbidden
				}
				if response.Code != want {
					t.Fatalf("status = %d, want %d", response.Code, want)
				}
			})
		}
	}
}

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

	// Six fixed create wrappers (new dashboard, the shared fork wrapper, and
	// three personal saved-visual routes),
	// twelve dashboard read wrappers (including the nested fork source read and
	// visual-to-Explorer handoff), seven update routes,
	// one read-only export route, and the archive and delete routes are registered. The command route is intentionally
	// body-dependent and is checked by the HTTP qualification test.
	if countAction(resourceActions, access.ActionDashboardCreate) != 6 {
		t.Fatalf("browser create action count = %d, want 6 (%v)", countAction(resourceActions, access.ActionDashboardCreate), resourceActions)
	}
	if countAction(resourceActions, access.ActionDashboardRead) != 12 {
		t.Fatalf("browser dashboard-read action count = %d, want 12 (%v)", countAction(resourceActions, access.ActionDashboardRead), resourceActions)
	}
	if countAction(authoringActions, access.ActionDashboardUpdate) != 7 || countAction(authoringActions, access.ActionDashboardRead) != 1 || countAction(authoringActions, access.ActionDashboardDelete) != 2 {
		t.Fatalf("browser authoring action matrix = %v, want seven update, one read, and two delete", authoringActions)
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
		"GET /visuals/saved":                                                false,
		"POST /visuals/saved":                                               false,
		"POST /visuals/saved/remove":                                        false,
		"POST /dashboards/{dashboard}/draft/chat-remove-visual":             false,
		"POST /dashboards/{dashboard}/draft/saved-visual":                   false,
		"GET /dashboards/new":                                               false,
		"POST /dashboards/new":                                              false,
		"GET /dashboards/{dashboard}/fork":                                  false,
		"POST /dashboards/{dashboard}/fork":                                 false,
		"GET /dashboards/{dashboard}/edit":                                  false,
		"POST /dashboards/{dashboard}/archive":                              false,
		"POST /dashboards/{dashboard}/delete":                               false,
		"GET /dashboards/{dashboard}/preview":                               false,
		"GET /dashboards/{dashboard}/export.yaml":                           false,
		"POST /dashboards/{dashboard}/draft/command":                        false,
		"POST /dashboards/{dashboard}/draft/filter":                         false,
		"POST /dashboards/{dashboard}/draft/filter-options":                 false,
		"POST /dashboards/{dashboard}/draft/visual-window":                  false,
		"POST /dashboards/{dashboard}/commands/select":                      false,
		"GET /dashboards/{dashboard}/pages/{page}/visuals/{visual}/explore": false,
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
