package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type referenceSearchCatalog struct {
	scope         agenttools.Scope
	searchRequest agenttools.CatalogSearchRequest
	listRequest   agenttools.CatalogListRequest
}

func (c *referenceSearchCatalog) Search(_ context.Context, scope agenttools.Scope, request agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	c.scope = scope
	c.searchRequest = request
	return agenttools.CatalogPage{Items: []agenttools.CatalogItem{{Ref: agenttools.CatalogRef{Kind: "model", ID: "model_orders"}, Name: "Orders"}}}, nil
}

func (c *referenceSearchCatalog) List(_ context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	c.scope = scope
	c.listRequest = request
	return agenttools.CatalogPage{Items: []agenttools.CatalogItem{{Ref: agenttools.CatalogRef{Kind: "dashboard", ID: "dashboard_sales"}, Name: "Sales"}}}, nil
}

func (*referenceSearchCatalog) Get(context.Context, agenttools.Scope, agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	return agenttools.CatalogGetResult{}, nil
}

func TestSearchReferencesPropagatesDevelopmentBypass(t *testing.T) {
	catalog := &referenceSearchCatalog{}
	module := &Module{
		catalog: catalog, projectID: projectgraph.ResourceID("project_demo"),
		currentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "dev", DevAuthBypass: true}, true
		},
	}
	results, err := module.SearchReferences(httptest.NewRequest(http.MethodGet, "/chats", nil), agent.TurnContext{}, "orders", 8)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.scope.PrincipalID != "dev" || !catalog.scope.DevAuthBypass || catalog.scope.ProjectID != "project_demo" {
		t.Fatalf("catalog scope = %#v", catalog.scope)
	}
	if catalog.searchRequest.Query != "orders" || len(results) != 1 || results[0].Reference.ID != "model_orders" {
		t.Fatalf("search request=%#v results=%#v", catalog.searchRequest, results)
	}
}

func TestSearchReferencesListsAccessibleContextForBareMention(t *testing.T) {
	catalog := &referenceSearchCatalog{}
	module := &Module{
		catalog: catalog, projectID: projectgraph.ResourceID("project_demo"),
		currentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal"}, true },
	}
	results, err := module.SearchReferences(httptest.NewRequest(http.MethodGet, "/chats", nil), agent.TurnContext{}, "", 8)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.listRequest.Limit != 8 || len(catalog.listRequest.ChildKinds) != len(catalogReferenceKinds) {
		t.Fatalf("list request = %#v", catalog.listRequest)
	}
	if len(results) != 1 || results[0].Reference.ID != "dashboard_sales" {
		t.Fatalf("results = %#v", results)
	}
}
