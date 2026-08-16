package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
	want := []string{"GET /", "GET /connections", "GET /connections/{asset}/{section}", "GET /data", "GET /data/{asset}/{section}", "POST /data/search", "GET /explore", "GET /models", "GET /models/{asset}/{section}", "GET /pipelines", "GET /pipelines/{asset}/{section}", "GET /semantic-models", "GET /semantic-models/{asset}/{section}", "POST /catalog/search", "POST /connections/search"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for _, legacy := range []string{"/workspaces", "/workspaces/{workspace}", "/admin/workspaces"} {
		for _, route := range got {
			if route == "GET "+legacy || route == "POST "+legacy {
				t.Fatalf("legacy route %q was mounted", legacy)
			}
		}
	}
}

type browserGraphStub struct{ graph servingstate.AssetGraph }

func (s browserGraphStub) ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error) {
	return s.graph, true, nil
}

type browserCatalogStub struct{}

func (browserCatalogStub) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:allowed", Kind: projectgraph.KindModel}}}}, nil
}

func (browserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

type pagedBrowserCatalogStub struct{}

func (pagedBrowserCatalogStub) List(_ context.Context, request projectcatalog.ListRequest) (projectcatalog.Page, error) {
	if request.Cursor == "" {
		return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:allowed", Kind: projectgraph.KindModel}}}, NextCursor: "page-2"}, nil
	}
	return projectcatalog.Page{Items: []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "model:second", Kind: projectgraph.KindModel}}}}, nil
}

func (pagedBrowserCatalogStub) Resolve(context.Context, string, projectcatalog.Ref, access.Capability) (projectcatalog.Result, error) {
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

func TestAssetsFilterUnauthorizedSiblingAndEdges(t *testing.T) {
	allowed := projectgraph.ResourceID("model:allowed")
	denied := projectgraph.ResourceID("model:denied")
	h := &BrowserHandler{
		ProjectID: "project:test", Environment: "dev", Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: allowed, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "allowed", Title: "Allowed", PayloadJSON: "{}"}, {ID: denied, ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "denied", Title: "Denied", PayloadJSON: "{}"}},
			Edges:  []servingstate.AssetEdge{{ID: "edge", ProjectID: "project:test", ServingStateID: "state", FromAssetID: allowed, ToAssetID: denied, Type: "depends_on"}},
		}},
		Catalog: browserCatalogStub{}, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	_, assets, edges, ok := h.assets(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/data", nil))
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
		ProjectID: "project:test", Environment: "dev", Graph: browserGraphStub{graph: servingstate.AssetGraph{
			Assets: []servingstate.Asset{{ID: "model:allowed", ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "allowed", PayloadJSON: "{}"}, {ID: "model:second", ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "second", PayloadJSON: "{}"}},
		}},
		Catalog: pagedBrowserCatalogStub{}, CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "alice"}, true },
	}
	_, assets, _, ok := h.assets(httptest.NewRecorder(), httptest.NewRequest(stdhttp.MethodGet, "/models", nil))
	if !ok || len(assets) != 2 {
		t.Fatalf("assets = %#v, ok=%v; want both catalog pages", assets, ok)
	}
}
