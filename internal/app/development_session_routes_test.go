package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	developmenthttp "github.com/flidai/leapview/internal/project/developmentsession/http"
	"github.com/go-chi/chi/v5"
)

func TestMountDevelopmentSessionRoutesRegistersStableNavigationSurface(t *testing.T) {
	router := chi.NewRouter()
	mountDevelopmentSessionRoutes(router, developmenthttp.New(developmenthttp.Config{Enabled: true}), candidateRouteDependencies{}, nil, nil)
	want := map[string]bool{
		"GET /api/v1/projects/{project}/targets/{target}/development-session/events":                                                       false,
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview":                                            false,
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/dashboards/{dashboard}":                     false,
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/dashboards/{dashboard}/pages/{page}":        false,
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/updates":                                    false,
		"POST /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/dashboards/{dashboard}/commands/{command}": false,
	}
	if err := chi.Walk(router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if _, ok := want[method+" "+path]; ok {
			want[method+" "+path] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for route, found := range want {
		if !found {
			t.Errorf("stable route missing: %s", route)
		}
	}
}

func TestMountDevelopmentSessionRoutesAppliesProjectGuard(t *testing.T) {
	router := chi.NewRouter()
	guarded := false
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			guarded = true
			http.Error(w, "denied", http.StatusForbidden)
		}
	}
	mountDevelopmentSessionRoutes(router, developmenthttp.New(developmenthttp.Config{Enabled: true}), candidateRouteDependencies{}, nil, guard)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/events", nil))
	if !guarded || response.Code != http.StatusForbidden {
		t.Fatalf("stable route guard = %t, status = %d", guarded, response.Code)
	}
}
