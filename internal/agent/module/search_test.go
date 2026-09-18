package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
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

type referenceAuthorizationLease struct {
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l referenceAuthorizationLease) Release() {}
func (l referenceAuthorizationLease) Identity() projectgraph.ServingIdentity {
	return l.snapshot.Identity()
}
func (l referenceAuthorizationLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type referenceAuthorizationLeases struct{ lease projectcatalog.Lease }

func (p referenceAuthorizationLeases) Acquire(context.Context) (projectcatalog.Lease, error) {
	return p.lease, nil
}

type referenceAuthorizationSubjects map[string]access.SubjectRef

func (s referenceAuthorizationSubjects) AuthorizationSubjects(_ context.Context, principalID string) ([]access.SubjectRef, error) {
	if subject, ok := s[principalID]; ok {
		return []access.SubjectRef{subject}, nil
	}
	return nil, nil
}

func TestSearchReferencesAutocompleteFiltersByAuthorization(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "model_allowed", Kind: projectgraph.KindModel, Name: "orders_allowed"},
		{ID: "model_denied", Kind: projectgraph.KindModel, Name: "orders_denied"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	subjects := make(referenceAuthorizationSubjects)
	grants := make([]accesssnapshot.Grant, 0, 2)
	for _, fixture := range []struct{ principal, resource string }{
		{principal: "alice", resource: "model_allowed"},
		{principal: "bob", resource: "model_denied"},
	} {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, fixture.principal)
		if err != nil {
			t.Fatal(err)
		}
		subjects[fixture.principal] = subject
		resource, err := access.NewResourceRef(projectgraph.ResourceID(fixture.resource), projectgraph.KindModel)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, subject, resource, access.CapabilityResourceRead)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, accesssnapshot.Grant{ID: "grant_" + fixture.principal, Canonical: canonical})
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectCatalog, err := projectcatalog.NewService(referenceAuthorizationLeases{lease: referenceAuthorizationLease{snapshot: snapshot}}, subjects)
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{
		catalog: BuildCatalog(CatalogConfig{ProjectCatalog: projectCatalog}), projectID: identity.ProjectID,
		currentPrincipal: func(r *http.Request) (Principal, bool) {
			return Principal{ID: r.Header.Get("X-Test-Principal")}, true
		},
	}
	for _, fixture := range []struct{ principal, expectedID string }{
		{principal: "alice", expectedID: "model_allowed"},
		{principal: "bob", expectedID: "model_denied"},
	} {
		for _, query := range []string{"orders", ""} {
			t.Run(fixture.principal+"/query="+query, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "/chats/references/search", nil)
				request.Header.Set("X-Test-Principal", fixture.principal)
				results, err := module.SearchReferences(request, agent.TurnContext{}, query, 8)
				if err != nil {
					t.Fatal(err)
				}
				if len(results) != 1 || results[0].Reference.ID != fixture.expectedID {
					t.Fatalf("autocomplete for %q with query %q = %#v, want only %q", fixture.principal, query, results, fixture.expectedID)
				}
			})
		}
	}
}
