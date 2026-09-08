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
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	"github.com/flidai/leapview/internal/dashboard/usage"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type dashboardEntryAuthoringFake struct {
	*builderAuthoringFake
	draft    application.DraftRead
	draftErr error
}

func (f *dashboardEntryAuthoringFake) Draft(context.Context, application.DraftRequest) (application.DraftRead, error) {
	return f.draft, f.draftErr
}

type fakeMetrics struct{}

func (fakeMetrics) ExecuteConsumersPage(_ context.Context, _ consumer.Request, _ consumer.Publisher) error {
	return nil
}

func (fakeMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "project", Title: "Workspace"}}
}
func (fakeMetrics) DefaultDashboardID() string {
	return "dash"
}
func (m fakeMetrics) Resolver() dashboardresolver.Resolver {
	return fakeDashboardResolver{}
}

type fakeDashboardResolver struct{}

func (fakeDashboardResolver) Resolve(dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if dashboardID != "dash" {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	model := &semanticmodel.Model{Name: "model", Title: "Model"}
	definition, err := dashboarddefinition.New("dash", "Dashboard", "", "model", fakeMetrics{}.Pages(dashboardID.String()), nil)
	if err != nil {
		return dashboardresolver.Resolved{}, err
	}
	return dashboardresolver.Resolved{Definition: definition, Model: model, Source: dashboardresolver.SourceMetadata{Kind: dashboardresolver.SourceProject, Identity: projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "generation"}}}, nil
}

type instanceMetrics struct{ fakeMetrics }

func (instanceMetrics) Resolver() dashboardresolver.Resolver { return instanceDashboardResolver{} }

type instanceDashboardResolver struct{}

func (instanceDashboardResolver) Resolve(dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	resolved, err := (fakeDashboardResolver{}).Resolve(dashboardID)
	resolved.Source.Kind = dashboardresolver.SourceInstance
	resolved.Source.AuthoredRevision = dashboardresolver.AuthoredRevisionEvidence{ID: "revision-7", Number: 7, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	return resolved, err
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
func (fakeMetrics) QueryDashboardPage(_ context.Context, _ string, _ string, filters dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{Filters: filters.WithDefaults()}, nil
}
func (fakeMetrics) QueryVisualizationWindow(_ context.Context, _, _ string, _ dashboard.Filters, _ visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return visualizationir.VisualizationEnvelope{}, nil
}
func TestDashboardRedirectsToFirstPage(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash", nil)

	testRouter(Handler{Metrics: fakeMetrics{}}).ServeHTTP(rec, req)

	if rec.Code != nethttp.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/dashboards/dash/pages/overview" {
		t.Fatalf("Location = %q", got)
	}
}

func TestPageNotFound(t *testing.T) {
	for _, path := range []string{"/dashboards/missing/pages/overview", "/dashboards/dash/pages/missing"} {
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
	req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/pages/overview", nil)

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

func TestPublishedDashboardUsesContextualAuthoringEntryAction(t *testing.T) {
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "alice"}
	publishedToken := authoring.RevisionToken{RevisionID: "revision-7", Number: 7, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	draftToken := authoring.RevisionToken{RevisionID: "revision-8", Number: 8, ContentHash: "sha256:" + strings.Repeat("b", 64)}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project", ID: "dash", OwnerPrincipalID: "alice", Slug: "dashboard", Title: "Dashboard", SemanticModel: "model",
		Visibility: authoring.VisibilityPrivate,
		Draft:      &authoring.Draft{ID: "draft-7", DashboardID: "dash", Revision: draftToken, Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Status = authoring.LifecycleStatusPublished
	lifecycle.Published = &authoring.Published{Revision: publishedToken, Compilation: authoring.CompiledRevisionToken{AuthoredRevision: publishedToken, DefinitionHash: "sha256:" + strings.Repeat("c", 64), SemanticModelID: "model", SemanticIdentity: projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "generation"}}, PublishedAt: time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC), Provenance: provenance}
	entry := &dashboardEntryAuthoringFake{
		builderAuthoringFake: &builderAuthoringFake{},
		draft:                application.DraftRead{Lifecycle: lifecycle},
	}
	handler := Handler{Metrics: instanceMetrics{}, ProjectID: "project", Authoring: entry, CurrentPrincipalID: func(*nethttp.Request) string { return "alice" }}
	rec := httptest.NewRecorder()
	handler.RenderPage(rec, httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/pages/overview", nil), "dash", "overview")
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, `authoring-action-label="Continue editing"`) || !strings.Contains(body, `authoring-action-href="/dashboards/dash/edit?draft=draft-7&page=overview"`) {
		t.Fatalf("missing contextual continue action: %s", body)
	}
}

func TestProjectDashboardUsesForkEntryAction(t *testing.T) {
	entry := &dashboardEntryAuthoringFake{
		builderAuthoringFake: &builderAuthoringFake{},
	}
	handler := Handler{Metrics: fakeMetrics{}, ProjectID: "project", Authoring: entry, CurrentPrincipalID: func(*nethttp.Request) string { return "viewer" }}
	rec := httptest.NewRecorder()
	handler.RenderPage(rec, httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/pages/overview", nil), "dash", "overview")
	body := html.UnescapeString(rec.Body.String())
	if !strings.Contains(body, `authoring-action-label="Make a copy"`) || !strings.Contains(body, `authoring-action-href="/dashboards/dash/fork"`) {
		t.Fatalf("missing contextual copy action: %s", body)
	}
}

func TestUpdatesPreservesDrawerAgentStateOnReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	currentSignals := `{"agent":{"activeConversationId":"conversation-1"},"agentVisuals":{"chart":{"title":"Current result"}}}`
	req := httptest.NewRequestWithContext(ctx, nethttp.MethodGet, "/updates?project=workspace&dashboard=dash&page=overview&datastar="+url.QueryEscape(currentSignals), nil)
	rec := httptest.NewRecorder()
	bootstrapCalls := 0
	handler := Handler{
		Metrics: fakeMetrics{}, ProjectID: "workspace",
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
		Metrics: fakeMetrics{}, ProjectID: "workspace", SessionStore: dashboardsession.NewMemoryStore(),
		CurrentUsagePrincipal: func(*nethttp.Request) (string, bool) { return "alice", true },
		RecordDashboardView: func(_ context.Context, view usage.View) error {
			views = append(views, view)
			return nil
		},
	}
	path := "/updates?project=workspace&dashboard=dash&page=overview&clientId=client&streamInstance=stream"
	for range 2 {
		req := httptest.NewRequestWithContext(ctx, nethttp.MethodGet, path, nil)
		handler.Updates(httptest.NewRecorder(), req)
	}
	if len(views) != 1 {
		t.Fatalf("recorded views = %#v, want one new-session view", views)
	}
	if got := views[0]; got.ProjectID != "workspace" || got.DashboardID != "dash" || got.PageID != "overview" || got.PrincipalID != "alice" {
		t.Fatalf("recorded view = %#v", got)
	}
}

func testRouter(handler Handler) nethttp.Handler {
	r := chi.NewRouter()
	r.Get("/dashboards/{dashboard}", handler.Dashboard)
	r.Get("/dashboards/{dashboard}/pages/{page}", handler.Page)
	return r
}
