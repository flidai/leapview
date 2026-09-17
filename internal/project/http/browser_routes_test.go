package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

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
	want := []string{"GET /", "GET /catalog/search", "GET /connections", "GET /connections/search", "GET /connections/{asset}/{section}", "GET /dashboards", "GET /dashboards/search", "GET /dashboards/{asset}/definition", "GET /dashboards/{asset}/details", "GET /dashboards/{asset}/lineage", "GET /dashboards/{asset}/versions", "GET /explore", "POST /explore/command", "GET /models", "GET /models/search", "GET /models/{asset}/{section}", "POST /models/{asset}/data/command", "GET /pipelines", "GET /pipelines/{asset}/{section}", "POST /pipelines/command", "GET /runs", "GET /search", "GET /semantic-models", "GET /semantic-models/search", "GET /semantic-models/{asset}/{section}", "POST /semantic-models/{asset}/data/command", "GET /sources", "GET /sources/search", "GET /sources/{asset}/{section}", "POST /connections/administration/configuration", "POST /connections/administration/lifecycle", "POST /dashboards/{asset}/appearance"}
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

func TestInvalidAssetSectionsReturnNotFoundBeforeDefinitionEnrichment(t *testing.T) {
	const assetID = "model:orders"
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{
			ID: assetID, ProjectID: "project:test", ServingStateID: "state", Type: "model", Key: "orders", PayloadJSON: `{}`,
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
