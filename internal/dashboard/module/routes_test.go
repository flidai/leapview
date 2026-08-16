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
	identity := func(_ access.Privilege, next http.HandlerFunc) http.HandlerFunc { return next }
	identityObjects := func(_ access.Privilege, _ func(*http.Request, string) []access.ObjectRef, next http.HandlerFunc) http.HandlerFunc {
		return next
	}
	identityResources := func(_ access.Capability, _ func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
		return next
	}
	(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{Protect: identity, ProtectWithObjects: identityObjects, ProtectWithResources: identityResources})

	want := map[string]bool{
		"GET /dashboards/{dashboard}/edit":             false,
		"GET /dashboards/{dashboard}/preview":          false,
		"GET /dashboards/{dashboard}/export.yaml":      false,
		"POST /dashboards/{dashboard}/draft/command":   false,
		"POST /dashboards/{dashboard}/commands/select": false,
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
}
