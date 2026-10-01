package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type productSearchCatalogFake struct {
	request projectcatalog.SearchRequest
}

type pagedTypedProductSearchCatalog struct {
	requests []projectcatalog.SearchRequest
}

func (f *pagedTypedProductSearchCatalog) Search(_ context.Context, request projectcatalog.SearchRequest) (projectcatalog.Page, error) {
	f.requests = append(f.requests, request)
	if request.Cursor == "" {
		return projectcatalog.Page{Items: []projectcatalog.Result{{
			Ref: projectcatalog.Ref{ID: "dashboard:denied", Kind: projectgraph.KindDashboard}, Name: "denied",
		}}, NextCursor: "page-2"}, nil
	}
	return projectcatalog.Page{Items: []projectcatalog.Result{{
		Ref: projectcatalog.Ref{ID: "dashboard:allowed", Kind: projectgraph.KindDashboard}, Name: "allowed",
	}}}, nil
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
		{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID("project:demo"), Kind: projectgraph.KindProjectNamespace}, Name: "demo"},
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

func TestProductSearchPagesUntilTypedCredentialFindsAuthorizedResults(t *testing.T) {
	projectID := projectgraph.ResourceID("project:test")
	resource, err := access.NewResourceRef("dashboard:allowed", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &pagedTypedProductSearchCatalog{}
	handler := &BrowserHandler{
		SearchCatalog:    catalog,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:ada"}, true },
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}}}, true
		},
	}
	response := httptest.NewRecorder()
	handler.ProductSearch(response, httptest.NewRequest(stdhttp.MethodGet, "/search?q=dashboard", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(catalog.requests) != 2 || catalog.requests[1].Cursor != "page-2" {
		t.Fatalf("search requests = %#v, want second authorized page", catalog.requests)
	}
	var body struct {
		Items []productSearchResult `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Reference.ID != "dashboard:allowed" {
		t.Fatalf("items = %#v, want only authorized later result", body.Items)
	}
}
