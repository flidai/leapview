package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/go-chi/chi/v5"
)

func TestPipelineRunModelsUseOnlyDurableInScopeChildRuns(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	root := refreshrun.RunRecord{ID: "root-run", Identity: identity, PipelineID: "pipeline:sales"}
	children := []refreshrun.RunRecord{
		{ID: "failed", ParentRunID: root.ID, Identity: identity, PipelineID: root.PipelineID, TargetType: refreshrun.TargetModel, TargetID: "model:failed", Status: refreshrun.RunStatusFailed, Error: "model query failed", StartedAt: "2026-09-21T13:00:00Z", FinishedAt: "2026-09-21T13:00:05Z"},
		{ID: "prepared", ParentRunID: root.ID, Identity: identity, PipelineID: root.PipelineID, TargetType: refreshrun.TargetModel, TargetID: "model:prepared", Status: refreshrun.RunStatusPrepared},
		{ID: "wrong-parent", ParentRunID: "other-run", Identity: identity, PipelineID: root.PipelineID, TargetType: refreshrun.TargetModel, TargetID: "model:queued", Status: refreshrun.RunStatusRunning},
		{ID: "out-of-scope", ParentRunID: root.ID, Identity: identity, PipelineID: root.PipelineID, TargetType: refreshrun.TargetModel, TargetID: "model:other", Status: refreshrun.RunStatusSucceeded},
		{ID: "wrong-generation", ParentRunID: root.ID, Identity: projectgraph.ServingIdentity{ProjectID: identity.ProjectID, Environment: identity.Environment, GenerationID: "generation:other"}, PipelineID: root.PipelineID, TargetType: refreshrun.TargetModel, TargetID: "model:failed", Status: refreshrun.RunStatusSucceeded},
	}
	models := pipelineRunModelsFrom([]string{"model:failed", "model:prepared", "model:queued"}, root, children)
	if len(models) != 3 {
		t.Fatalf("models = %#v; want recorded scope only", models)
	}
	if models[0].Status == nil || *models[0].Status != refreshrun.RunStatusFailed || models[0].Error == nil || *models[0].Error != "model query failed" {
		t.Fatalf("failed model evidence = %#v", models[0])
	}
	if models[0].Duration == nil || *models[0].Duration != "5s" {
		t.Fatalf("durable child duration = %v; want 5s", models[0].Duration)
	}
	if models[1].Status == nil || *models[1].Status != refreshrun.RunStatusPrepared || models[1].StatusLabel == nil || *models[1].StatusLabel != "Ready to publish" {
		t.Fatalf("prepared model evidence = %#v; want durable prepared status labeled Ready to publish", models[1])
	}
	if models[2].Status != nil || models[2].Error != nil {
		t.Fatalf("missing durable child outcome was invented: %#v", models[2])
	}
}

func TestPipelineRunDetailKeepsValidationOutcomeSeparateFromMissingTiming(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	run := refreshrun.RunRecord{
		ID: "run:failed", Identity: identity, SemanticModelID: "semantic-model:sales", PipelineID: "pipeline:sales-refresh",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline:sales-refresh", Status: refreshrun.RunStatusFailed,
		PlanDigest: "plan-digest", MaterializationScope: []string{"model:sales"}, Error: "refresh execution failed",
	}
	child := refreshrun.RunRecord{
		ID: "child:failed", ParentRunID: run.ID, Identity: identity, PipelineID: run.PipelineID,
		TargetType: refreshrun.TargetModel, TargetID: "model:sales", Status: refreshrun.RunStatusFailed,
		StartedAt: "2026-09-21T13:00:00Z", FinishedAt: "2026-09-21T13:00:05Z", Error: "refresh execution failed",
	}
	reader := pipelineRunDetailReaderStub{
		run: run, childRuns: []refreshrun.RunRecord{child}, attempts: map[string]refreshrun.RunAttemptPage{
			run.ID:   {Attempts: []refreshrun.RunAttemptRecord{{Number: 1, Status: "failed", ClaimedAt: "2026-09-21T12:59:59Z", StartedAt: "2026-09-21T13:00:00Z", FinishedAt: "2026-09-21T13:00:06Z", Error: "refresh execution failed"}}},
			child.ID: {Attempts: []refreshrun.RunAttemptRecord{}},
		},
	}
	h := &BrowserHandler{
		RunDetailReader:      reader,
		RunPublicationReader: pipelineRunPublicationReaderStub{},
		ResolveProjectID:     func(context.Context) (projectgraph.ResourceID, error) { return identity.ProjectID, nil },
		Environment:          identity.Environment,
		CurrentUser:          func(*http.Request) (Principal, bool) { return Principal{ID: "alice", DevBypass: true}, true },
	}
	r := httptest.NewRequest(http.MethodGet, "/?asset=pipeline:sales-refresh&run=run:failed", nil)
	data, err := h.pipelineRunDocumentData(r)
	if err != nil {
		t.Fatalf("pipelineRunDocumentData() error = %v", err)
	}
	if data.Page.PipelineTitle != "sales-refresh" {
		t.Fatalf("pipeline breadcrumb title = %q, want canonical key without resource-kind prefix", data.Page.PipelineTitle)
	}
	if data.Page.Execution.ValidationOutcome != "unknown" || data.Page.Execution.ValidationTimingAvailable {
		t.Fatalf("validation signal = %q timing available %v; no persisted validator evidence should remain unknown with no timing", data.Page.Execution.ValidationOutcome, data.Page.Execution.ValidationTimingAvailable)
	}
	if data.Page.Execution.PublicationOutcome != "not_published" {
		t.Fatalf("publication outcome = %q, want not_published", data.Page.Execution.PublicationOutcome)
	}
	if len(data.Page.Execution.Attempts) != 1 || data.Page.Execution.Attempts[0].Status != "failed" || data.Page.Execution.Attempts[0].Duration == nil || *data.Page.Execution.Attempts[0].Duration != "6s" {
		t.Fatalf("root attempt evidence = %#v", data.Page.Execution.Attempts)
	}
	if len(data.Page.Execution.Models) != 1 || data.Page.Execution.Models[0].Status == nil || *data.Page.Execution.Models[0].Status != refreshrun.RunStatusFailed || data.Page.Execution.Models[0].Error == nil || *data.Page.Execution.Models[0].Error != child.Error {
		t.Fatalf("model failure evidence = %#v", data.Page.Execution.Models)
	}
	if len(data.Page.Execution.Models[0].Attempts) != 0 {
		t.Fatalf("model attempts = %#v, want empty because no persisted child attempt was supplied", data.Page.Execution.Models[0].Attempts)
	}
}

func TestPipelineRunBreadcrumbPrefersAuthoredPipelineTitle(t *testing.T) {
	pipelineID := projectgraph.ResourceID("pipeline:sales-refresh")
	h := &BrowserHandler{ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.ResourceManifest{
		RefreshPipelines: map[string]refreshschedule.Definition{pipelineID.String(): {ID: pipelineID, Name: "Sales refresh"}},
	}}}
	if got := h.currentPipelineTitle(t.Context(), pipelineID); got != "Sales refresh" {
		t.Fatalf("authored pipeline breadcrumb title = %q, want configured name", got)
	}
	h.ProjectDefinitionReader = nil
	if got := h.currentPipelineTitle(t.Context(), pipelineID); got != "sales-refresh" {
		t.Fatalf("pipeline breadcrumb fallback = %q, want key without resource-kind prefix", got)
	}
}

func TestPipelineRunPublicationOutcomeRequiresCommittedEvidence(t *testing.T) {
	if got := pipelineRunPublicationOutcome(refreshrun.RunStatusSucceeded, true, nil); got != "published" {
		t.Fatalf("confirmed publication outcome = %q", got)
	}
	if got := pipelineRunPublicationOutcome(refreshrun.RunStatusSucceeded, false, nil); got != "unverified" {
		t.Fatalf("unconfirmed publication for successful run = %q; success alone is not publication evidence", got)
	}
	if got := pipelineRunPublicationOutcome(refreshrun.RunStatusRunning, false, nil); got != "pending" {
		t.Fatalf("active publication outcome = %q", got)
	}
	if got := pipelineRunPublicationOutcome(refreshrun.RunStatusSucceeded, false, errors.New("reader unavailable")); got != "unverified" {
		t.Fatalf("unavailable publication reader outcome = %q", got)
	}
	if got := pipelineRunPublicationOutcome(refreshrun.RunStatusFailed, false, nil); got != "not_published" {
		t.Fatalf("terminal failure outcome = %q", got)
	}
}

func TestPipelineRunPageDegradesWhenOptionalReadersFail(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	run := refreshrun.RunRecord{
		ID: "run:one", Identity: identity, SemanticModelID: "semantic-model:sales", PipelineID: "pipeline:sales",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline:sales", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusPrepared, PlanDigest: "plan-digest", MaterializationScope: []string{"model:sales"},
	}
	h := &BrowserHandler{
		RunDetailReader:  pipelineRunDetailReaderStub{run: run, childRunsErr: errors.New("child store unavailable"), attemptsErr: errors.New("attempt store unavailable")},
		RunEventReader:   pipelineRunEventReaderStub{err: errors.New("event store unavailable")},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return identity.ProjectID, nil },
		Environment:      identity.Environment,
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "alice", DevBypass: true}, true },
	}
	r := httptest.NewRequest(http.MethodGet, "/pipelines/pipeline%3Asales/runs/run%3Aone/details", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("asset", "pipeline%3Asales")
	routeContext.URLParams.Add("run", "run%3Aone")
	routeContext.URLParams.Add("section", "details")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeContext))
	data, err := h.pipelineRunDocumentData(r)
	if err != nil {
		t.Fatalf("pipelineRunDocumentData() error = %v; optional evidence failures should not block the run page", err)
	}
	if len(data.Page.Execution.Models) != 1 || data.Page.Execution.ModelsUnavailable == nil || !*data.Page.Execution.ModelsUnavailable {
		t.Fatalf("model fallback = %#v; want recorded scope with unavailable outcomes", data.Page.Execution)
	}
	if data.Page.Events == nil || len(data.Page.Events) != 0 || data.Page.EventsUnavailable == nil || !*data.Page.EventsUnavailable {
		t.Fatalf("event fallback = %#v, unavailable %v", data.Page.Events, data.Page.EventsUnavailable)
	}
	if data.Page.Execution.PublicationOutcome != "unverified" || data.Page.StatusLabel != "Running" {
		t.Fatalf("run/publication statuses = %q / %q", data.Page.StatusLabel, data.Page.Execution.PublicationOutcome)
	}
	if data.Page.Execution.AttemptsUnavailable == nil || !*data.Page.Execution.AttemptsUnavailable || data.Page.Execution.Models[0].AttemptsUnavailable == nil || !*data.Page.Execution.Models[0].AttemptsUnavailable {
		t.Fatalf("attempt fallback = %#v, want unavailable flags when attempt or child-run storage fails", data.Page.Execution)
	}
	if data.Page.ActiveTab != "details" {
		t.Fatalf("route section = %q; want the path section to take precedence", data.Page.ActiveTab)
	}
}

func TestPipelineRunPageMarksCappedChildRunReadIncomplete(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	run := refreshrun.RunRecord{
		ID: "run:one", Identity: identity, SemanticModelID: "semantic-model:sales", PipelineID: "pipeline:sales",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline:sales", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusSucceeded, PlanDigest: "plan-digest", MaterializationScope: []string{"model:sales"},
	}
	children := make([]refreshrun.RunRecord, pipelineRunChildRunPageSize)
	h := &BrowserHandler{
		RunDetailReader:  pipelineRunDetailReaderStub{run: run, childRuns: children},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return identity.ProjectID, nil },
		Environment:      identity.Environment,
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "alice", DevBypass: true}, true },
	}
	r := httptest.NewRequest(http.MethodGet, "/?asset=pipeline:sales&run=run:one", nil)
	data, err := h.pipelineRunDocumentData(r)
	if err != nil {
		t.Fatalf("pipelineRunDocumentData() error = %v", err)
	}
	if data.Page.Execution.ModelsUnavailable == nil || !*data.Page.Execution.ModelsUnavailable {
		t.Fatalf("capped child-run result was presented as complete: %#v", data.Page.Execution)
	}
}

func TestPipelineRunDocumentRouteAndUpdatesBootstrap(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	run := refreshrun.RunRecord{
		ID: "run:one", Identity: identity, SemanticModelID: "semantic-model:sales", PipelineID: "pipeline:sales",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline:sales", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusSucceeded, PlanDigest: "plan-digest", MaterializationScope: []string{"model:sales"},
	}
	h := &BrowserHandler{
		RunDetailReader:  pipelineRunDetailReaderStub{run: run},
		RunEventReader:   pipelineRunEventReaderStub{},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return identity.ProjectID, nil },
		Environment:      identity.Environment,
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
		Catalog:          pipelineRunCatalogStub{canonicalID: "pipeline:sales"},
		AuthorizePipeline: func(_ *http.Request, assetID string, capability access.Capability) (bool, error) {
			return assetID == "pipeline:sales" && capability == access.CapabilityResourceRead, nil
		},
	}
	router := chi.NewRouter()
	h.MountAuthenticated(router)
	document := httptest.NewRecorder()
	router.ServeHTTP(document, httptest.NewRequest(http.MethodGet, "/pipelines/pipeline%3Asales/runs/run%3Aone/details", nil))
	if document.Code != http.StatusOK || !strings.Contains(document.Body.String(), "lv-pipeline-run-page") {
		t.Fatalf("canonical encoded run document = %d: %s", document.Code, document.Body.String())
	}
	if !strings.Contains(document.Body.String(), "pipeline_run_detail") || !strings.Contains(document.Body.String(), "section=details") {
		t.Fatalf("document does not bootstrap the path-selected run section: %s", document.Body.String())
	}

	streamBody := pipelineDetailHTTPUpdates(t, h, url.Values{
		"route": {"pipeline_run_detail"}, "surface": {"pipeline_run_detail"}, "asset": {"pipeline:sales"},
		"run": {"run:one"}, "section": {"events"},
	})
	if !strings.Contains(streamBody, "run:one") || !strings.Contains(streamBody, "pipeline_run_detail") || !strings.Contains(streamBody, `"activeTab":"events"`) {
		t.Fatalf("typed run bootstrap = %s", streamBody)
	}
	streamID := regexp.MustCompile(`"streamInstanceId":"([^"]+)"`)
	first := streamID.FindStringSubmatch(streamBody)
	second := streamID.FindStringSubmatch(pipelineDetailHTTPUpdates(t, h, url.Values{
		"route": {"pipeline_run_detail"}, "surface": {"pipeline_run_detail"}, "asset": {"pipeline:sales"},
		"run": {"run:one"}, "section": {"events"},
	}))
	if len(first) != 2 || len(second) != 2 || first[1] == second[1] {
		t.Fatalf("run stream bootstrap did not issue a new stream identity on reconnect: first=%v second=%v", first, second)
	}
}

func TestPipelineRunRouteFailsClosedForUnauthorizedWrongPipelineAndChildRuns(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:one"}
	root := refreshrun.RunRecord{
		ID: "run:one", Identity: identity, SemanticModelID: "semantic-model:sales", PipelineID: "pipeline:sales",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline:sales", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusFailed, PlanDigest: "plan-digest", MaterializationScope: []string{"model:sales"},
	}
	newRouter := func(record refreshrun.RunRecord, canRead bool) *chi.Mux {
		h := &BrowserHandler{
			RunDetailReader:  pipelineRunDetailReaderStub{run: record},
			ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return identity.ProjectID, nil },
			Environment:      identity.Environment,
			CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
			Catalog:          pipelineRunCatalogStub{canonicalID: "pipeline:sales"},
			AuthorizePipeline: func(_ *http.Request, assetID string, capability access.Capability) (bool, error) {
				return canRead && assetID == "pipeline:sales" && capability == access.CapabilityResourceRead, nil
			},
		}
		router := chi.NewRouter()
		h.MountAuthenticated(router)
		return router
	}
	path := "/pipelines/pipeline%3Asales/runs/run%3Aone"

	unauthorized := httptest.NewRecorder()
	newRouter(root, false).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("unauthorized run route status = %d; want hidden 404", unauthorized.Code)
	}

	wrongPipeline := root
	wrongPipeline.PipelineID = "pipeline:other"
	wrongPipeline.TargetID = "pipeline:other"
	wrong := httptest.NewRecorder()
	newRouter(wrongPipeline, true).ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, path, nil))
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong-pipeline route status = %d; want 404", wrong.Code)
	}

	child := root
	child.ID = "run:child"
	child.ParentRunID = "run:parent"
	child.TargetType = refreshrun.TargetModel
	child.TargetID = "model:sales"
	childRoute := httptest.NewRecorder()
	newRouter(child, true).ServeHTTP(childRoute, httptest.NewRequest(http.MethodGet, path, nil))
	if childRoute.Code != http.StatusNotFound {
		t.Fatalf("child-run route status = %d; want 404", childRoute.Code)
	}
}

type pipelineRunDetailReaderStub struct {
	run          refreshrun.RunRecord
	childRuns    []refreshrun.RunRecord
	childRunsErr error
	attempts     map[string]refreshrun.RunAttemptPage
	attemptsErr  error
}

func (s pipelineRunDetailReaderStub) GetRun(context.Context, refreshrun.ReadScope, string) (refreshrun.RunRecord, error) {
	return s.run, nil
}

func (s pipelineRunDetailReaderStub) ListChildRuns(context.Context, refreshrun.ReadScope, string) ([]refreshrun.RunRecord, error) {
	return s.childRuns, s.childRunsErr
}

func (s pipelineRunDetailReaderStub) ListRunAttempts(_ context.Context, _ refreshrun.ReadScope, runID string) (refreshrun.RunAttemptPage, error) {
	if s.attemptsErr != nil {
		return refreshrun.RunAttemptPage{}, s.attemptsErr
	}
	if page, ok := s.attempts[runID]; ok {
		return page, nil
	}
	return refreshrun.RunAttemptPage{Attempts: []refreshrun.RunAttemptRecord{}}, nil
}

type pipelineRunEventReaderStub struct{ err error }

func (s pipelineRunEventReaderStub) ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error) {
	return nil, s.err
}

type pipelineRunPublicationReaderStub struct {
	evidence refreshpresentation.RunPublicationEvidence
	found    bool
	err      error
}

func (s pipelineRunPublicationReaderStub) RunPublication(context.Context, refreshrun.ReadScope, string) (refreshpresentation.RunPublicationEvidence, bool, error) {
	return s.evidence, s.found, s.err
}

func TestAuthorizedHistoricalGraphHidesGraphWhenAnyHistoricalAssetIsFiltered(t *testing.T) {
	graph := servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: "pipeline:sales", ProjectID: "project:test", ServingStateID: "generation:old", Type: "refresh_pipeline", Key: "sales", Title: "Sales"},
		{ID: "model:private", ProjectID: "project:test", ServingStateID: "generation:old", Type: "model", Key: "private", Title: "Private model"},
	}}
	h := &BrowserHandler{
		HistoricalGraph: pipelineRunHistoricalGraphStub{graph: graph},
		CurrentUser:     func(*http.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
		Catalog:         pipelineRunCatalogStub{denied: "model:private"},
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "dev", GenerationID: "generation:old"}
	root := refreshrun.RunRecord{Identity: identity, PipelineID: "pipeline:sales"}
	lineage, available, _, reason := h.historicalPipelineGraph(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), identity.ProjectID, refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, root, "pipeline:sales")
	if available || len(lineage.Nodes) != 0 || reason == "" || !strings.Contains(reason, "cannot access") {
		t.Fatalf("filtered historical graph = %#v, available %v, reason %q", lineage, available, reason)
	}
}

type pipelineRunHistoricalGraphStub struct {
	graph servingstate.AssetGraph
}

func (s pipelineRunHistoricalGraphStub) ServingStateGraph(context.Context, projectgraph.ResourceID, string, servingstate.ID) (servingstate.AssetGraph, bool, error) {
	return s.graph, true, nil
}

type pipelineRunCatalogStub struct {
	denied      string
	canonicalID string
}

func (s pipelineRunCatalogStub) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	return projectcatalog.Page{}, nil
}

func (s pipelineRunCatalogStub) Resolve(_ context.Context, _ string, ref projectcatalog.Ref, _ access.Capability, _ bool) (projectcatalog.Result, error) {
	if ref.ID.String() == s.denied || (s.canonicalID != "" && ref.ID.String() != s.canonicalID) {
		return projectcatalog.Result{}, errors.New("forbidden")
	}
	return projectcatalog.Result{Ref: ref}, nil
}
