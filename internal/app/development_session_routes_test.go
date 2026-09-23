package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/project/developmentsession"
	developmenthttp "github.com/flidai/leapview/internal/project/developmentsession/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
)

func TestMountDevelopmentSessionRoutesRegistersStableNavigationSurface(t *testing.T) {
	router := chi.NewRouter()
	mountDevelopmentSessionRoutes(router, developmenthttp.New(developmenthttp.Config{Enabled: true}), candidateRouteDependencies{}, nil, nil)
	want := map[string]bool{
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/events":                                     false,
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

func TestDevelopmentSessionBrowserAndAPIRoutesDoNotOverlap(t *testing.T) {
	router := chi.NewRouter()
	session := developmenthttp.New(developmenthttp.Config{Enabled: true, Store: developmentsession.NewMemoryStore()})
	mountDevelopmentSessionRoutes(router, session, candidateRouteDependencies{}, nil, nil)
	session.Mount(router)
	counts := map[string]int{}
	if err := chi.Walk(router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		counts[method+" "+path]++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"GET /api/v1/projects/{project}/targets/{target}/development-session/events",
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/redirect",
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview",
		"GET /api/v1/projects/{project}/targets/{target}/development-session/candidate/preview/events",
	} {
		if counts[route] != 1 {
			t.Fatalf("development-session route %q registered %d times", route, counts[route])
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
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/candidate/preview/events", nil))
	if !guarded || response.Code != http.StatusForbidden {
		t.Fatalf("stable route guard = %t, status = %d", guarded, response.Code)
	}
}

func TestDevelopmentSessionAPIRoutesInstallBearerPrincipal(t *testing.T) {
	auth, err := accessmodule.NewAuth(nil, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "session-token", CSRFKey: strings.Repeat("k", 32)})
	if err != nil {
		t.Fatal(err)
	}
	accessModule, err := accessmodule.Build(t.Context(), accessmodule.Config{ExistingAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	projectID := projectgraph.ResourceID("project_1")
	principal := accessmodule.LocalDeveloperPrincipal()
	key := developmentsession.Key{OwnerID: principal.ID, CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectID, TargetID: "target_1", Environment: "development"}
	store := developmentsession.NewMemoryStore()
	if _, err := store.Save(t.Context(), developmentsession.Record{Key: key}, 0); err != nil {
		t.Fatal(err)
	}
	session := developmenthttp.New(developmenthttp.Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(r *http.Request) (string, bool) {
			current, ok := accessModule.CurrentPrincipal(r)
			return current.ID, ok
		},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
	})
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer session-token" {
				http.Error(w, "bearer required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	mountDevelopmentSessionAPIRoutes(router, accessModule, session)
	path := "/api/v1/projects/project_1/targets/target_1/development-session/"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), principal.ID) {
		t.Fatalf("authenticated development session = %d %q", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status = %d", response.Code)
	}
}

func TestDevelopmentSessionProjectScopeBeforeFirstActivation(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	key := developmentsession.Key{
		OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "checkout_1",
		ProjectID: projectID, TargetID: "target_1", Environment: "dev",
	}
	store := developmentsession.NewMemoryStore()
	if _, err := store.Save(t.Context(), developmentsession.Record{Key: key}, 0); err != nil {
		t.Fatal(err)
	}
	// A fresh local target has a durable Project claim but no activated
	// dashboard serving lease. Its first development-session attempt must still
	// be able to resolve the exact, claimed project scope.
	claim := projectClaimRepositoryStub{claim: deployment.ProjectClaim{
		ProjectID: projectID, Environment: servingstate.Environment("dev"),
		ClaimedBy: key.OwnerID, ClaimedAt: time.Now().UTC(),
	}}
	authoringProject := postgresAuthoringProjectIDResolver(claim, authoringServingStateReaderStub{}, key.TargetID, "dev")
	runtime := &runtimeServices{
		projectIDResolver: func(context.Context) (projectgraph.ResourceID, error) {
			return "", errors.New("no active serving generation")
		},
		developmentProjectIDResolver: authoringProject,
	}
	if _, err := runtime.resolveProjectID(t.Context()); err == nil {
		t.Fatal("fresh runtime unexpectedly acquired an active serving lease")
	}
	handler := developmenthttp.New(developmenthttp.Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID,
		TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return key.OwnerID, true },
		ResolveProjectID: runtime.developmentProjectIDResolver,
	})
	router := chi.NewRouter()
	handler.Mount(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), key.ID()) {
		t.Fatalf("fresh local development session = %d %q", response.Code, response.Body.String())
	}
}
