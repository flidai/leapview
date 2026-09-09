package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/observability"
	"github.com/go-chi/chi/v5"
)

// This exercises the shared ingress used before browser/API authorization,
// cursor lookup, idempotency, and domain dispatch. It cannot select a Project.
func TestProjectBoundaryRejectsRequestSelectorsBeforeDispatch(t *testing.T) {
	for _, path := range []string{"/", "/explore", "/search", "/catalog/search", "/sources", "/dashboards/sales", "/updates", "/agent", "/api/v1/search", "/api/v1/semantic-models/sales/query", "/api/v1/projects/bound/releases", "/api/v1/projects/bound/audit-events"} {
		t.Run(path, func(t *testing.T) {
			calls := 0
			mux := chi.NewRouter()
			mountRouterMiddleware(mux, routerMiddlewareDependencies{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry: observability.New()})
			mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
			for _, selector := range []string{"project=foreign", "projectId=foreign", "projectUid=foreign", "projectUID=foreign", "project_id=foreign", "project=", "project=bound&project=foreign", "%70rojectId=foreign"} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?"+selector, nil))
				if response.Code != http.StatusBadRequest || calls != 0 {
					t.Fatalf("%s: status=%d dispatches=%d, want rejection before dispatch", selector, response.Code, calls)
				}
				if strings.Contains(response.Body.String(), "foreign") {
					t.Fatal("selector value leaked into diagnostic")
				}
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+"?q=sales", nil))
			if response.Code != http.StatusNoContent || calls != 1 {
				t.Fatal("ordinary bound request was rejected")
			}
		})
	}
}

func TestProjectBoundaryRejectsMalformedQueryBeforeDispatch(t *testing.T) {
	calls := 0
	mux := chi.NewRouter()
	mountRouterMiddleware(mux, routerMiddlewareDependencies{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry: observability.New()})
	mux.Get("/search", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/search?%zz", nil))
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("malformed query: status=%d dispatches=%d, want rejection before dispatch", response.Code, calls)
	}
}

func TestProjectBoundaryPreservesBootstrapAndPlatformAuditFilter(t *testing.T) {
	mux := chi.NewRouter()
	mountRouterMiddleware(mux, routerMiddlewareDependencies{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), telemetry: observability.New()})
	const body = `{"projectUid":"issuer_uid","environment":"prod"}`
	mux.Post("/api/v1/instance/project-claim", func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body {
			t.Fatal("ingress changed bootstrap payload")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Get("/api/v1/audit-events", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/instance/project-claim", strings.NewReader(body)),
		httptest.NewRequest(http.MethodGet, "/api/v1/audit-events?project=bound", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("legal contract rejected: %d", response.Code)
		}
	}
}

func TestProjectBoundaryGeneratedLocatorsCannotRetarget(t *testing.T) {
	store := testStore(t)
	principal := testPrincipal(t, t.Context(), store, "boundary@example.com", "Boundary")
	token, _ := testScopedAPIToken(t, t.Context(), store, access.APITokenInput{PrincipalID: principal.ID, Name: "boundary", Capabilities: []access.Capability{access.CapabilityProjectAdmin, access.CapabilityResourceRead, access.CapabilityResourceUse}})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})}))
	router := server.Routes()
	parameters := regexp.MustCompile(`\{[^}]+\}`)
	count := 0
	for operation, contract := range apiaggregate.GetAPIGenOperationContracts() {
		if !strings.Contains(contract.Path, "{project}") {
			continue
		}
		count++
		t.Run(operation, func(t *testing.T) {
			path := strings.ReplaceAll(contract.Path, "{project}", "foreign_project")
			path = parameters.ReplaceAllString(path, "0198f2c0-7c7a-7f00-8a11-000000000001")
			request := httptest.NewRequest(contract.Method, path, strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Idempotency-Key", "boundary-"+operation)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("foreign locator %s: %d %s", path, response.Code, response.Body)
			}
		})
	}
	if count == 0 {
		t.Fatal("no generated Project locator routes audited")
	}
}
