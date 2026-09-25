package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
)

const pipelineDetailHTTPProjectID = "project:test"
const pipelineDetailHTTPAssetID = "pipeline:daily"

func TestPipelineDetailDocumentsAndTypedUpdatesStaySectionScoped(t *testing.T) {
	h := newPipelineDetailHTTPTestHandler()
	router := chi.NewRouter()
	h.MountAuthenticated(router)

	for _, test := range []struct {
		path    string
		section string
	}{
		{path: "/pipelines/pipeline:daily", section: "overview"},
		{path: "/pipelines/pipeline:daily/runs", section: "runs"},
		{path: "/pipelines/pipeline:daily/definition", section: "definition"},
	} {
		t.Run(test.section, func(t *testing.T) {
			document := httptest.NewRecorder()
			router.ServeHTTP(document, httptest.NewRequest(stdhttp.MethodGet, test.path, nil))
			if document.Code != stdhttp.StatusOK {
				t.Fatalf("document status = %d, want 200: %s", document.Code, document.Body.String())
			}
			body := document.Body.String()
			for _, want := range []string{
				"route=pipeline_detail", "surface=pipeline_detail", "environment=production",
				"asset=pipeline%3Adaily", "section=" + test.section,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("document updates bootstrap URL missing %q:\n%s", want, body)
				}
			}

			values := url.Values{
				"route":       {"pipeline_detail"},
				"surface":     {"pipeline_detail"},
				"environment": {"production"},
				"asset":       {pipelineDetailHTTPAssetID},
				"section":     {test.section},
			}
			streamBody := pipelineDetailHTTPUpdates(t, h, values)
			if !strings.Contains(streamBody, `"kind":"pipeline_detail"`) || !strings.Contains(streamBody, `"activeTab":"`+test.section+`"`) {
				t.Fatalf("typed bootstrap for %s = %s, want matching pipeline detail section", test.section, streamBody)
			}
		})
	}
}

func TestPipelineDetailReadAuthorizationUnknownAssetAndInvalidSection(t *testing.T) {
	denied := newPipelineDetailHTTPTestHandler()
	denied.CurrentUser = func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "reader"}, true }
	denied.Catalog = pipelineDetailReadCatalog{allowPipelineRead: false}
	router := chi.NewRouter()
	denied.MountAuthenticated(router)
	for name, request := range map[string]*stdhttp.Request{
		"document": httptest.NewRequest(stdhttp.MethodGet, "/pipelines/pipeline:daily/overview", nil),
		"updates":  httptest.NewRequest(stdhttp.MethodGet, "/updates?route=pipeline_detail&surface=pipeline_detail&asset=pipeline%3Adaily&section=overview", nil),
	} {
		t.Run("denied "+name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if name == "document" {
				router.ServeHTTP(response, request)
			} else {
				denied.Updates(response, request)
			}
			if response.Code != stdhttp.StatusForbidden {
				t.Fatalf("denied %s status = %d, want 403: %s", name, response.Code, response.Body.String())
			}
		})
	}

	allowed := newPipelineDetailHTTPTestHandler()
	allowedRouter := chi.NewRouter()
	allowed.MountAuthenticated(allowedRouter)
	for name, path := range map[string]string{
		"unknown pipeline": "/pipelines/pipeline:missing/overview",
		"invalid section":  "/pipelines/pipeline:daily/not-a-section",
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			allowedRouter.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, path, nil))
			if response.Code != stdhttp.StatusNotFound {
				t.Fatalf("%s status = %d, want 404: %s", name, response.Code, response.Body.String())
			}
		})
	}
}

func TestStaticPipelineRunsRouteWinsOverPipelineDetailAssetRoute(t *testing.T) {
	h := newPipelineDetailHTTPTestHandler()
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, "/pipelines/runs", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("static run monitor status = %d, want 200: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "<title>Runs</title>") || !strings.Contains(body, "route=pipelines") || !strings.Contains(body, "view=runs") || !strings.Contains(body, "<lv-pipelines-page") {
		t.Fatalf("static route did not render the pipeline run monitor shell:\n%s", body)
	}
}

func TestPipelineDetailRunsFixesPipelineAndPreservesRunMonitorFilters(t *testing.T) {
	h := newPipelineDetailHTTPTestHandler()
	monitor := h.RunMonitor.(*pipelineDetailHTTPRunMonitor)
	monitor.page = refreshrun.MonitorPage{
		Total: 31,
		Runs: []refreshrun.RunRecord{{
			ID: "run:failed", Identity: projectgraph.ServingIdentity{ProjectID: pipelineDetailHTTPProjectID, Environment: "production", GenerationID: "state:production"},
			PipelineID: pipelineDetailHTTPAssetID, SemanticModelID: "semantic_model:sales", TargetType: refreshrun.TargetRefreshPipeline,
			TriggerType: "schedule", Status: "failed", CreatedAt: "2026-09-22T12:00:00Z", StartedAt: "2026-09-22T12:00:03Z", Error: "source unavailable",
		}},
	}
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, "/pipelines/pipeline:daily/runs?q=failed&range=7d&status=failed&trigger=schedule&page=2", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("pipeline runs status = %d, want 200: %s", response.Code, response.Body.String())
	}
	filter := monitor.filter
	if filter == nil {
		t.Fatal("pipeline-scoped Runs did not query the shared run monitor")
	}
	if filter.Search != "failed" || filter.Status != "failed" || filter.Trigger != "schedule" || filter.Limit != 25 || filter.Offset != 25 {
		t.Fatalf("pipeline run monitor filter = %#v, want search/status/trigger and page 2", filter)
	}
	if len(filter.PipelineIDs) != 1 || filter.PipelineIDs[0] != pipelineDetailHTTPAssetID || len(filter.AllowedPipelineIDs) != 1 || filter.AllowedPipelineIDs[0] != pipelineDetailHTTPAssetID {
		t.Fatalf("pipeline run monitor scope = %#v / %#v, want only the route pipeline", filter.PipelineIDs, filter.AllowedPipelineIDs)
	}
	body := response.Body.String()
	for _, want := range []string{"range=7d", "status=failed", "trigger=schedule", "page=2"} {
		if !strings.Contains(body, want) {
			t.Errorf("scoped Runs response missing %q", want)
		}
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/pipelines/pipeline:daily/runs?q=failed&range=7d&status=failed&trigger=schedule&page=2", nil)
	_, state, err := h.pipelineDetailPageState(request, pipelineDetailHTTPAssetID, "runs")
	if err != nil {
		t.Fatalf("pipeline Runs projection failed: %v", err)
	}
	if state.RunMonitor == nil || state.RunMonitor.Pipeline != pipelineDetailHTTPAssetID || state.RunMonitor.Range != "7d" || state.RunMonitor.Status != "failed" || state.RunMonitor.Trigger != "schedule" || state.RunMonitor.Page != 2 || state.RunMonitor.Total != 31 {
		t.Fatalf("pipeline Runs state = %#v, want the fixed pipeline and requested monitor filters", state.RunMonitor)
	}
	if len(state.MonitorRuns) != 1 || state.MonitorRuns[0].Run.ID != "run:failed" {
		t.Fatalf("pipeline Runs rows = %#v, want the matching monitor record", state.MonitorRuns)
	}
}

func pipelineDetailHTTPUpdates(t *testing.T, h *BrowserHandler, values url.Values) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/updates?"+values.Encode(), nil)
	recorder := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(recorder, request)
	}()
	select {
	case <-recorder.wrote:
	case <-time.After(time.Second):
		t.Fatal("typed pipeline updates did not write a bootstrap patch")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pipeline updates stream did not stop after cancellation")
	}
	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("pipeline updates status = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func newPipelineDetailHTTPTestHandler() *BrowserHandler {
	return &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: pipelineDetailHTTPAssetID, ProjectID: pipelineDetailHTTPProjectID, ServingStateID: "state:production",
			Type: "refresh_pipeline", Key: "daily", Title: "Daily refresh", PayloadJSON: `{}`,
		}}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.ResourceManifest{
			RefreshPipelines: map[string]refreshschedule.Definition{
				pipelineDetailHTTPAssetID: {ID: pipelineDetailHTTPAssetID, Name: "daily"},
			},
			AuthoredResourceSources: map[string]string{pipelineDetailHTTPAssetID: "apiVersion: leapview.dev/v1\nkind: Pipeline\n"},
		}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return pipelineDetailHTTPProjectID, nil },
		Environment:      "production",
		RunMonitor:       &pipelineDetailHTTPRunMonitor{},
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
}

type pipelineDetailReadCatalog struct{ allowPipelineRead bool }

func (c pipelineDetailReadCatalog) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	return projectcatalog.Page{}, nil
}

func (c pipelineDetailReadCatalog) Resolve(_ context.Context, _ string, ref projectcatalog.Ref, capability access.Capability, _ bool) (projectcatalog.Result, error) {
	if c.allowPipelineRead && ref.Kind == projectgraph.KindPipeline && capability == access.CapabilityResourceRead {
		return projectcatalog.Result{Ref: ref}, nil
	}
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type pipelineDetailHTTPRunMonitor struct {
	filter *refreshrun.MonitorFilter
	page   refreshrun.MonitorPage
}

func (m *pipelineDetailHTTPRunMonitor) MonitorRuns(_ context.Context, _ projectgraph.ResourceID, _ string, filter refreshrun.MonitorFilter) (refreshrun.MonitorPage, error) {
	m.filter = &filter
	return m.page, nil
}
