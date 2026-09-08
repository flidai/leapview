package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	analyticsgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type addDashboardAuthoringStub struct {
	appendErr     error
	appendCalls   int
	appendRequest authoringapplication.ExplorationAppendRequest
	target        authoringapplication.ExplorationTarget
	targets       []authoringapplication.ExplorationTarget
	targetCalls   int
}

const (
	testAppendRequestID      = "01912f14-7b3c-7e31-8a74-6a6e8f9d4c20"
	testAppendIdempotencyKey = "01912f14-7b3c-7e32-8a74-6a6e8f9d4c20"
	testAppendTraceRequestID = "01912f14-7b3c-7e33-8a74-6a6e8f9d4c20"
)

func (s *addDashboardAuthoringStub) ExplorationTargets(context.Context, authoringapplication.ExplorationTargetsRequest) ([]authoringapplication.ExplorationTarget, error) {
	return s.targets, nil
}

func (s *addDashboardAuthoringStub) ExplorationTarget(context.Context, authoringapplication.ExplorationTargetRequest) (authoringapplication.ExplorationTarget, error) {
	s.targetCalls++
	if s.target.ID == "" {
		s.target = authoringapplication.ExplorationTarget{ID: "dashboard:test", SemanticModel: "semantic:sales", DraftID: "draft:test", RevisionToken: "opaque-revision"}
	}
	return s.target, nil
}

func TestDataExplorerDashboardTargetLoadsPagesOnlyAfterExplicitSelection(t *testing.T) {
	app := &addDashboardAuthoringStub{
		target: authoringapplication.ExplorationTarget{
			ID: "dashboard:test", Title: "Sales", SemanticModel: "semantic:sales", DraftID: "draft:test", RevisionToken: "opaque-revision",
			Pages: []authoringapplication.ExplorationTargetPage{{ID: "overview", Title: "Overview"}},
		},
		targets: []authoringapplication.ExplorationTarget{{ID: "dashboard:test", Title: "Sales", SemanticModel: "semantic:sales"}},
	}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	request := httptest.NewRequest(stdhttp.MethodGet, "/explore/dashboard-target/dashboard:test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != stdhttp.StatusOK || app.targetCalls != 1 {
		t.Fatalf("selected target status=%d calls=%d body=%q", response.Code, app.targetCalls, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "opaque-revision") || !strings.Contains(response.Body.String(), "overview") {
		t.Fatalf("selected target response omitted page/CAS projection: %q", response.Body.String())
	}
}

func TestDataExplorerDashboardTargetsRefreshPatchesPickerWithoutExplorerBootstrap(t *testing.T) {
	app := &addDashboardAuthoringStub{
		targets: []authoringapplication.ExplorationTarget{{ID: "dashboard:test", Title: "Sales", SemanticModel: "semantic:sales"}},
	}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/explore/dashboard-targets?model=semantic%3Asales", nil)
	h.DataExplorerDashboardTargets(recorder, request)
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("target refresh status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "dataExplorerDashboard") || !strings.Contains(body, "dashboard:test") || strings.Contains(body, `"dataExplorer":`) {
		t.Fatalf("target refresh patch=%q", body)
	}
}

func TestDashboardForkTargetsExposeOnlyAuthorizedExplicitCopyLinks(t *testing.T) {
	h := &BrowserHandler{
		Catalog: forkCatalogStub{items: []projectcatalog.Result{
			{Ref: projectcatalog.Ref{ID: "dashboard:authored", Kind: projectgraph.KindDashboard}, DisplayName: "Existing draft"},
			{Ref: projectcatalog.Ref{ID: "dashboard:project", Kind: projectgraph.KindDashboard}, DisplayName: "Project Sales"},
			{Ref: projectcatalog.Ref{ID: "dashboard:private", Kind: projectgraph.KindDashboard}, DisplayName: "Private Sales"},
		}},
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		AuthorizeDashboard: func(_ *stdhttp.Request, dashboardID string, capability access.Capability) (bool, error) {
			return capability == access.CapabilityResourceEdit && dashboardID == "dashboard:project", nil
		},
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/explore", nil)
	targets, overflow := h.dashboardForkTargets(request, []authoringapplication.ExplorationTarget{{ID: "dashboard:authored"}})
	if len(targets) != 1 || targets[0].ID != "dashboard:project" || targets[0].ForkHref != "/dashboards/dashboard:project/fork" {
		t.Fatalf("fork targets = %#v", targets)
	}
	if overflow {
		t.Fatal("small fork target list reported overflow")
	}
}

type forkCatalogStub struct{ items []projectcatalog.Result }

func (s forkCatalogStub) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	return projectcatalog.Page{Items: s.items}, nil
}

func (s forkCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

func (s *addDashboardAuthoringStub) AppendExploration(_ context.Context, request authoringapplication.ExplorationAppendRequest) (authoringservice.Result, error) {
	s.appendCalls++
	s.appendRequest = request
	if s.appendErr != nil {
		return authoringservice.Result{}, s.appendErr
	}
	return authoringservice.Result{}, nil
}

func TestDataExplorerAddToDashboardDelegatesAuthorizationAndModelAdmission(t *testing.T) {
	app := &addDashboardAuthoringStub{appendErr: context.Canceled}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	request := addDashboardRequest(t, addExplorationToDashboardCommand{DashboardID: "dashboard:test", Spec: exploration.ExplorationSpec{ModelID: "secret:model"}})
	h.DataExplorerAddToDashboard(recorder, request)
	if recorder.Code != stdhttp.StatusConflict || app.appendCalls != 1 {
		t.Fatalf("denied add status=%d append=%d body=%q", recorder.Code, app.appendCalls, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "context canceled") || strings.Contains(recorder.Body.String(), "secret") {
		t.Fatalf("append failure leaked internal detail: %q", recorder.Body.String())
	}
}

func TestDataExplorerAddToDashboardPassesOpaqueTargetToAtomicApplication(t *testing.T) {
	app := &addDashboardAuthoringStub{}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	command := addExplorationToDashboardCommand{
		DashboardID: "dashboard:test", RevisionToken: "opaque-revision", PageID: "overview", PlacementChoice: "half",
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{{Field: "status_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
	}
	recorder := httptest.NewRecorder()
	h.DataExplorerAddToDashboard(recorder, addDashboardRequest(t, command))
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("add status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if app.appendCalls != 1 {
		t.Fatalf("application calls append=%d", app.appendCalls)
	}
	if app.appendRequest.DashboardID != dashboardauthoring.DashboardID(command.DashboardID) || app.appendRequest.PageID != command.PageID || app.appendRequest.RevisionToken != command.RevisionToken || app.appendRequest.PlacementChoice != command.PlacementChoice || app.appendRequest.Spec.ModelID != command.Spec.ModelID || app.appendRequest.RequestID != testAppendRequestID {
		t.Fatalf("append request = %#v", app.appendRequest)
	}
}

func TestDataExplorerAddToDashboardPrefersDurableIdempotencyKey(t *testing.T) {
	app := &addDashboardAuthoringStub{}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	command := addExplorationToDashboardCommand{
		DashboardID: "dashboard:test", RevisionToken: "opaque-revision", PageID: "overview", PlacementChoice: "half",
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{{Field: "status_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
	}
	request := addDashboardRequest(t, command)
	request.Header.Set("X-Request-ID", testAppendTraceRequestID)
	request.Header.Set("Idempotency-Key", testAppendIdempotencyKey)
	recorder := httptest.NewRecorder()
	h.DataExplorerAddToDashboard(recorder, request)
	if recorder.Code != stdhttp.StatusOK || app.appendCalls != 1 {
		t.Fatalf("add status=%d append=%d body=%q", recorder.Code, app.appendCalls, recorder.Body.String())
	}
	if app.appendRequest.RequestID != testAppendIdempotencyKey {
		t.Fatalf("durable append identity=%q, want %q", app.appendRequest.RequestID, testAppendIdempotencyKey)
	}
}

func TestDataExplorerAddToDashboardRejectsMiddlewareGeneratedRequestID(t *testing.T) {
	app := &addDashboardAuthoringStub{}
	h := &BrowserHandler{
		DashboardAuthoring:        app,
		DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(),
		ResolveProjectID:          func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:               func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	command := addExplorationToDashboardCommand{
		DashboardID: "dashboard:test", RevisionToken: "opaque-revision", PageID: "overview", PlacementChoice: "half",
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{{Field: "status_count"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
	}
	request := addDashboardRequest(t, command)
	request.Header.Del("X-Request-ID")
	request.Header.Del("Idempotency-Key")
	recorder := httptest.NewRecorder()
	httpmiddleware.RequestCorrelation(stdhttp.HandlerFunc(h.DataExplorerAddToDashboard)).ServeHTTP(recorder, request)
	if recorder.Code != stdhttp.StatusBadRequest || app.appendCalls != 0 {
		t.Fatalf("generated request identity status=%d append=%d body=%q", recorder.Code, app.appendCalls, recorder.Body.String())
	}
}

func TestDataExplorerAddToDashboardDoesNotInspectTargetModelInTransport(t *testing.T) {
	app := &addDashboardAuthoringStub{target: authoringapplication.ExplorationTarget{ID: "dashboard:test", SemanticModel: "semantic:other", DraftID: "draft:test", RevisionToken: "opaque-revision"}}
	h := &BrowserHandler{DashboardAuthoring: app, DashboardAuthoringCommand: analyticsgen.GenUIActionExecuteDashboardAuthoringCommand(), ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true }}
	command := addExplorationToDashboardCommand{DashboardID: "dashboard:test", RevisionToken: "opaque-revision", PageID: "overview", PlacementChoice: "half", Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Metrics: []exploration.ExplorationMetricRef{{Field: "status_count"}}, Filters: []exploration.ExplorationFilter{}, Limit: 10}}
	recorder := httptest.NewRecorder()
	h.DataExplorerAddToDashboard(recorder, addDashboardRequest(t, command))
	if recorder.Code != stdhttp.StatusOK || app.appendCalls != 1 {
		t.Fatalf("mismatched model status=%d append=%d", recorder.Code, app.appendCalls)
	}
}

func addDashboardRequest(t *testing.T, command addExplorationToDashboardCommand) *stdhttp.Request {
	t.Helper()
	body, err := json.Marshal(addExplorationToDashboardSignal{AddExplorationToDashboard: command})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/explore/add-to-dashboard", bytes.NewReader(body))
	request.Header.Set("X-Request-ID", testAppendRequestID)
	request.Header.Set(uicommand.HeaderOperationID, analyticsgen.GenUIActionExecuteDashboardAuthoringCommand().OperationID())
	return request
}
