package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type productSearchCatalogFake struct {
	request projectcatalog.SearchRequest
}

func (f *productSearchCatalogFake) Search(_ context.Context, request projectcatalog.SearchRequest) (projectcatalog.Page, error) {
	f.request = request
	return projectcatalog.Page{Items: []projectcatalog.Result{
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("dashboard:sales"), Kind: projectgraph.KindDashboard}, Name: "sales", DisplayName: "Sales dashboard"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("model:orders"), Kind: projectgraph.KindModel}, Name: "orders", Description: "Governed orders"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("source:orders"), Kind: projectgraph.KindSource}, Name: "orders source"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("connection:warehouse"), Kind: projectgraph.KindConnection}, Name: "warehouse"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("semantic-model:sales"), Kind: projectgraph.KindSemanticModel}, Name: "sales semantics"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("pipeline:refresh"), Kind: projectgraph.KindPipeline}, Name: "refresh"},
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("project:demo"), Kind: projectgraph.KindProject}, Name: "demo"},
	}}, nil
}

func TestProductSearchUsesSessionPrincipalAndCanonicalCatalog(t *testing.T) {
	catalog := &productSearchCatalogFake{}
	handler := &BrowserHandler{
		SearchCatalog: catalog,
		CurrentUser: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal:ada", DevBypass: true}, true
		},
	}
	response := httptest.NewRecorder()
	handler.ProductSearch(response, httptest.NewRequest(stdhttp.MethodGet, "/search?q=sales", nil))

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if catalog.request.PrincipalID != "principal:ada" || !catalog.request.DevAuthBypass || catalog.request.Query != "sales" || catalog.request.Limit != 24 {
		t.Fatalf("search request=%#v", catalog.request)
	}
	if len(catalog.request.Kinds) != len(productSearchKinds) {
		t.Fatalf("search kinds=%v", catalog.request.Kinds)
	}
	for index, kind := range productSearchKinds {
		if catalog.request.Kinds[index] != kind {
			t.Fatalf("search kinds=%v", catalog.request.Kinds)
		}
	}
	for _, want := range []string{
		`"href":"/dashboards/dashboard:sales"`,
		`"href":"/models/model:orders/details"`,
		`"href":"/sources/source:orders/details"`,
		`"href":"/connections/connection:warehouse/details"`,
		`"href":"/semantic-models/semantic-model:sales/details"`,
		`"href":"/pipelines/pipeline:refresh/details"`,
		`"displayName":"Sales dashboard"`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("body=%s missing %s", response.Body.String(), want)
		}
	}
	if strings.Contains(response.Body.String(), "project:demo") {
		t.Fatalf("body=%s contains a non-asset result", response.Body.String())
	}
}
