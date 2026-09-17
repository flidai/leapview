package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/agent"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/observability"
	"github.com/go-chi/chi/v5"
)

func TestProjectAuditProducerPersistsThroughScopedEndpoint(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	admin := testPlatformPrincipal(t, ctx, store, "audit-boundary@example.com", "Audit Boundary")
	token := testAPIToken(t, ctx, store, admin.ID, "audit-boundary")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})}))
	if err := candidateSourceAuditRecorder(server.routes.accessModule)(ctx, deploymentmodule.CandidateSourceAuditEvent{
		PrincipalID: admin.ID, ProjectID: testProjectID, Action: "candidate.source.resolved",
		Capability: access.CapabilityResourcePublish, Status: "success", MetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("record Project audit: %v", err)
	}
	preferences, ok := testAccessRepository(store).(access.AuditedPrincipalPreferences)
	if !ok {
		t.Fatal("test access repository does not support audited preferences")
	}
	if err := preferences.SetPrincipalThemeAudited(ctx, admin.ID, access.ThemeDark); err != nil {
		t.Fatalf("record platform audit: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+testProjectID.String()+"/audit-events?action=candidate.source.resolved", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("Project audit status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []struct {
			ProjectID string `json:"projectId"`
			Action    string `json:"action"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ProjectID != testProjectID.String() || payload.Items[0].Action != "candidate.source.resolved" {
		t.Fatalf("Project audit response = %#v", payload.Items)
	}

	foreign := httptest.NewRequest(http.MethodGet, "/api/v1/projects/project:foreign/audit-events?action=candidate.source.resolved", nil)
	foreign.Header.Set("Authorization", "Bearer "+token)
	foreignResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(foreignResponse, foreign)
	if foreignResponse.Code != http.StatusNotFound || strings.Contains(foreignResponse.Body.String(), "candidate.source.resolved") {
		t.Fatalf("foreign Project audit status=%d body=%s", foreignResponse.Code, foreignResponse.Body.String())
	}

	platform := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events?action=candidate.source.resolved", nil)
	platform.Header.Set("Authorization", "Bearer "+token)
	platformResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(platformResponse, platform)
	if platformResponse.Code != http.StatusOK || !strings.Contains(platformResponse.Body.String(), `"projectId":"`+testProjectID.String()+`"`) {
		t.Fatalf("platform audit status=%d body=%s", platformResponse.Code, platformResponse.Body.String())
	}

	platformOnly := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events?action=principal.theme.updated", nil)
	platformOnly.Header.Set("Authorization", "Bearer "+token)
	platformOnlyResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(platformOnlyResponse, platformOnly)
	var platformOnlyPayload struct {
		Items []struct {
			ProjectID *string `json:"projectId"`
			Action    string  `json:"action"`
		} `json:"items"`
	}
	if err := json.Unmarshal(platformOnlyResponse.Body.Bytes(), &platformOnlyPayload); err != nil {
		t.Fatal(err)
	}
	if platformOnlyResponse.Code != http.StatusOK || len(platformOnlyPayload.Items) != 1 || platformOnlyPayload.Items[0].Action != "principal.theme.updated" || platformOnlyPayload.Items[0].ProjectID != nil {
		t.Fatalf("unscoped platform audit status=%d body=%s", platformOnlyResponse.Code, platformOnlyResponse.Body.String())
	}
}

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

// Exercise the actual application router: a route registered outside its
// shared ingress must not pass this selector-fence inventory.
func TestProjectBoundarySelectorFenceCoversPublicRouteInventory(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "model"}),
	}))
	server.runtime.persistenceConfigured = true
	handler := server.Routes()
	router, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("application handler does not expose mounted routes")
	}
	mounted := make(map[string]bool)
	if err := chi.Walk(router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != "*" {
			mounted[method+" "+path] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	selected := make(map[string]bool)
	for route := range mounted {
		method, path, _ := strings.Cut(route, " ")
		metadata, found := nonAPIRouteMetadata(method, path)
		if found && (metadata.owner == "project" || metadata.owner == "dashboard" || metadata.owner == "agent") {
			selected[route] = true
		}
	}
	counts := map[string]int{"query": 0, "search": 0, "agent": 0, "release": 0}
	queryBodies := map[string]bool{
		"queryDashboardPage": false, "queryDashboardVisualData": false,
		"querySemanticModel": false, "explainSemanticModelQuery": false,
	}
	for _, contract := range apiaggregate.GetAPIGenOperationContracts() {
		category := ""
		switch {
		case contract.Path == "/api/v1/search":
			category = "search"
		case strings.HasSuffix(contract.Path, "/query") || strings.HasSuffix(contract.Path, "/query/explain"):
			category = "query"
		case strings.HasPrefix(contract.Path, "/api/v1/agent/"):
			category = "agent"
		case strings.Contains(contract.Path, "/releases"):
			category = "release"
		}
		if category != "" {
			if category == "query" {
				if _, covered := queryBodies[contract.OperationID]; !covered {
					t.Fatalf("query operation %q lacks body-selector coverage", contract.OperationID)
				}
				queryBodies[contract.OperationID] = true
			}
			if category == "search" && (contract.Method != http.MethodGet || contract.RequestBodyRequired) {
				t.Fatalf("search operation %q now accepts a request body; add body-selector evidence", contract.OperationID)
			}
			counts[category]++
			route := contract.Method + " " + contract.Path
			if !mounted[route] {
				t.Fatalf("API-02 contract route %q is not mounted", route)
			}
			selected[route] = true
		}
	}
	for category, count := range counts {
		if count == 0 {
			t.Fatalf("%s API contract inventory is empty", category)
		}
	}
	for operation, covered := range queryBodies {
		if !covered {
			t.Fatalf("query body operation %q disappeared from route inventory", operation)
		}
	}
	for _, route := range []string{"GET /", "GET /models/search", "POST /pipelines/command", "GET /chats/references/search"} {
		if !selected[route] {
			t.Fatalf("API-02 browser or agent route %q is not mounted", route)
		}
	}
	routes := make([]string, 0, len(selected))
	for route := range selected {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	parameters := regexp.MustCompile(`\{[^}]+\}`)
	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			method, path, _ := strings.Cut(route, " ")
			request := httptest.NewRequest(method, parameters.ReplaceAllString(path, "bound")+"?projectId=project:foreign", nil)
			request.Header.Set("Accept", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "PROJECT_SELECTOR_UNSUPPORTED") || strings.Contains(response.Body.String(), "project:foreign") {
				t.Fatalf("mounted route escaped Project selector fence: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProjectBoundaryRejectsGeneratedAPIRequestBodySelectors(t *testing.T) {
	store := testStore(t)
	principal := testPlatformPrincipal(t, t.Context(), store, "selector-boundary@example.com", "Selector Boundary")
	token := testAPIToken(t, t.Context(), store, principal.ID, "selector-boundary")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth:  testAuth(store, accessmodule.AuthConfig{APITokenOnly: true}),
		Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "model"}),
	}))

	for _, tc := range []struct {
		name         string
		path         string
		validBody    string
		forgedBody   string
		validStatus  int
		withSnapshot bool
	}{
		{
			name: "query", path: "/api/v1/semantic-models/test/query", validStatus: http.StatusNotFound,
			validBody:  `{"dimensions":[{"field":"orders.status","alias":"status"}],"metrics":[{"field":"order_count"}],"limit":1}`,
			forgedBody: `{"projectId":"project:foreign","dimensions":[{"field":"orders.status","alias":"status"}],"metrics":[{"field":"order_count"}],"limit":1}`,
		},
		{
			name: "query explain", path: "/api/v1/semantic-models/test/query/explain", validStatus: http.StatusOK, withSnapshot: true,
			validBody:  `{"metrics":[{"field":"order_count"}]}`,
			forgedBody: `{"projectId":"project:foreign","metrics":[{"field":"order_count"}]}`,
		},
		{
			name: "dashboard page query", path: "/api/v1/dashboards/executive-sales/pages/overview/query", validStatus: http.StatusOK, withSnapshot: true,
			validBody:  `{}`,
			forgedBody: `{"projectId":"project:foreign"}`,
		},
		{
			name: "dashboard visual query", path: "/api/v1/dashboards/executive-sales/pages/overview/visuals/order_rows/query", validStatus: http.StatusOK, withSnapshot: true,
			validBody:  `{"limit":1}`,
			forgedBody: `{"projectId":"project:foreign","limit":1}`,
		},
		{
			name: "agent", path: "/api/v1/agent/conversations", validStatus: http.StatusCreated,
			validBody:  `{"title":"Bound"}`,
			forgedBody: `{"projectId":"project:foreign","title":"Foreign"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestFor := func(body, key string) *http.Request {
				request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "application/json")
				request.Header.Set("Idempotency-Key", key)
				if tc.withSnapshot {
					request = servingSnapshotRequest(t, server, request)
				}
				return request
			}
			control := httptest.NewRecorder()
			server.Routes().ServeHTTP(control, requestFor(tc.validBody, "018f4f2e-0000-7000-8000-000000000931"))
			if control.Code != tc.validStatus {
				t.Fatalf("control status=%d body=%s, want %d", control.Code, control.Body.String(), tc.validStatus)
			}

			response := httptest.NewRecorder()
			server.Routes().ServeHTTP(response, requestFor(tc.forgedBody, "018f4f2e-0000-7000-8000-000000000932"))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want unknown Project selector rejection", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "project:foreign") {
				t.Fatalf("selector value leaked into response: %s", response.Body.String())
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
