package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	dashboardui "github.com/flidai/leapview/internal/dashboard/ui"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type exploreHandoffMetrics struct {
	fakeMetrics
	definition dashboarddefinition.Definition
	page       dashboard.Page
	model      *semanticmodel.Model
}

func (m exploreHandoffMetrics) Resolver() dashboardresolver.Resolver {
	model := m.model
	if model == nil {
		model = exploreHandoffModel()
	}
	return exploreHandoffResolver{definition: m.definition, model: model}
}

func (m exploreHandoffMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != m.definition.ID {
		return nil
	}
	return []dashboard.Page{m.page}
}

func (m exploreHandoffMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	model := m.model
	if model == nil {
		model = exploreHandoffModel()
	}
	if strings.TrimSpace(m.definition.SemanticModel) == modelID {
		return model, true
	}
	for _, visual := range m.definition.Visualizations {
		if strings.TrimSpace(visual.Query.ModelID) == modelID {
			return model, true
		}
	}
	return nil, false
}

type exploreHandoffResolver struct {
	definition dashboarddefinition.Definition
	model      *semanticmodel.Model
}

func (r exploreHandoffResolver) Resolve(dashboardID projectgraph.ResourceID) (dashboardresolver.Resolved, error) {
	if dashboardID.String() != r.definition.ID {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{Definition: r.definition, Model: r.model}, nil
}

func TestExploreVisualizationRedirectsWithCompiledQueryAndSourceContext(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	store, request := exploreHandoffSession(t, definition, "client-1", "stream-1", dashboardsession.State{})
	handler := exploreHandoffAuthorizedHandler(exploreHandoffMetrics{definition: definition, page: page}, store)
	response := httptest.NewRecorder()
	testRouter(handler).ServeHTTP(response, request)
	if response.Code != nethttp.StatusSeeOther {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if location.Path != "/explore" || location.Query().Get("mode") != "explore" || location.Query().Get("v") != "2" {
		t.Fatalf("Location = %q", location)
	}
	if location.Query().Get("returnTo") != "/dashboards/dash/pages/overview" || location.Query().Get("sourceVisual") != "revenue" {
		t.Fatalf("source context = %#v", location.Query())
	}
	var spec exploration.ExplorationSpec
	if err := json.Unmarshal([]byte(location.Query().Get("state")), &spec); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if spec.ModelID != "semantic:sales" || spec.DatasetID == nil || *spec.DatasetID != "orders" || spec.Limit != 80 {
		t.Fatalf("spec identity/limit = %#v", spec)
	}
	if len(spec.Dimensions) != 1 || spec.Dimensions[0].Field != "orders.purchase_date" || spec.Dimensions[0].Alias == nil || *spec.Dimensions[0].Alias != "purchase_month" || spec.Dimensions[0].Grain == nil || *spec.Dimensions[0].Grain != exploration.ExplorationTimeGrainMonth {
		t.Fatalf("spec dimensions = %#v", spec.Dimensions)
	}
	if len(spec.Metrics) != 1 || spec.Metrics[0].Field != "revenue" || len(spec.Sort) != 1 || spec.Sort[0].Field != "orders.purchase_date" || spec.Sort[0].Direction != exploration.ExplorationSortDirectionDesc {
		t.Fatalf("spec metrics/sort = %#v / %#v", spec.Metrics, spec.Sort)
	}
}

func TestExploreVisualizationDoesNotRedirectWhenSemanticModelIsNotProjectVisible(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	store, request := exploreHandoffSession(t, definition, "client-hidden-model", "stream-hidden-model", dashboardsession.State{})
	handler := exploreHandoffAuthorizedHandler(exploreHandoffMetrics{definition: definition, page: page}, store)
	handler.AuthorizeListResource = func(context.Context, string, access.ResourceRef, access.Capability) (bool, error) {
		return false, nil
	}
	response := httptest.NewRecorder()
	testRouter(handler).ServeHTTP(response, request)
	if response.Code != nethttp.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("hidden semantic model response = %d Location=%q body=%q", response.Code, response.Header().Get("Location"), response.Body.String())
	}
}

func TestExploreVisualizationLocationRestoresInAuthorizedExplorer(t *testing.T) {
	for _, visualID := range []string{"category_revenue", "revenue_by_month"} {
		t.Run(visualID, func(t *testing.T) { testExploreVisualizationLocationRestoresInAuthorizedExplorer(t, visualID, false) })
		t.Run(visualID+"_filtered", func(t *testing.T) { testExploreVisualizationLocationRestoresInAuthorizedExplorer(t, visualID, true) })
	}
}

func testExploreVisualizationLocationRestoresInAuthorizedExplorer(t *testing.T, visualID string, activeFilters bool) {
	project, err := projectcompiler.LoadSourceRoot("../../../dashboards")
	if err != nil {
		t.Fatalf("compile dashboards fixture: %v", err)
	}
	definition, ok := project.Manifest.DashboardDefinitions["dashboard:executive-sales"]
	if !ok {
		t.Fatal("compiled project omitted Executive Sales dashboard")
	}
	visual, ok := definition.Visualizations[visualID]
	if !ok {
		t.Fatal("compiled Executive Sales omitted category_revenue visual")
	}
	model := project.Manifest.SemanticModels[definition.SemanticModel]
	if model == nil {
		t.Fatalf("compiled semantic model %q is missing", definition.SemanticModel)
	}
	var page dashboard.Page
	for _, candidate := range definition.Pages {
		if pageContainsVisual(candidate, visual.ID) {
			page = candidate
			break
		}
	}
	if page.ID == "" {
		t.Fatal("compiled Executive Sales did not place category_revenue on a page")
	}

	const clientID = "handoff-client"
	const streamID = "handoff-stream"
	const projectID = projectgraph.ResourceID("project:test")
	filters := definition.DefaultFilterState()
	if activeFilters {
		for key, binding := range definition.CompiledFilterBindings() {
			filter := definition.FilterDefinitions[binding.Filter]
			if filter.Field != "category" {
				continue
			}
			expression := dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals, Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "Health"}}
			filters.AppliedControls[key] = dashboardfilter.AppliedState{Expression: expression, ResolvedExpression: expression}
		}
	}
	key := dashboardsession.Key{
		ProjectID: projectID, PrincipalOrClient: "alice:" + clientID, DashboardID: projectgraph.ResourceID(definition.ID),
		ServingStateID: filters.DefaultsRevision, StreamInstanceID: streamID,
	}
	if err := key.Validate(); err != nil {
		t.Fatal(err)
	}
	store := dashboardsession.NewMemoryStore()
	if _, err := store.Create(context.Background(), key, dashboardsession.NewState(page.ID, dashboardfilter.MachineSnapshot{
		Version: dashboardfilter.MachineSnapshotVersion, State: filters,
	})); err != nil {
		t.Fatalf("create dashboard session: %v", err)
	}
	routeQuery := url.Values{"clientId": {clientID}, "streamInstanceId": {streamID}}
	path := "/dashboards/" + url.PathEscape(definition.ID) + "/pages/" + url.PathEscape(page.ID) + "/visuals/" + url.PathEscape(visual.ID) + "/explore?" + routeQuery.Encode()
	routeRequest := httptest.NewRequest(nethttp.MethodGet, path, nil)
	routeResponse := httptest.NewRecorder()
	handler := Handler{
		Metrics:            exploreHandoffMetrics{definition: definition, page: page, model: model},
		ProjectID:          projectID,
		SessionStore:       store,
		CurrentPrincipalID: func(*nethttp.Request) string { return "alice" },
		AuthorizeListResource: func(_ context.Context, principalID string, resource access.ResourceRef, capability access.Capability) (bool, error) {
			return principalID == "alice" && resource.ID().String() == definition.SemanticModel && resource.Kind() == projectgraph.KindSemanticModel && capability == access.CapabilityResourceRead, nil
		},
	}
	testRouter(handler).ServeHTTP(routeResponse, routeRequest)
	if routeResponse.Code != nethttp.StatusSeeOther {
		t.Fatalf("handoff route status = %d, body = %q", routeResponse.Code, routeResponse.Body.String())
	}
	location, err := url.Parse(routeResponse.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse handoff Location: %v", err)
	}
	var spec exploration.ExplorationSpec
	if err := json.Unmarshal([]byte(location.Query().Get("state")), &spec); err != nil {
		t.Fatalf("decode handoff state: %v", err)
	}
	if spec.DatasetID == nil || *spec.DatasetID != "sales_orders" || len(spec.Dimensions) != 1 || len(spec.Sort) != 1 {
		t.Fatalf("handoff state lacks Explorer field identities: %#v", spec)
	}
	if activeFilters && len(spec.Filters) != 1 {
		t.Fatalf("active category filter lost: %#v", spec.Filters)
	}
	if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
		t.Fatalf("handoff state fails semantic model validation: %v", err)
	}

	compiledModels := map[string]*semanticquery.CompiledModel{}
	for modelID, semantic := range project.Manifest.SemanticModels {
		compiled, err := semanticquery.CompileModel(semantic)
		if err != nil {
			t.Fatalf("compile Explorer bindings for %s: %v", modelID, err)
		}
		compiledModels[modelID] = compiled
	}
	assets := make([]servingstate.Asset, 0, len(project.Manifest.Models)+len(project.Manifest.SemanticModels))
	for resourceID, table := range project.Manifest.Models {
		assets = append(assets, servingstate.Asset{ID: projectgraph.ResourceID(resourceID), ProjectID: projectID, ServingStateID: "generation-1", Type: "model", Key: table.ModelName, Title: table.ModelName, PayloadJSON: `{}`})
	}
	for resourceID, semantic := range project.Manifest.SemanticModels {
		assets = append(assets, servingstate.Asset{ID: projectgraph.ResourceID(resourceID), ProjectID: projectID, ServingStateID: "generation-1", Type: "semantic_model", Key: semantic.Name, Title: semantic.Title, PayloadJSON: `{}`})
	}
	explorer := &projecthttp.BrowserHandler{
		Graph:                   exploreHandoffExplorerGraph{graph: servingstate.AssetGraph{Assets: assets}},
		ProjectDefinitionReader: exploreHandoffProjectDefinition{definition: project.Manifest, compiled: compiledModels},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Catalog:                 exploreHandoffProjectCatalog{assets: assets},
		CurrentUser:             func(*nethttp.Request) (projecthttp.Principal, bool) { return projecthttp.Principal{ID: "alice"}, true },
		Environment:             "dev",
	}
	explorerRequest := httptest.NewRequest(nethttp.MethodGet, location.RequestURI(), nil)
	explorerResponse := httptest.NewRecorder()
	explorer.Explore(explorerResponse, explorerRequest)
	if explorerResponse.Code != nethttp.StatusOK {
		body := explorerResponse.Body.String()
		if len(body) > 500 {
			body = body[:500]
		}
		t.Fatalf("authorized Explorer restore status = %d, body = %q; Location=%s", explorerResponse.Code, body, strings.TrimSpace(routeResponse.Header().Get("Location")))
	}
}

func exploreHandoffAuthorizedHandler(metrics exploreHandoffMetrics, store dashboardsession.Store) Handler {
	return Handler{
		Metrics: metrics, ProjectID: "project:test", SessionStore: store,
		CurrentPrincipalID: func(*nethttp.Request) string { return "alice" },
		AuthorizeListResource: func(_ context.Context, principalID string, resource access.ResourceRef, capability access.Capability) (bool, error) {
			return principalID == "alice" && resource.Kind() == projectgraph.KindSemanticModel && capability == access.CapabilityResourceRead, nil
		},
	}
}

type exploreHandoffExplorerGraph struct{ graph servingstate.AssetGraph }

func (g exploreHandoffExplorerGraph) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	return g.graph, true, nil
}

type exploreHandoffProjectDefinition struct {
	definition projectmanifest.ResourceManifest
	compiled   map[string]*semanticquery.CompiledModel
}

func (d exploreHandoffProjectDefinition) ProjectDefinitionSnapshot(context.Context) (projectmanifest.ResourceManifest, map[string]*semanticquery.CompiledModel, error) {
	return d.definition, d.compiled, nil
}

type exploreHandoffProjectCatalog struct{ assets []servingstate.Asset }

func (c exploreHandoffProjectCatalog) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	allowedKinds := make(map[projectgraph.Kind]bool, len(request.Kinds))
	for _, kind := range request.Kinds {
		allowedKinds[kind] = true
	}
	page := projectcatalog.Page{Items: []projectcatalog.Result{}}
	for _, asset := range c.assets {
		kind := projectgraph.Kind("")
		switch asset.Type {
		case "model":
			kind = projectgraph.KindModel
		case "semantic_model":
			kind = projectgraph.KindSemanticModel
		}
		if kind == "" || !allowedKinds[kind] {
			continue
		}
		page.Items = append(page.Items, projectcatalog.Result{Ref: projectcatalog.Ref{ID: asset.ID, Kind: kind}, Name: asset.Key, DisplayName: asset.Title})
	}
	return page, nil
}

func (exploreHandoffProjectCatalog) Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

func TestExploreVisualizationRejectsSessionWithInteractionSelections(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	state := dashboardsession.State{InteractionSelections: []map[string]any{{"field": "orders.region"}}}
	store, request := exploreHandoffSession(t, definition, "client-2", "stream-2", state)
	handler := exploreHandoffAuthorizedHandler(exploreHandoffMetrics{definition: definition, page: page}, store)
	response := httptest.NewRecorder()
	testRouter(handler).ServeHTTP(response, request)
	if response.Code != nethttp.StatusConflict {
		t.Fatalf("status = %d, body = %q; want active interaction to fail closed", response.Code, response.Body.String())
	}
}

func TestExploreVisualizationRejectsUnsupportedQueryAndUnexpectedContext(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	unsupported := definition.Visualizations["revenue"]
	unsupported.Query.Kind = visualizationdefinition.QueryDetail
	unsupported.Query.Aggregate = nil
	unsupported.Query.Detail = &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "orders.id", Alias: "id"}}, Limit: 80}
	definition.Visualizations["revenue"] = unsupported
	store, request := exploreHandoffSession(t, definition, "client-3", "stream-3", dashboardsession.State{})
	handler := Handler{Metrics: exploreHandoffMetrics{definition: definition, page: page}, ProjectID: "project:test", SessionStore: store}
	response := httptest.NewRecorder()
	testRouter(handler).ServeHTTP(response, request)
	if response.Code != nethttp.StatusUnprocessableEntity {
		t.Fatalf("unsupported query status = %d, body = %q", response.Code, response.Body.String())
	}

	request, err := nethttp.NewRequest(nethttp.MethodGet, request.URL.String()+"&filter=anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	testRouter(Handler{Metrics: exploreHandoffMetrics{definition: definition, page: page}, ProjectID: "project:test", SessionStore: store}).ServeHTTP(response, request)
	if response.Code != nethttp.StatusBadRequest {
		t.Fatalf("unexpected context status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestExploreVisualizationIsUnavailableInScopedCandidatePreview(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/pages/overview/visuals/revenue/explore", nil)
	testRouter(Handler{RouteScope: dashboardui.RouteScope{BasePath: "/candidates/candidate-1/projects/project-1"}}).ServeHTTP(response, request)
	if response.Code != nethttp.StatusNotFound {
		t.Fatalf("scoped preview status = %d, want concealed 404", response.Code)
	}
}

func TestRefreshVisualPatchCarriesCurrentExploreHrefAndClearsItForActiveState(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	handler := Handler{}
	filters := dashboard.Filters{CompiledState: func() *dashboardfilter.State {
		state := definition.DefaultFilterState()
		return &state
	}()}
	base := dashboardstream.Envelope{Signals: map[string]any{"visuals": map[string]dashboarddatastar.VisualizationSignal{"revenue": {VisualID: "revenue"}}}}
	event := dashboardstream.RefreshEvent{Type: dashboardstream.RefreshEventVisual, Target: "revenue", Filters: filters}
	decorated := handler.decorateExploreHrefsForAuthorizedVisuals(base, event, definition, exploreHandoffModel(), page, "client-1", "stream-1", map[string]bool{"revenue": true})
	visual := decorated.Signals["visuals"].(map[string]dashboarddatastar.VisualizationSignal)["revenue"]
	if visual.ExploreHref != "/dashboards/dash/pages/overview/visuals/revenue/explore?clientId=client-1&streamInstanceId=stream-1" {
		t.Fatalf("ExploreHref = %q", visual.ExploreHref)
	}
	decorated = handler.decorateExploreHrefsForAuthorizedVisuals(base, event, definition, exploreHandoffModel(), page, "client-1", "stream-1", nil)
	visual = decorated.Signals["visuals"].(map[string]dashboarddatastar.VisualizationSignal)["revenue"]
	if visual.ExploreHref != "" {
		t.Fatalf("unverified model visibility published ExploreHref = %q", visual.ExploreHref)
	}

	event.Filters.Selections = []dashboard.InteractionSelection{{ID: "selection-1"}}
	decorated = handler.decorateExploreHrefsForAuthorizedVisuals(base, event, definition, exploreHandoffModel(), page, "client-1", "stream-1", map[string]bool{"revenue": true})
	visual = decorated.Signals["visuals"].(map[string]dashboarddatastar.VisualizationSignal)["revenue"]
	if visual.ExploreHref != "" {
		t.Fatalf("active interaction retained ExploreHref = %q", visual.ExploreHref)
	}
}

func TestVisualWindowStartDoesNotAddUnrequestedExploreVisuals(t *testing.T) {
	definition, page := exploreHandoffDefinition()
	base := dashboardstream.Envelope{Signals: map[string]any{
		"visuals": map[string]any{"order_rows": map[string]any{"status": map[string]any{"kind": "loading"}}},
	}}
	event := dashboardstream.RefreshEvent{Type: dashboardstream.RefreshEventStart, Command: "visual_window", Targets: []string{"visual:order_rows"}}
	decorated := (Handler{}).decorateExploreHrefsForAuthorizedVisuals(base, event, definition, exploreHandoffModel(), page, "client-1", "stream-1", map[string]bool{"revenue": true})
	visuals := decorated.Signals["visuals"].(map[string]any)
	if len(visuals) != 1 || visuals["order_rows"] == nil {
		t.Fatalf("visual-window patch includes unrequested visuals: %#v", visuals)
	}
}

func exploreHandoffDefinition() (dashboarddefinition.Definition, dashboard.Page) {
	page := dashboard.Page{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "component", Visual: "revenue"}}}
	visual := visualizationdefinition.Definition{
		ID: "revenue",
		Query: visualizationdefinition.QueryBinding{
			Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales", DatasetID: "primary",
			Aggregate: &visualizationdefinition.AggregateQueryBinding{
				TableID:    "orders",
				Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "purchase_date", Alias: "purchase_month", Grain: "month"}},
				Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}},
				Sort:       []visualizationdefinition.Sort{{FieldID: "purchase_month", Direction: "desc"}},
				Limit:      80,
			},
		},
	}
	definition := dashboarddefinition.Definition{
		ID: "dash", SemanticModel: "semantic:sales", Pages: []dashboard.Page{page},
		Visualizations: map[string]visualizationdefinition.Definition{"revenue": visual},
	}
	return definition, page
}

func exploreHandoffModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:       "sales",
		Tables:     map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"purchase_date": {Type: "date", Datatype: semanticmodel.DataTypeDate}}}},
		Dimensions: map[string]semanticmodel.SemanticDimension{"purchase_date": {Type: "date", Datatype: semanticmodel.DataTypeDate, Timezone: "UTC", Calendar: "gregorian", WeekStart: "sunday", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.purchase_date"}}}},
		Metrics:    map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders"}},
		Datasets:   map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
}

func exploreHandoffSession(t *testing.T, definition dashboarddefinition.Definition, clientID, streamID string, state dashboardsession.State) (*dashboardsession.MemoryStore, *nethttp.Request) {
	t.Helper()
	store := dashboardsession.NewMemoryStore()
	defaults := definition.DefaultFilterState()
	key := dashboardsession.Key{
		ProjectID: "project:test", PrincipalOrClient: "alice:" + clientID, DashboardID: "dash",
		ServingStateID: defaults.DefaultsRevision, StreamInstanceID: streamID,
	}
	if err := key.Validate(); err != nil {
		t.Fatal(err)
	}
	if state.ActivePage == "" {
		state.ActivePage = "overview"
	}
	if state.Filters.State.DefaultsRevision == "" {
		state.Filters = dashboardfilter.MachineSnapshot{Version: dashboardfilter.MachineSnapshotVersion, State: defaults}
	}
	if _, err := store.Create(context.Background(), key, state); err != nil {
		t.Fatalf("create dashboard session: %v", err)
	}
	request := httptest.NewRequest(nethttp.MethodGet, "/dashboards/dash/pages/overview/visuals/revenue/explore?clientId="+clientID+"&streamInstanceId="+streamID, nil)
	return store, request
}
