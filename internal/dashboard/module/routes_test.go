package module

import (
	"net/http"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/go-chi/chi/v5"
)

func TestMountAuthenticatedRegistersDashboardBuilderBrowserSurface(t *testing.T) {
	router := chi.NewRouter()
	identity := func(_ access.Privilege, next http.HandlerFunc) http.HandlerFunc { return next }
	identityObjects := func(_ access.Privilege, _ func(*http.Request, string) []access.ObjectRef, next http.HandlerFunc) http.HandlerFunc {
		return next
	}
	(&Module{handler: dashboardhttp.Handler{}}).MountAuthenticated(router, RouteGuard{Protect: identity, ProtectWithObjects: identityObjects})

	want := map[string]bool{
		"GET /workspaces/{workspace}/dashboards/{dashboard}/edit":           false,
		"GET /workspaces/{workspace}/dashboards/{dashboard}/preview":        false,
		"GET /workspaces/{workspace}/dashboards/{dashboard}/export.yaml":    false,
		"POST /workspaces/{workspace}/dashboards/{dashboard}/draft/command": false,
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
