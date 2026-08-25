package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/publication"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectview "github.com/flidai/leapview/internal/project"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
)

func TestMountAuthenticatedRegistersCanonicalSurfacesOnly(t *testing.T) {
	router := chi.NewRouter()
	h := &BrowserHandler{Authenticate: func(next stdhttp.Handler) stdhttp.Handler { return next }}
	h.MountAuthenticated(router)

	var got []string
	if err := chi.Walk(router, func(method, route string, _ stdhttp.Handler, _ ...func(stdhttp.Handler) stdhttp.Handler) error {
		got = append(got, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(got)
	want := []string{"GET /", "GET /catalog/search", "GET /connections", "GET /connections/search", "GET /connections/{asset}/{section}", "GET /dashboards", "GET /dashboards/search", "GET /dashboards/{asset}/definition", "GET /dashboards/{asset}/details", "GET /dashboards/{asset}/lineage", "GET /dashboards/{asset}/versions", "GET /explore", "POST /explore/command", "GET /models", "GET /models/search", "GET /models/{asset}/{section}", "POST /models/{asset}/data/command", "GET /pipelines", "GET /pipelines/{asset}/{section}", "POST /pipelines/command", "GET /semantic-models", "GET /semantic-models/search", "GET /semantic-models/{asset}/{section}", "POST /semantic-models/{asset}/data/command", "GET /sources", "GET /sources/search", "GET /sources/{asset}/{section}", "POST /connections/administration/configuration", "POST /connections/administration/lifecycle", "POST /dashboards/{asset}/appearance"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for _, legacy := range []string{"/data", "/data/{asset}/{section}", "/data/search", "/workspaces", "/workspaces/{workspace}", "/admin/workspaces"} {
		for _, route := range got {
			if route == "GET "+legacy || route == "POST "+legacy {
				t.Fatalf("legacy route %q was mounted", legacy)
			}
		}
	}
}

func TestRequestedAssetSectionSupportsFixedDashboardRoutes(t *testing.T) {
	for _, section := range []string{"details", "definition", "versions", "lineage"} {
		request := httptest.NewRequest(stdhttp.MethodGet, "/dashboards/dashboard:executive-sales/"+section, nil)
		if got := requestedAssetSection(request); got != section {
			t.Fatalf("section = %q, want %q", got, section)
		}
	}
}

func TestBoundProjectUsesActiveProjectResolver(t *testing.T) {
	want := projectgraph.ResourceID("project:active")
	h := &BrowserHandler{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return want, nil }}
	got, err := h.boundProject(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("project ID = %q, want %q", got, want)
	}
}

type browserGraphStub struct{ graph servingstate.AssetGraph }

func (s browserGraphStub) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	return s.graph, true, nil
}

type browserAssetVersionsStub struct {
	versions []servingstate.AssetVersion
}

type browserPhysicalCatalogStub map[string]ModelPhysicalMetadata

func (s browserPhysicalCatalogStub) ModelPhysicalMetadata(context.Context, projectgraph.ResourceID, string) (map[string]ModelPhysicalMetadata, error) {
	return s, nil
}

type browserUnavailablePhysicalCatalogStub struct{}

func (browserUnavailablePhysicalCatalogStub) ModelPhysicalMetadata(context.Context, projectgraph.ResourceID, string) (map[string]ModelPhysicalMetadata, error) {
	return nil, errors.New("catalog snapshot unavailable")
}

func (s browserAssetVersionsStub) AssetVersions(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID) ([]servingstate.AssetVersion, error) {
	return s.versions, nil
}

type browserRefreshStateStub struct {
	state                    refreshpresentation.AssetRefreshState
	err                      error
	requestedModelID         *projectgraph.ResourceID
	requestedSemanticModelID *projectgraph.ResourceID
}

func (s browserRefreshStateStub) AssetRefreshState(context.Context, projectgraph.ResourceID, string, projectgraph.ResourceID, projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error) {
	return s.state, s.err
}

func (s browserRefreshStateStub) ModelRefreshState(_ context.Context, _ projectgraph.ResourceID, _ string, modelID projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error) {
	if s.requestedModelID != nil {
		*s.requestedModelID = modelID
	}
	return s.state, s.err
}

func (s browserRefreshStateStub) SemanticModelRefreshState(_ context.Context, _ projectgraph.ResourceID, _ string, semanticModelID projectgraph.ResourceID) (refreshpresentation.AssetRefreshState, error) {
	if s.requestedSemanticModelID != nil {
		*s.requestedSemanticModelID = semanticModelID
	}
	return s.state, s.err
}

func TestAssetVersionsStateKeepsCurrentHashAndLoadsHistory(t *testing.T) {
	h := &BrowserHandler{
		Environment: "dev",
		AssetVersions: browserAssetVersionsStub{versions: []servingstate.AssetVersion{
			{ServingStateID: "state:current", Environment: "dev", Status: "active", ContentHash: "sha256:current", CreatedAt: "2026-08-20T12:00:00Z", SnapshotID: "snapshot:2", PayloadJSON: `{"kind":"Model"}`},
			{ServingStateID: "state:old", Status: "inactive", ContentHash: "sha256:old", CreatedAt: "2026-08-19T12:00:00Z"},
		}},
	}
	state, err := h.assetVersionsState(t.Context(), "project:test", projectview.DevelopAssetView{ID: "model:orders", ContentHash: "sha256:current"}, "versions")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentContentHash != "sha256:current" || len(state.Versions) != 2 || state.Versions[1].ContentHash != "sha256:old" {
		t.Fatalf("versions state = %#v", state)
	}
	if state.Versions[0].Environment != "dev" || state.Versions[0].SnapshotID != "snapshot:2" || state.Versions[0].PayloadJSON != `{"kind":"Model"}` {
		t.Fatalf("versions drawer state = %#v", state.Versions[0])
	}
}

func TestAssetVersionsStateDiscoversHistoryWhenCurrentHashIsMissing(t *testing.T) {
	h := &BrowserHandler{Environment: "dev", AssetVersions: browserAssetVersionsStub{versions: []servingstate.AssetVersion{{ServingStateID: "state:old", ContentHash: "sha256:old"}}}}
	state, err := h.assetVersionsState(t.Context(), "project:test", projectview.DevelopAssetView{ID: "dashboard:sales"}, "details")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Versions) != 1 || state.Versions[0].ContentHash != "sha256:old" {
		t.Fatalf("versions state = %#v, want history discovery for Versions tab", state)
	}
}

func TestAssetRefreshStateMapsPipelinePresentation(t *testing.T) {
	finished := "2026-08-20T11:00:00Z"
	next := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	h := &BrowserHandler{
		Environment: "dev",
		RefreshState: browserRefreshStateStub{state: refreshpresentation.AssetRefreshState{
			Runs:             []refreshpresentation.AssetRefreshRun{{ID: "run:latest", Status: "succeeded", FinishedAt: finished, ServingStateID: "state:refresh"}},
			LatestSuccessful: refreshpresentation.AssetRefreshRun{ID: "run:latest", Status: "succeeded", FinishedAt: finished},
			DataVersion:      refreshpresentation.AssetDataVersion{SnapshotID: 42, ServingStateID: "state:refresh", Source: "refresh"},
			NextRun:          next,
		}},
	}
	state, err := h.assetRefreshState(t.Context(), "project:test", projectview.DevelopAssetView{
		ID: "pipeline:daily", Type: string(projectview.AssetTypeRefreshPipeline), Payload: map[string]any{"SemanticModel": "semantic:sales"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.LatestSuccessful.ID != "run:latest" || state.DataVersion.SnapshotID != 42 || !state.NextRun.Equal(next) {
		t.Fatalf("refresh state = %#v", state)
	}
}

func TestAssetRefreshStateMapsModelRunHistory(t *testing.T) {
	requestedModelID := projectgraph.ResourceID("")
	h := &BrowserHandler{
		Environment: "dev",
		RefreshState: browserRefreshStateStub{state: refreshpresentation.AssetRefreshState{
			Runs:             []refreshpresentation.AssetRefreshRun{{ID: "run:model", Status: "succeeded", TriggerType: "dependency"}},
			LatestSuccessful: refreshpresentation.AssetRefreshRun{ID: "run:model", Status: "succeeded"},
		}, requestedModelID: &requestedModelID},
	}
	state, err := h.assetRefreshState(t.Context(), "project:test", projectview.DevelopAssetView{
		ID: "model:sales_customers", Key: "sales_customers", Type: string(projectview.AssetTypeModelTable),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[0].ID != "run:model" || state.LatestSuccessful.ID != "run:model" {
		t.Fatalf("model refresh state = %#v", state)
	}
	if requestedModelID != "sales_customers" {
		t.Fatalf("model refresh target = %q, want authored model key", requestedModelID)
	}
}

func TestAssetRefreshStateMapsSemanticModelRunHistory(t *testing.T) {
	requestedSemanticModelID := projectgraph.ResourceID("")
	h := &BrowserHandler{
		Environment: "dev",
		RefreshState: browserRefreshStateStub{state: refreshpresentation.AssetRefreshState{
			Runs:             []refreshpresentation.AssetRefreshRun{{ID: "run:semantic", Status: "succeeded", TriggerType: "schedule"}},
			LatestSuccessful: refreshpresentation.AssetRefreshRun{ID: "run:semantic", Status: "succeeded"},
		}, requestedSemanticModelID: &requestedSemanticModelID},
	}
	state, err := h.assetRefreshState(t.Context(), "project:test", projectview.DevelopAssetView{
		ID: "semantic-model:sales", Key: "sales", Type: string(projectview.AssetTypeSemanticModel),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Runs) != 1 || state.Runs[0].ID != "run:semantic" || state.LatestSuccessful.ID != "run:semantic" {
		t.Fatalf("semantic model refresh state = %#v", state)
	}
	if requestedSemanticModelID != "semantic-model:sales" {
		t.Fatalf("semantic model refresh target = %q, want canonical semantic-model ID", requestedSemanticModelID)
	}
}

func TestAssetRefreshStateMarksMissingReaderUnavailable(t *testing.T) {
	state, err := (&BrowserHandler{}).assetRefreshState(t.Context(), "project:test", projectview.DevelopAssetView{ID: "pipeline:daily", Type: string(projectview.AssetTypeRefreshPipeline)})
	if err != nil {
		t.Fatal(err)
	}
	if !state.Unavailable {
		t.Fatalf("refresh state = %#v, want unavailable without reader", state)
	}
}

func TestPipelineMutationProjectionRequiresResourceUse(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodGet, "/pipelines", nil)
	var gotID string
	h := &BrowserHandler{AuthorizePipeline: func(_ *stdhttp.Request, pipelineID string, capability access.Capability) (bool, error) {
		if capability != access.CapabilityResourceUse {
			t.Fatalf("capability = %q, want RESOURCE_USE", capability)
		}
		gotID = pipelineID
		return false, nil
	}}
	if h.pipelineMutationAllowed(request, "pipeline:sales") {
		t.Fatal("read-only pipeline unexpectedly exposed mutation controls")
	}
	if gotID != "pipeline:sales" {
		t.Fatalf("authorization ID = %q, want canonical asset ID", gotID)
	}
}

func TestPipelineMutationProjectionAllowsConfiguredDevelopmentBypass(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodGet, "/pipelines", nil)
	authorizerCalled := false
	h := &BrowserHandler{
		CurrentUser: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "dev", DevBypass: true}, true
		},
		AuthorizePipeline: func(*stdhttp.Request, string, access.Capability) (bool, error) {
			authorizerCalled = true
			return false, nil
		},
	}
	if !h.pipelineMutationAllowed(request, "pipeline:sales") {
		t.Fatal("configured development bypass did not expose pipeline mutation controls")
	}
	if authorizerCalled {
		t.Fatal("development bypass unexpectedly consulted the serving-state resource authorizer")
	}
}

func TestDashboardCreationProjectionAllowsConfiguredDevelopmentBypass(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodGet, "/", nil)
	authorizerCalled := false
	h := &BrowserHandler{
		CurrentUser: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "dev", DevBypass: true}, true
		},
		AuthorizeCreateDashboard: func(*stdhttp.Request, projectgraph.ResourceID, access.Capability) (bool, error) {
			authorizerCalled = true
			return false, nil
		},
	}
	if !h.dashboardCreationAllowed(request) {
		t.Fatal("configured development bypass did not expose dashboard creation")
	}
	if authorizerCalled {
		t.Fatal("development bypass unexpectedly consulted the serving-state project authorizer")
	}
}

type browserProjectDefinitionStub struct {
	definition projectmanifest.Project
	compiled   map[string]*semanticquery.CompiledModel
	err        error
}

type browserSourceSchemaStub struct {
	observation SourceSchemaObservation
	found       bool
	err         error
}

func (s browserSourceSchemaStub) SourceSchemaObservation(context.Context, projectgraph.ResourceID, string, string, projectgraph.ResourceID) (SourceSchemaObservation, bool, error) {
	return s.observation, s.found, s.err
}

type browserDataQueryStub struct {
	query  dataquery.Query
	result dataquery.Result
	err    error
}

func (s *browserDataQueryStub) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	s.query = query
	return s.result, s.err
}

func (s browserProjectDefinitionStub) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return s.definition, s.compiled, s.err
}

func TestModelAssetBootstrapUsesActiveCompiledDefinition(t *testing.T) {
	const projectID = "project:test"
	const assetID = "model:zip_geolocations"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: projectID, ServingStateID: "state", Type: "model_table", Key: "zip_geolocations", Title: "ZIP locations", PayloadJSON: `{"kind":"model"}`,
		}}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: projectID,
			Models: map[string]semanticmodel.Table{assetID: {
				SourceDependencies: []string{"geolocations"}, Execution: semanticmodel.ExecutionDefinition{SQL: "select zip_code from source.geolocations"},
				Entities: map[string]semanticmodel.EntityDefinition{"zip": {Type: "primary", Fields: []string{"zip_code"}}}, GrainEntity: "zip", Dimensions: map[string]semanticmodel.MetricDimension{"zip_code": {Label: "ZIP code"}},
			}},
		}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:      "dev",
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=details", nil))
	if !ok {
		t.Fatal("asset bootstrap returned not ok")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fields (1)", `"label":"Mode","value":"SQL transform"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("bootstrap = %s, missing %q", encoded, want)
		}
	}
	if strings.Contains(string(encoded), `"label":"Input sources"`) {
		t.Fatalf("bootstrap = %s, duplicate input source count remains in overview", encoded)
	}
	definitionPatch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=definition", nil))
	if !ok {
		t.Fatal("asset definition bootstrap returned not ok")
	}
	definitionJSON, err := json.Marshal(definitionPatch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(definitionJSON), "select zip_code from source.geolocations") {
		t.Fatalf("definition bootstrap = %s, missing model SQL", definitionJSON)
	}
}

func TestPipelineDefinitionBootstrapDoesNotDependOnRefreshState(t *testing.T) {
	const projectID = "project:test"
	const assetID = "pipeline:daily"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: projectID, ServingStateID: "state", Type: "refresh_pipeline", Key: "daily", Title: "Daily refresh", PayloadJSON: `{}`,
		}}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: projectID,
			RefreshPipelines: map[string]refreshschedule.Definition{
				assetID: {ID: assetID, Name: "daily", SemanticModelID: "semantic:sales"},
			},
			AuthoredResourceSources: map[string]string{assetID: "apiVersion: leapview.dev/v1\nkind: Pipeline\n"},
		}},
		RefreshState:     browserRefreshStateStub{err: errors.New("refresh runtime unavailable")},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:      "dev",
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=definition", nil))
	if !ok {
		t.Fatal("pipeline definition bootstrap returned not ok")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `kind: Pipeline`) {
		t.Fatalf("definition bootstrap = %s, missing pipeline configuration", encoded)
	}

	details, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=details", nil))
	if !ok {
		t.Fatal("pipeline details bootstrap returned not ok")
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detailsJSON), `"status":"unavailable"`) || !strings.Contains(string(detailsJSON), `"disabled":true`) {
		t.Fatalf("details bootstrap = %s, want unavailable refresh state and disabled action", detailsJSON)
	}
	for _, want := range []string{"Refresh guidance", "refresh runtime", "Run now unavailable"} {
		if !strings.Contains(string(detailsJSON), want) {
			t.Fatalf("details bootstrap = %s, missing actionable unavailable-state text %q", detailsJSON, want)
		}
	}
	if strings.Contains(string(detailsJSON), `"href":"/connections"`) {
		t.Fatalf("details bootstrap = %s, must not infer a connection failure", detailsJSON)
	}

	for _, section := range []string{"bogus", "data"} {
		recorder := httptest.NewRecorder()
		if _, ok := h.assetBootstrap(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section="+section, nil)); ok {
			t.Fatalf("pipeline %s bootstrap returned ok", section)
		}
		if recorder.Code != stdhttp.StatusNotFound {
			t.Fatalf("pipeline %s status = %d, want %d", section, recorder.Code, stdhttp.StatusNotFound)
		}
	}
	router := chi.NewRouter()
	router.Get("/pipelines/{asset}/{section}", h.PipelineAsset)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "/pipelines/"+assetID+"/bogus", nil))
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("invalid pipeline document status = %d, want %d", recorder.Code, stdhttp.StatusNotFound)
	}
}

func TestInvalidAssetSectionsReturnNotFoundBeforeDefinitionEnrichment(t *testing.T) {
	const assetID = "model:orders"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "orders", PayloadJSON: `{}`,
		}}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{err: errors.New("definition unavailable")},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:             func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	recorder := httptest.NewRecorder()
	if _, ok := h.assetBootstrap(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=bogus", nil)); ok {
		t.Fatal("invalid asset bootstrap returned ok")
	}
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("invalid bootstrap status = %d, want 404", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	if _, ok := h.assetBootstrap(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?route=connection_asset&surface=asset&asset="+assetID+"&section=details", nil)); ok {
		t.Fatal("model bootstrap accepted connection route kind")
	}
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("mismatched model route status = %d, want 404", recorder.Code)
	}
	router := chi.NewRouter()
	router.Get("/models/{asset}/{section}", h.ModelAsset)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "/models/"+assetID+"/bogus", nil))
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("invalid document status = %d, want 404", recorder.Code)
	}
}

func TestAssetDocumentRejectsAssetFromDifferentResourceArea(t *testing.T) {
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: "source:orders", ProjectID: "project:test", ServingStateID: "state", Type: "source", Key: "orders", PayloadJSON: `{}`,
		}}}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	router := chi.NewRouter()
	router.Get("/pipelines/{asset}/{section}", h.PipelineAsset)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(stdhttp.MethodGet, "/pipelines/source:orders/details", nil))
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("cross-area asset status = %d, want 404", recorder.Code)
	}
}

func TestUpdatesStopsAfterFailedAreaBootstrap(t *testing.T) {
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	for _, route := range []string{"data", "connections", "pipelines"} {
		recorder := httptest.NewRecorder()
		h.Updates(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?route="+route, nil))
		if recorder.Code != stdhttp.StatusServiceUnavailable {
			t.Fatalf("%s update status = %d, want 503", route, recorder.Code)
		}
		if body := recorder.Body.String(); body != "Service Unavailable\n" {
			t.Fatalf("%s update body = %q, want only bootstrap error", route, body)
		}
	}
}

func TestCatalogUpdatesRemainOpenAfterBootstrap(t *testing.T) {
	h := &BrowserHandler{
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(stdhttp.MethodGet, "/updates?route=catalog", nil).WithContext(ctx)
	recorder := &notifyingResponseRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		wrote:            make(chan struct{}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(recorder, request)
	}()

	select {
	case <-recorder.wrote:
	case <-time.After(time.Second):
		t.Fatal("catalog updates did not write its bootstrap patch")
	}
	select {
	case <-done:
		t.Fatal("catalog updates closed after its bootstrap patch")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("catalog updates did not stop after cancellation")
	}
}

type notifyingResponseRecorder struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	once  sync.Once
}

func (r *notifyingResponseRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(p)
	r.once.Do(func() { close(r.wrote) })
	return n, err
}

func TestModelAssetBootstrapUsesAuthoredSQLWhenRuntimeProjectionIsTargetBound(t *testing.T) {
	const projectID = "project:test"
	assets := []servingstate.Asset{
		{ID: "model:zip_geolocations", ProjectID: projectID, ServingStateID: "state", Type: "model_table", Key: "zip_geolocations", Title: "ZIP locations", PayloadJSON: `{}`},
		{ID: "model:sales_orders", ProjectID: projectID, ServingStateID: "state", Type: "model_table", Key: "sales_orders", Title: "Sales orders", PayloadJSON: `{}`},
	}
	definition := projectmanifest.Project{
		ID: projectID,
		// Target-bound runtime execution is intentionally not used as authored
		// source. Both tables look like direct sources in this projection.
		Models: map[string]semanticmodel.Table{
			"model:zip_geolocations": {Execution: semanticmodel.ExecutionDefinition{Source: "source:olist_geolocation"}},
			"model:sales_orders":     {Execution: semanticmodel.ExecutionDefinition{Source: "source:olist_orders"}},
		},
		AuthoredModelDefinitions: map[string]projectmanifest.AuthoredModelDefinition{
			"model:zip_geolocations": {Type: "sql", SQL: `SELECT zip_prefix FROM source."olist.geolocation"`},
			"model:sales_orders":     {Type: "sql", SQL: `SELECT order_id, revenue FROM source."olist.orders"`},
		},
		AuthoredModelSources: map[string]string{
			"model:zip_geolocations": "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:zip_geolocations, name: zip_geolocations}\n",
			"model:sales_orders":     "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:sales_orders, name: sales_orders}\n",
		},
	}
	h := &BrowserHandler{
		Graph:                   browserGraphStub{graph: servingstate.AssetGraph{Assets: assets}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: definition},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:             "dev",
		CurrentUser:             func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	for _, assetID := range []string{"model:zip_geolocations", "model:sales_orders"} {
		patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=definition", nil))
		if !ok {
			t.Fatalf("asset %s bootstrap returned not ok", assetID)
		}
		encoded, err := json.Marshal(patch)
		if err != nil {
			t.Fatal(err)
		}
		want := definition.AuthoredModelDefinitions[assetID].SQL
		if !strings.Contains(string(encoded), strings.ReplaceAll(want, `"`, `\"`)) {
			t.Fatalf("asset %s bootstrap = %s, missing authored SQL %q", assetID, encoded, want)
		}
		if !strings.Contains(string(encoded), `kind: Model`) {
			t.Fatalf("asset %s bootstrap = %s, missing authored model configuration", assetID, encoded)
		}
	}
}

func TestModelAssetReadModelIncludesServingCatalogStatistics(t *testing.T) {
	const assetID = "model:zip_geolocations"
	snapshotAt := time.Date(2026, 8, 24, 14, 32, 0, 0, time.UTC)
	h := &BrowserHandler{
		Environment: "dev",
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: "project:test", Models: map[string]semanticmodel.Table{assetID: {ModelName: "zip_geolocations"}},
		}},
		PhysicalCatalog: browserPhysicalCatalogStub{"zip_geolocations": {
			RowCount: 99_441, ColumnCount: 5, FileCount: 2, SizeBytes: 1_572_864, SnapshotID: 17,
			SnapshotAt: snapshotAt,
			Schema:     semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "zip_prefix", PhysicalType: "VARCHAR"}}},
		}},
	}
	asset, err := h.projectAssetReadModel(t.Context(), projectview.DevelopAssetView{
		ID: assetID, ProjectID: "project:test", Type: string(projectview.AssetTypeModelTable), Key: "zip_geolocations", Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("projectAssetReadModel() error = %v", err)
	}
	physical, ok := asset.Payload["Physical"].(map[string]any)
	if !ok || physical["RowCount"] != int64(99_441) || physical["SizeBytes"] != int64(1_572_864) || physical["SnapshotID"] != int64(17) || physical["SnapshotAt"] != snapshotAt.Format(time.RFC3339) {
		t.Fatalf("physical payload = %#v", asset.Payload["Physical"])
	}
	schema, ok := asset.Payload["Schema"].(map[string]any)
	columns, columnsOK := schema["columns"].([]any)
	if !ok || !columnsOK || len(columns) != 1 {
		t.Fatalf("schema payload = %#v", asset.Payload["Schema"])
	}
	column, columnOK := columns[0].(map[string]any)
	if !columnOK || column["physicalType"] != "VARCHAR" {
		t.Fatalf("schema payload = %#v", asset.Payload["Schema"])
	}
}

func TestModelAssetReadModelRemainsAvailableWhenServingCatalogStatisticsAreUnavailable(t *testing.T) {
	const assetID = "model:zip_geolocations"
	h := &BrowserHandler{
		Environment: "dev",
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: "project:test", Models: map[string]semanticmodel.Table{assetID: {ModelName: "zip_geolocations"}},
		}},
		PhysicalCatalog: browserUnavailablePhysicalCatalogStub{},
	}
	asset, err := h.projectAssetReadModel(t.Context(), projectview.DevelopAssetView{
		ID: assetID, ProjectID: "project:test", Type: string(projectview.AssetTypeModelTable), Key: "zip_geolocations", Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("projectAssetReadModel() error = %v", err)
	}
	if asset.ID != assetID {
		t.Fatalf("asset = %#v, want authored model detail to remain available", asset)
	}
	if _, ok := asset.Payload["Physical"]; ok {
		t.Fatalf("physical payload = %#v, want unavailable statistics omitted", asset.Payload["Physical"])
	}
}

func TestSourceAssetReadModelUsesActiveGenerationObservedSchema(t *testing.T) {
	const assetID = "source:orders"
	observedAt := time.Date(2026, 8, 24, 7, 30, 0, 0, time.UTC)
	h := &BrowserHandler{
		Environment: "dev",
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: "project:test", Sources: map[string]semanticmodel.Source{assetID: {
				SchemaMode: "compatible",
				Fields: map[string]semanticmodel.SourceField{
					"order_id": {Datatype: semanticmodel.DataTypeString, Description: "Order identifier"},
				},
			}},
		}},
		SourceSchemas: browserSourceSchemaStub{observation: SourceSchemaObservation{
			Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
				{Name: "review_id", Ordinal: 0, PhysicalType: "VARCHAR"},
				{Name: "order_id", Ordinal: 1, PhysicalType: "VARCHAR"},
			}},
			Mode: "compatible", Status: "success", ObservedAt: observedAt, SchemaDigest: "sha256:observed",
		}, found: true},
	}
	asset, err := h.projectAssetReadModel(t.Context(), projectview.DevelopAssetView{
		ID: assetID, ProjectID: "project:test", ServingStateID: "state:active", Type: string(projectview.AssetTypeSource), Key: "orders", Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("projectAssetReadModel() error = %v", err)
	}
	schema, ok := asset.Payload["Schema"].(map[string]any)
	if !ok || len(schema["columns"].([]any)) != 2 {
		t.Fatalf("source schema = %#v, want two observed columns", asset.Payload["Schema"])
	}
	observation, ok := asset.Payload["SchemaObservation"].(map[string]any)
	if !ok || observation["Status"] != "success" || observation["ObservedAt"] != observedAt.Format(time.RFC3339) {
		t.Fatalf("source observation = %#v, want active generation evidence", asset.Payload["SchemaObservation"])
	}
	fields := asset.Payload["Fields"].(map[string]any)
	if len(fields) != 1 || fields["order_id"] == nil {
		t.Fatalf("source contract fields = %#v, want authored contract retained", fields)
	}
}

func TestSourceAssetReadModelFallsBackWhenObservedSchemaIsUnavailable(t *testing.T) {
	const assetID = "source:orders"
	h := &BrowserHandler{
		Environment: "dev",
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: "project:test", Sources: map[string]semanticmodel.Source{assetID: {
				SchemaMode: "compatible", Fields: map[string]semanticmodel.SourceField{"order_id": {Datatype: semanticmodel.DataTypeString}},
			}},
		}},
		SourceSchemas: browserSourceSchemaStub{err: errors.New("provenance unavailable")},
	}
	asset, err := h.projectAssetReadModel(t.Context(), projectview.DevelopAssetView{
		ID: assetID, ProjectID: "project:test", ServingStateID: "state:legacy", Type: string(projectview.AssetTypeSource), Key: "orders", Payload: map[string]any{},
	})
	if err != nil {
		t.Fatalf("projectAssetReadModel() error = %v, want authored fallback", err)
	}
	if _, ok := asset.Payload["SchemaObservation"]; ok {
		t.Fatalf("schema observation = %#v, want unavailable evidence omitted", asset.Payload["SchemaObservation"])
	}
	fields := asset.Payload["Fields"].(map[string]any)
	if fields["order_id"] == nil || asset.Payload["SchemaMode"] != "compatible" {
		t.Fatalf("source fallback payload = %#v, want authored contract", asset.Payload)
	}
}

func TestDashboardAssetReadModelProjectsCompiledDefinitionAndPublications(t *testing.T) {
	const configuration = "apiVersion: leapview.dev/v1\nkind: Dashboard\n"
	asset := projectview.DevelopAssetView{ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales", Title: "Sales", Payload: map[string]any{"kind": "dashboard"}}
	definition := projectmanifest.Project{
		DashboardDefinitions: map[string]dashboarddefinition.Definition{
			"dashboard:sales": {ID: "dashboard:sales", Title: "Sales", SemanticModel: "semantic:sales", Pages: []dashboard.Page{{ID: "overview", Title: "Overview"}}, Visualizations: map[string]visualizationdefinition.Definition{}},
		},
		Publications: map[string]publication.Definition{
			"publication:website": {Name: "publication:website", Dashboard: "dashboard:sales", DefaultPage: "overview", AllowedOrigins: []string{"https://example.test"}, ConfigurationDigest: "sha256:abc"},
			"publication:other":   {Name: "publication:other", Dashboard: "dashboard:other"},
		},
		AuthoredResourceSources: map[string]string{"dashboard:sales": configuration},
	}
	enriched, err := projectAssetReadModelFromDefinition(asset, definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	publications, ok := enriched.Payload["Publications"].([]map[string]any)
	if !ok || len(publications) != 1 {
		t.Fatalf("publications payload = %#v, want one matching publication", enriched.Payload["Publications"])
	}
	if publications[0]["Name"] != "publication:website" || publications[0]["DefaultPage"] != "overview" {
		t.Fatalf("publication payload = %#v, want authored definition", publications[0])
	}
	if _, ok := enriched.Payload["pages"]; !ok {
		t.Fatalf("dashboard payload = %#v, want compiled pages", enriched.Payload)
	}
	if enriched.Payload["Configuration"] != configuration {
		t.Fatalf("dashboard configuration = %#v, want exact authored YAML", enriched.Payload["Configuration"])
	}
}

func TestConnectionAssetReadModelDefinitionRedactsSensitiveConfiguration(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Title: "Warehouse", Payload: map[string]any{}}
	definition := projectmanifest.Project{Connections: map[string]semanticmodel.Connection{
		asset.ID: {
			Kind: "postgres", Host: "private.database.internal", Port: 5432, Database: "finance", Username: "analyst", Path: "/target/files", Root: "/managed/root", Scope: "target-only",
			Credentials: semanticmodel.ConnectionCredentials{Provider: "infisical", Secret: "prod/warehouse/password"},
		},
	}}
	enriched, err := projectAssetReadModelFromDefinition(asset, definition, nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration, _ := enriched.Payload["Configuration"].(string)
	if !strings.Contains(configuration, "kind: Connection") || !strings.Contains(configuration, "type: postgres") {
		t.Fatalf("connection configuration = %q, want safe authored identity", configuration)
	}
	for _, secret := range []string{"private.database.internal", "finance", "analyst", "infisical", "prod/warehouse/password", "/target/files", "/managed/root", "target-only"} {
		if strings.Contains(configuration, secret) {
			t.Fatalf("connection configuration exposed %q: %s", secret, configuration)
		}
	}
}

func TestConnectionAssetBootstrapUsesConnectionPageSignalOnCanonicalStream(t *testing.T) {
	const assetID = "connection:warehouse"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: "project:test", ServingStateID: "state", Type: "connection", Key: "warehouse", Title: "Warehouse", PayloadJSON: `{}`,
		}}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{Connections: map[string]semanticmodel.Connection{
			assetID: {Kind: "duckdb"},
		}}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		Environment:      "dev",
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?route=connection_asset&surface=asset&asset="+assetID+"&section=details", nil))
	if !ok {
		t.Fatal("connection asset bootstrap returned not ok")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"connection_asset"`, `"connectionLifecycle"`, `"label":"Connections"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("connection bootstrap = %s, missing %s", encoded, want)
		}
	}
	recorder := httptest.NewRecorder()
	if _, ok := h.assetBootstrap(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?route=data&surface=asset&asset="+assetID+"&section=details", nil)); ok {
		t.Fatal("connection bootstrap accepted data route kind")
	}
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("mismatched connection route status = %d, want 404", recorder.Code)
	}
}

func TestDataExplorerSignalsUseAuthorizedActiveDefinition(t *testing.T) {
	const projectID = "project:test"
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {ModelName: "orders", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: "source:orders", ProjectID: projectID, ServingStateID: "state", Type: "source", Key: "orders", Title: "Orders source", PayloadJSON: `{}`},
			{ID: "model:orders", ProjectID: projectID, ServingStateID: "state", Type: "model_table", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: "semantic:sales", ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID:             projectID,
			Sources:        map[string]semanticmodel.Source{"source:orders": {Fields: map[string]semanticmodel.SourceField{"id": {Type: "integer"}}}},
			Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
			SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
			NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
		}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:      "dev",
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	recorder := httptest.NewRecorder()
	page, explorer, ok := h.dataExplorerSignals(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore?object=model:orders", nil))
	if !ok {
		t.Fatalf("data explorer returned not ok: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if page.Context.ObjectCount != 2 || len(explorer.Objects) != 2 {
		t.Fatalf("objects = %#v, context = %#v", explorer.Objects, page.Context)
	}
	if explorer.SelectedObject == nil || explorer.SelectedObject.ResourceID != "model:orders" {
		t.Fatalf("selected object = %#v, want model:orders", explorer.SelectedObject)
	}
	if len(explorer.Explore.Models) != 1 || len(explorer.Explore.Datasets) != 1 || len(explorer.Explore.Fields) != 1 {
		t.Fatalf("explore signal = %#v", explorer.Explore)
	}
	_, semanticExplorer, ok := h.dataExplorerSignals(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore?mode=explore&model=semantic:sales&dataset=orders", nil))
	if !ok || projectsignals.ValueOrZero(semanticExplorer.Command.Mode) != "explore" || semanticExplorer.SelectedObject == nil || semanticExplorer.SelectedObject.ResourceID != "model:orders" {
		t.Fatalf("semantic deep link = %#v", semanticExplorer)
	}
}

func TestDataExplorerPreviewExecutesGovernedModelTableQuery(t *testing.T) {
	executor := &browserDataQueryStub{result: dataquery.Result{
		Rows:           []dataquery.Row{{"order_id": int64(42), "status": "paid"}},
		TotalRows:      1,
		TotalRowsKnown: true,
		SQL:            `select "order_id", "status" from "orders"`,
	}}
	columns := []projectsignals.DataPreviewColumnSignal{{Key: "order_id", Label: "Order ID"}, {Key: "status", Label: "Status"}}
	object := projectsignals.DataExplorerObjectSignal{
		Key: "model_table:model:orders:semantic-model:sales", ResourceID: "model:orders", Layer: "model_table",
		ModelID: projectsignals.Pointer("semantic-model:sales"), Table: projectsignals.Pointer("orders"), Columns: &columns,
	}
	preview := dataExplorerPreview(t.Context(), executor, "project:test", object, projectsignals.DataExplorerCommand{
		ObjectKey: projectsignals.Pointer(object.Key), Count: 100, Limit: 100, Block: projectsignals.Pointer("all"),
	})

	if preview.Error != nil {
		t.Fatalf("preview error = %q", *preview.Error)
	}
	if preview.AvailableRows != 1 || len(preview.Blocks["a"].Rows) != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	if executor.query.ProjectID != "project:test" || executor.query.ModelID != "semantic-model:sales" || executor.query.Target != "orders" {
		t.Fatalf("query = %#v", executor.query)
	}
	if executor.query.Surface != dataquery.SurfaceDataExplorer || executor.query.Operation != dataquery.OperationPreviewWindow {
		t.Fatalf("query metadata = %#v", executor.query)
	}
}

func TestDataExplorerSemanticExploreExecutesGovernedAggregate(t *testing.T) {
	executor := &browserDataQueryStub{result: dataquery.Result{
		Columns: []dataquery.Column{{Name: "status"}, {Name: "orders"}},
		Rows:    []dataquery.Row{{"status": "paid", "orders": int64(7)}}, SQL: "select status, count(*)", DurationMS: 12,
	}}
	command, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{
		ModelID: projectsignals.Pointer("semantic-model:sales"), DatasetID: projectsignals.Pointer("orders"),
		Dimensions: []string{"orders.status"}, Metrics: []string{"orders"}, Filters: []projectsignals.DataExploreFilterSignal{},
		Sort: []projectsignals.DataExploreSortSignal{{Field: "orders", Direction: "desc"}}, Limit: 100,
	}, []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Label: "Status", Kind: "dimension", Compatible: true},
		{ID: "orders", Label: "Orders", Kind: "metric", Compatible: true},
	})

	if result.Error != nil || result.RowsReturned != 1 || len(result.Rows) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(command.Dimensions) != 1 || len(command.Metrics) != 1 {
		t.Fatalf("normalized command = %#v", command)
	}
	if executor.query.Kind != dataquery.KindSemanticAggregate || executor.query.ProjectID != "project:test" || executor.query.Operation != dataquery.OperationSemanticExplore {
		t.Fatalf("query = %#v", executor.query)
	}
	if executor.query.Fields[0].Alias != "status" || executor.query.Metrics[0].Alias != "orders" {
		t.Fatalf("query aliases = %#v / %#v", executor.query.Fields, executor.query.Metrics)
	}
}

func TestDataExplorerSemanticExploreUnscopesMultiRootMetric(t *testing.T) {
	executor := &browserDataQueryStub{result: dataquery.Result{
		Columns: []dataquery.Column{{Name: "order_share"}},
		Rows:    []dataquery.Row{{"order_share": 0.5}}, SQL: "select order_share",
	}}
	command, result := dataExplorerSemanticResult(t.Context(), executor, "project:test", projectsignals.DataExploreCommand{
		ModelID: projectsignals.Pointer("semantic-model:sales"), DatasetID: projectsignals.Pointer("customers"),
		Metrics: []string{"order_share"}, Limit: 100,
	}, []projectsignals.DataExploreFieldSignal{
		// An empty modelTable is the projection contract for a derived/ratio
		// metric whose dependencies span more than one physical dataset.
		{ID: "order_share", Label: "Order share", Kind: "metric", Compatible: true},
	})

	if result.Error != nil {
		t.Fatalf("result error = %q", *result.Error)
	}
	if len(command.Metrics) != 1 || executor.query.Kind != dataquery.KindSemanticAggregate {
		t.Fatalf("normalized command/query = %#v / %#v", command, executor.query)
	}
	if executor.query.Target != "" {
		t.Fatalf("query target = %q, want unscoped multi-root execution", executor.query.Target)
	}
	if executor.query.Metrics[0].Field != "order_share" {
		t.Fatalf("query metrics = %#v", executor.query.Metrics)
	}
}

func TestAssetDataExplorerScopesModelsAndSemanticModels(t *testing.T) {
	const projectID = "project:test"
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	executor := &browserDataQueryStub{result: dataquery.Result{Rows: []dataquery.Row{{"status": "paid"}}, TotalRows: 1, TotalRowsKnown: true}}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: "model:orders", ProjectID: projectID, Type: "model_table", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: "semantic-model:sales", ProjectID: projectID, Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: projectID, Models: map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
			SemanticModels: map[string]*semanticmodel.Model{"semantic-model:sales": model}, NameIndex: projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
		}, compiled: map[string]*semanticquery.CompiledModel{"semantic-model:sales": compiled}},
		QueryExecutor: executor, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment: "dev", CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	for _, test := range []struct {
		asset string
		mode  string
	}{
		{asset: "model:orders", mode: "browse"},
		{asset: "semantic-model:sales", mode: "explore"},
	} {
		recorder := httptest.NewRecorder()
		_, explorer, _, ok := h.dataExplorerSignalsForAssetCommand(recorder, httptest.NewRequest(stdhttp.MethodGet, "/", nil), test.asset, projectsignals.DataExplorerCommand{})
		if !ok {
			t.Fatalf("asset %s state failed: status=%d body=%s", test.asset, recorder.Code, recorder.Body.String())
		}
		if projectsignals.ValueOrZero(explorer.Command.Mode) != test.mode || len(explorer.Objects) != 1 || explorer.SelectedObject == nil {
			t.Fatalf("asset %s explorer = %#v", test.asset, explorer)
		}
		if test.mode == "browse" && len(explorer.Preview.Blocks["a"].Rows) != 1 {
			t.Fatalf("model preview = %#v", explorer.Preview)
		}
		if test.mode == "explore" && projectsignals.ValueOrZero(explorer.Explore.Command.ModelID) != test.asset {
			t.Fatalf("semantic command = %#v", explorer.Explore.Command)
		}
	}
}

func TestSemanticModelAssetBootstrapUsesCompiledModelProjection(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"customers": {ModelName: "customers", Entities: map[string]semanticmodel.EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{
			"orders_customer": {
				From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Fields: []string{"customer_id"}},
				To:   semanticmodel.RelationshipEndpointSpec{Dataset: "customers", Fields: []string{"customer_id"}},
			},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customer", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	const projectID = "project:test"
	const assetID = "semantic:sales"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: assetID, ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{"kind":"semantic_model"}`}},
		}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{ID: projectID, SemanticModels: map[string]*semanticmodel.Model{assetID: model}}, compiled: map[string]*semanticquery.CompiledModel{assetID: compiled}},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:             "dev",
		CurrentUser:             func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=details", nil))
	if !ok {
		t.Fatal("asset bootstrap returned not ok")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Datasets (2)", "Metrics (1)", "Relationships (1)"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("bootstrap = %s, missing %q", encoded, want)
		}
	}
	if !strings.Contains(string(encoded), `"nodes"`) || !strings.Contains(string(encoded), `"edges"`) {
		t.Fatalf("bootstrap = %s, semantic graph missing nodes or edges", encoded)
	}
}

func TestSemanticModelAssetBootstrapRejectsMissingCompiledModel(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {ModelName: "orders"},
	}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: "semantic:sales", ProjectID: "project:test", ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{"kind":"semantic_model"}`}},
		}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{ID: "project:test", SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		Environment:             "dev",
		CurrentUser:             func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	recorder := httptest.NewRecorder()
	if _, ok := h.assetBootstrap(recorder, httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset=semantic:sales&section=details", nil)); ok {
		t.Fatal("asset bootstrap returned ok without compiled semantic model")
	}
	if recorder.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, stdhttp.StatusServiceUnavailable)
	}
}

type browserCatalogStub struct{}

func (browserCatalogStub) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:allowed", Kind: projectgraph.KindModel}}}}, nil
}

func (browserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type pagedBrowserCatalogStub struct{}

func (pagedBrowserCatalogStub) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	if request.Cursor == "" {
		return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:allowed", Kind: projectgraph.KindModel}}}, NextCursor: "page-2"}, nil
	}
	return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:second", Kind: projectgraph.KindModel}}}}, nil
}

func (pagedBrowserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type kindAwareBrowserCatalogStub struct{ available projectgraph.Kind }

func (s kindAwareBrowserCatalogStub) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	for _, kind := range request.Kinds {
		if kind == s.available {
			return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "visible", Kind: kind}}}}, nil
		}
	}
	return projectcatalog.Page{}, nil
}

func (kindAwareBrowserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type sourceOnlyBrowserCatalogStub struct{}

func (sourceOnlyBrowserCatalogStub) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	for _, kind := range request.Kinds {
		if kind == projectgraph.KindSource {
			return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "source:orders", Kind: kind}}}}, nil
		}
	}
	return projectcatalog.Page{}, nil
}

func (sourceOnlyBrowserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

func TestAssetsFilterUnauthorizedSiblingAndEdges(t *testing.T) {
	allowed := projectgraph.ResourceID("model:allowed")
	denied := projectgraph.ResourceID("model:denied")
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, Environment: "dev", Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: allowed, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "allowed", Title: "Allowed", PayloadJSON: "{}"}, {ID: denied, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "denied", Title: "Denied", PayloadJSON: "{}"}},
			Edges:  []servingstate.AssetEdge{{ID: "edge", ProjectID: "project:test", ServingStateID: "state", FromAssetID: allowed, ToAssetID: denied, Type: "depends_on"}},
		}},
		Catalog: browserCatalogStub{}, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	_, assets, edges, ok := h.assets(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/sources", nil))
	if !ok {
		t.Fatal("assets returned not ok")
	}
	if len(assets) != 1 || assets[0].ID != allowed.String() {
		t.Fatalf("assets = %#v, want only %q", assets, allowed)
	}
	if len(edges) != 0 {
		t.Fatalf("edges = %#v, want denied endpoint edge removed", edges)
	}
}

func TestSourceSurfacesRetainOnlyAuthorizedConnectionContext(t *testing.T) {
	source := servingstate.Asset{ID: "source:orders", ProjectID: "project:test", ServingStateID: "state", Type: "source", Key: "orders", Title: "Orders", PayloadJSON: `{}`}
	otherSource := servingstate.Asset{ID: "source:customers", ProjectID: "project:test", ServingStateID: "state", Type: "source", Key: "customers", Title: "Customers", PayloadJSON: `{}`}
	connection := servingstate.Asset{ID: "connection:warehouse", ProjectID: "project:test", ServingStateID: "state", Type: "connection", Key: "warehouse", Title: "Warehouse", PayloadJSON: `{}`}
	edge := servingstate.AssetEdge{ID: "edge:source-connection", ProjectID: "project:test", ServingStateID: "state", FromAssetID: source.ID, ToAssetID: connection.ID, Type: "uses_connection"}
	newHandler := func() *BrowserHandler {
		return &BrowserHandler{
			Graph:            browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{source, otherSource, connection}, Edges: []servingstate.AssetEdge{edge}}},
			ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
			Environment:      "dev",
			CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
		}
	}
	assertParent := func(name string, payload any) {
		t.Helper()
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"parentTitle":"Warehouse"`, `"parentHref":"/connections/connection:warehouse/details"`} {
			if !strings.Contains(string(encoded), want) {
				t.Fatalf("%s payload = %s, missing %s", name, encoded, want)
			}
		}
	}

	h := newHandler()
	bootstrap, ok := h.projectBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?area=sources", nil))
	if !ok {
		t.Fatal("source bootstrap returned not ok")
	}
	assertParent("bootstrap", bootstrap)

	searchRecorder := httptest.NewRecorder()
	signals := url.QueryEscape(`{"projectAssetQuery":"orders"}`)
	h.SourcesSearch(searchRecorder, httptest.NewRequest(stdhttp.MethodGet, "/sources/search?datastar="+signals, nil))
	if searchRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("source search status = %d, want 200", searchRecorder.Code)
	}
	if body := searchRecorder.Body.String(); !strings.Contains(body, "Warehouse") || !strings.Contains(body, "/connections/connection:warehouse/details") || strings.Contains(body, "Customers") {
		t.Fatalf("source search payload = %q, want matching source with parent context only", body)
	}

	pageRecorder := httptest.NewRecorder()
	h.Sources(pageRecorder, httptest.NewRequest(stdhttp.MethodGet, "/sources", nil))
	if pageRecorder.Code != stdhttp.StatusOK || !strings.Contains(pageRecorder.Body.String(), "/updates?area=sources") {
		t.Fatalf("source page status/body = %d %q, want source bootstrap URL", pageRecorder.Code, pageRecorder.Body.String())
	}

	deniedParent := newHandler()
	deniedParent.Catalog = sourceOnlyBrowserCatalogStub{}
	deniedParent.CurrentUser = func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true }
	deniedBootstrap, ok := deniedParent.projectBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?area=sources", nil))
	if !ok {
		t.Fatal("authorized source bootstrap returned not ok")
	}
	encoded, err := json.Marshal(deniedBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Warehouse") || strings.Contains(string(encoded), "connection:warehouse") {
		t.Fatalf("source bootstrap leaked unauthorized connection: %s", encoded)
	}
}

func TestAssetsConsumeCatalogPages(t *testing.T) {
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, Environment: "dev", Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: "model:allowed", ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "allowed", PayloadJSON: "{}"}, {ID: "model:second", ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "second", PayloadJSON: "{}"}},
		}},
		Catalog: pagedBrowserCatalogStub{}, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	_, assets, _, ok := h.assets(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/models", nil))
	if !ok || len(assets) != 2 {
		t.Fatalf("assets = %#v, ok=%v; want both catalog pages", assets, ok)
	}
}

func TestAssetsDoesNotMutateSharedServingGraph(t *testing.T) {
	denied := projectgraph.ResourceID("model:denied")
	allowed := projectgraph.ResourceID("model:allowed")
	graph := servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: denied, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "denied", PayloadJSON: "{}"},
		{ID: allowed, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "allowed", PayloadJSON: "{}"},
	}}
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, Environment: "dev", Graph: browserGraphStub{graph: graph},
		Catalog: browserCatalogStub{}, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	if _, _, _, ok := h.assets(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/models", nil)); !ok {
		t.Fatal("assets returned not ok")
	}
	if graph.Assets[0].ID != denied || graph.Assets[1].ID != allowed {
		t.Fatalf("shared graph mutated: %#v", graph.Assets)
	}
}

func TestSourcesRequiresVisibleSourceRatherThanUnrelatedResource(t *testing.T) {
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, Environment: "dev",
		Catalog:     kindAwareBrowserCatalogStub{available: projectgraph.KindModel},
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	recorder := httptest.NewRecorder()
	h.Sources(recorder, httptest.NewRequest(stdhttp.MethodGet, "/sources", nil))
	if recorder.Code != stdhttp.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, stdhttp.StatusForbidden)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "data page") || !strings.Contains(body, "Return to Insights") {
		t.Fatalf("forbidden source recovery body = %q", body)
	}
}

func TestExploreRequiresVisibleSemanticModel(t *testing.T) {
	h := &BrowserHandler{
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, Environment: "dev",
		Catalog:     kindAwareBrowserCatalogStub{available: projectgraph.KindModel},
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	recorder := httptest.NewRecorder()
	h.Explore(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore", nil))
	if recorder.Code != stdhttp.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, stdhttp.StatusForbidden)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "data page") || !strings.Contains(body, "Return to Insights") {
		t.Fatalf("forbidden Explorer recovery body = %q", body)
	}
}
