package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
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
	want := []string{"GET /", "GET /connections", "GET /connections/{asset}/{section}", "GET /explore", "POST /explore/command", "GET /models", "GET /models/{asset}/{section}", "POST /models/{asset}/data/command", "POST /models/search", "GET /pipelines", "GET /pipelines/{asset}/{section}", "GET /semantic-models", "GET /semantic-models/{asset}/{section}", "POST /semantic-models/{asset}/data/command", "POST /semantic-models/search", "GET /sources", "GET /sources/{asset}/{section}", "POST /sources/search", "POST /catalog/search", "POST /connections/search"}
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

type browserSemanticModelStub struct{ model *semanticmodel.Model }

func (s browserSemanticModelStub) SemanticModel(string) (*semanticmodel.Model, bool) {
	return s.model, s.model != nil
}

type browserProjectDefinitionStub struct {
	definition projectmanifest.Project
	err        error
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

func (s browserProjectDefinitionStub) ProjectDefinition(context.Context) (projectmanifest.Project, error) {
	return s.definition, s.err
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
				Sources: []string{"geolocations"}, Transform: semanticmodel.Transform{SQL: "select zip_code from source.geolocations"},
				PrimaryKey: "zip_code", Grain: "zip_code", Dimensions: map[string]semanticmodel.MetricDimension{"zip_code": {Label: "ZIP code"}},
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
	for _, want := range []string{"Fields (1)", `"label":"Input sources","value":"1"`, "select zip_code from source.geolocations"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("bootstrap = %s, missing %q", encoded, want)
		}
	}
}

func TestDataExplorerSignalsUseAuthorizedActiveDefinition(t *testing.T) {
	const projectID = "project:test"
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {Grain: "order_id", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
	}}
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
		}},
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
		Dimensions: []string{"orders.status"}, Measures: []string{"orders"}, Filters: []projectsignals.DataExploreFilterSignal{},
		Sort: []projectsignals.DataExploreSortSignal{{Field: "orders", Direction: "desc"}}, Limit: 100,
	}, []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Label: "Status", Kind: "dimension", Compatible: true},
		{ID: "orders", Label: "Orders", Kind: "measure", Compatible: true},
	})

	if result.Error != nil || result.RowsReturned != 1 || len(result.Rows) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(command.Dimensions) != 1 || len(command.Measures) != 1 {
		t.Fatalf("normalized command = %#v", command)
	}
	if executor.query.Kind != dataquery.KindSemanticAggregate || executor.query.ProjectID != "project:test" || executor.query.Operation != dataquery.OperationSemanticExplore {
		t.Fatalf("query = %#v", executor.query)
	}
	if executor.query.Fields[0].Alias != "status" || executor.query.Measures[0].Alias != "orders" {
		t.Fatalf("query aliases = %#v / %#v", executor.query.Fields, executor.query.Measures)
	}
}

func TestAssetDataExplorerScopesModelsAndSemanticModels(t *testing.T) {
	const projectID = "project:test"
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{
		"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
	}}
	executor := &browserDataQueryStub{result: dataquery.Result{Rows: []dataquery.Row{{"status": "paid"}}, TotalRows: 1, TotalRowsKnown: true}}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: "model:orders", ProjectID: projectID, Type: "model_table", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: "semantic-model:sales", ProjectID: projectID, Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID: projectID, Models: map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
			SemanticModels: map[string]*semanticmodel.Model{"semantic-model:sales": model}, NameIndex: projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
		}},
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
			"orders":    {PrimaryKey: "order_id", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"customers": {PrimaryKey: "customer_id"},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count"},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customer", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one"}},
	}
	const projectID = "project:test"
	const assetID = "semantic:sales"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: assetID, ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{"kind":"semantic_model"}`}},
		}},
		SemanticModelReader: browserSemanticModelStub{model: model},
		ResolveProjectID:    func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:         "dev",
		CurrentUser:         func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	patch, ok := h.assetBootstrap(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/updates?surface=asset&asset="+assetID+"&section=details", nil))
	if !ok {
		t.Fatal("asset bootstrap returned not ok")
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Model tables (2)", "Measures (1)", "Relationships (1)"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("bootstrap = %s, missing %q", encoded, want)
		}
	}
	if !strings.Contains(string(encoded), `"nodes"`) || !strings.Contains(string(encoded), `"edges"`) {
		t.Fatalf("bootstrap = %s, semantic graph missing nodes or edges", encoded)
	}
}

func TestSemanticModelAssetBootstrapRejectsMissingCompiledModel(t *testing.T) {
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: "semantic:sales", ProjectID: "project:test", ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{"kind":"semantic_model"}`}},
		}},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		Environment:      "dev",
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
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
}
