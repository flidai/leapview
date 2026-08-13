package http

import (
	"context"
	"html"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	"github.com/flidai/leapview/internal/dashboard/usage"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
	"github.com/go-chi/chi/v5"
)

type fakeMetrics struct{}

func (fakeMetrics) ExecuteConsumersPage(_ context.Context, _ consumer.Request, _ consumer.Publisher) error {
	return nil
}

func (fakeMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{Workspace: catalog.Workspace{ID: "workspace", Title: "Workspace"}}
}
func (fakeMetrics) DefaultDashboardID() string {
	return "dash"
}
func (fakeMetrics) DefaultFilters(string) dashboard.Filters {
	return dashboard.Filters{}.WithDefaults()
}
func (fakeMetrics) ModelIDForDashboard(string) string {
	return "model"
}
func (fakeMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	return request.WithDefaults()
}
func (fakeMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != "dash" {
		return nil
	}
	return []dashboard.Page{{ID: "overview", Title: "Overview"}, {ID: "ops", Title: "Ops"}}
}
func (fakeMetrics) Report(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if dashboardID != "dash" {
		return dashboarddefinition.Definition{}, nil, false
	}
	report := reportdef.Dashboard{
		ID:            "dash",
		Title:         "Dashboard",
		SemanticModel: "model",
		Visuals:       map[string]reportdef.AuthoringVisualization{},
		Pages:         fakeMetrics{}.Pages(dashboardID),
	}
	model := &semanticmodel.Model{Name: "model", Title: "Model"}
	return dashboardfixture.Compile(report, model), model, true
}
func (fakeMetrics) VisualizationDefinition(string, string) (visualizationdefinition.Definition, bool) {
	return visualizationdefinition.Definition{}, false
}
func (fakeMetrics) QueryDashboardPage(_ context.Context, _ string, _ string, filters dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{Filters: filters.WithDefaults()}, nil
}
func (fakeMetrics) QueryVisualizationWindow(_ context.Context, _, _ string, _ dashboard.Filters, _ visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, nil
}
func TestDashboardRedirectsToFirstPage(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodGet, "/workspaces/workspace/dashboards/dash", nil)

	testRouter(Handler{Metrics: fakeMetrics{}}).ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/workspaces/workspace/dashboards/dash/pages/overview" {
		t.Fatalf("Location = %q", got)
	}
}

func TestPageNotFound(t *testing.T) {
	for _, path := range []string{"/workspaces/workspace/dashboards/missing/pages/overview", "/workspaces/workspace/dashboards/dash/pages/missing"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(nethttp.MethodGet, path, nil)

			testRouter(Handler{Metrics: fakeMetrics{}}).ServeHTTP(rec, req)

			if rec.Code != nethttp.StatusNotFound {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestPageSetsClientCookieAndRendersReport(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodGet, "/workspaces/workspace/dashboards/dash/pages/overview", nil)

	testRouter(Handler{Metrics: fakeMetrics{}}).ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != "pagestream_client_id" || cookies[0].Value == "" {
		t.Fatalf("cookies = %#v", cookies)
	}
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, `<lv-dashboard-page`) || !strings.Contains(body, `/updates?dashboard=dash`) || !strings.Contains(body, `route=dashboard`) || !strings.Contains(body, `@get('/updates?`) || strings.Contains(body, `data-signals=`) {
		t.Fatalf("page did not render report shell:\n%s", body)
	}
	if strings.Contains(body, `<lv-report-canvas`) {
		t.Fatalf("page rendered dashboard internals in Go shell:\n%s", body)
	}
}

func TestUpdatesPreservesDrawerAgentStateOnReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	currentSignals := `{"agent":{"activeConversationId":"conversation-1"},"agentVisuals":{"chart":{"title":"Current result"}}}`
	req := httptest.NewRequestWithContext(ctx, nethttp.MethodGet, "/updates?workspace=workspace&dashboard=dash&page=overview&datastar="+url.QueryEscape(currentSignals), nil)
	rec := httptest.NewRecorder()
	bootstrapCalls := 0
	handler := Handler{
		Metrics: fakeMetrics{},
		AgentBootstrap: func(*nethttp.Request, string) reportui.AgentBootstrap {
			bootstrapCalls++
			return reportui.AgentBootstrap{}
		},
	}

	handler.Updates(rec, req)

	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) == 0 {
		t.Fatal("updates did not emit a bootstrap patch")
	}
	if _, exists := patches[0]["agent"]; exists {
		t.Fatalf("reconnect bootstrap replaced current agent signal: %#v", patches[0]["agent"])
	}
	if _, exists := patches[0]["agentVisuals"]; exists {
		t.Fatalf("reconnect bootstrap replaced current agent visuals: %#v", patches[0]["agentVisuals"])
	}
	if bootstrapCalls != 0 {
		t.Fatalf("AgentBootstrap calls = %d, want 0 on reconnect", bootstrapCalls)
	}
}

func TestUpdatesRecordsOneHumanViewForNewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	views := []usage.View{}
	handler := Handler{
		Metrics: fakeMetrics{}, SessionStore: dashboardsession.NewMemoryStore(),
		CurrentUsagePrincipal: func(*nethttp.Request) (string, bool) { return "alice", true },
		RecordDashboardView: func(_ context.Context, view usage.View) error {
			views = append(views, view)
			return nil
		},
	}
	path := "/updates?workspace=workspace&dashboard=dash&page=overview&clientId=client&streamInstance=stream"
	for range 2 {
		req := httptest.NewRequestWithContext(ctx, nethttp.MethodGet, path, nil)
		handler.Updates(httptest.NewRecorder(), req)
	}
	if len(views) != 1 {
		t.Fatalf("recorded views = %#v, want one new-session view", views)
	}
	if got := views[0]; got.WorkspaceID != "workspace" || got.DashboardID != "dash" || got.PageID != "overview" || got.PrincipalID != "alice" {
		t.Fatalf("recorded view = %#v", got)
	}
}

func testRouter(handler Handler) nethttp.Handler {
	r := chi.NewRouter()
	r.Get("/workspaces/{workspace}/dashboards/{dashboard}", handler.Dashboard)
	r.Get("/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}", handler.Page)
	return r
}
