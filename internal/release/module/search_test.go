package module

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectapi "github.com/flidai/leapview/internal/project/api"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
)

type searchCatalogFake struct {
	page    projectcatalog.Page
	err     error
	request projectcatalog.SearchRequest
}

func (f *searchCatalogFake) Search(_ context.Context, request projectcatalog.SearchRequest) (projectcatalog.Page, error) {
	f.request = request
	return f.page, f.err
}

func TestSearchUsesActiveCatalogAndMapsStableResults(t *testing.T) {
	fake := &searchCatalogFake{page: projectcatalog.Page{Items: []projectcatalog.Result{{
		Ref:  projectcatalog.Ref{ID: projectgraph.ResourceID("dashboard_sales"), Kind: projectgraph.KindDashboard},
		Name: "Sales", DisplayName: "Sales dashboard", Domain: "commerce", Owner: "owner-1", Tags: []string{"revenue"},
	}}, NextCursor: "next"}}
	module := &Module{searchCatalog: fake, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) {
		return Principal{ID: "principal-1"}, true
	}}}
	params := projectapi.SearchParams{Q: "sales"}
	response := httptest.NewRecorder()
	module.Search(response, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=sales", nil), params)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if fake.request.PrincipalID != "principal-1" || fake.request.Query != "sales" || len(fake.request.Kinds) != 7 {
		t.Fatalf("catalog request=%#v", fake.request)
	}
	for _, want := range []string{`"kind":"dashboard"`, `"id":"dashboard_sales"`, `"displayName":"Sales dashboard"`, `"nextCursor":"next"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("body=%s missing %s", response.Body.String(), want)
		}
	}
}

func TestSearchFailsClosedForAuthenticationCatalogAndInvalidKinds(t *testing.T) {
	tests := []struct {
		name   string
		module *Module
		params projectapi.SearchParams
		status int
		want   string
	}{
		{name: "unauthenticated", module: &Module{searchCatalog: &searchCatalogFake{}, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{}, false }}}, params: projectapi.SearchParams{Q: "sales"}, status: http.StatusUnauthorized, want: "AUTHENTICATION_REQUIRED"},
		{name: "catalog unavailable", module: &Module{searchCatalog: &searchCatalogFake{err: projectcatalog.ErrUnavailable}, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-1"}, true }}}, params: projectapi.SearchParams{Q: "sales"}, status: http.StatusServiceUnavailable, want: "Project search is temporarily unavailable"},
		{name: "invalid kind", module: &Module{searchCatalog: &searchCatalogFake{}, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-1"}, true }}}, params: projectapi.SearchParams{Q: "sales", Kind: &[]projectapi.SearchKind{"visual"}}, status: http.StatusBadRequest, want: "INVALID_SEARCH_KIND"},
		{name: "invalid cursor", module: &Module{searchCatalog: &searchCatalogFake{err: projectcatalog.ErrInvalidCursor}, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-1"}, true }}}, params: projectapi.SearchParams{Q: "sales"}, status: http.StatusBadRequest, want: "INVALID_SEARCH_REQUEST"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.module.Search(response, httptest.NewRequest(http.MethodGet, "/api/v1/search", nil), test.params)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("body=%s missing %s", response.Body.String(), test.want)
			}
			if test.name == "catalog unavailable" && strings.Contains(response.Body.String(), "catalog unavailable") {
				t.Fatal("backend catalog error leaked")
			}
		})
	}
}

func TestSearchAPIGenDispatcherReachesCatalogHandler(t *testing.T) {
	fake := &searchCatalogFake{page: projectcatalog.Page{Items: []projectcatalog.Result{{
		Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("model_sales"), Kind: projectgraph.KindModel}, Name: "Sales model",
	}}}}
	module := &Module{searchCatalog: fake, api: APIConfig{CurrentPrincipal: func(*http.Request) (Principal, bool) {
		return Principal{ID: "principal-1"}, true
	}}}
	response := httptest.NewRecorder()
	handled := projecthttp.DispatchAPIGenOperation("search", module, slog.Default(), response, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=sales", nil))
	if !handled {
		t.Fatal("search operation was not dispatched")
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"model_sales"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSearchErrorStatusTreatsUnknownCatalogFailuresAsUnavailable(t *testing.T) {
	status, code := searchErrorStatus(errors.New("database details"))
	if status != http.StatusServiceUnavailable || code != "SEARCH_UNAVAILABLE" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}
