package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type contextCatalog struct {
	items map[string]agenttools.CatalogItem
}

func (c contextCatalog) Search(context.Context, agenttools.Scope, agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	return agenttools.CatalogPage{}, nil
}

func (c contextCatalog) List(context.Context, agenttools.Scope, agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	return agenttools.CatalogPage{}, nil
}

func (c contextCatalog) Get(_ context.Context, _ agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	item, ok := c.items[request.Ref.ID]
	if !ok {
		return agenttools.CatalogGetResult{}, &agenttools.CatalogError{Code: "catalog_not_found", Message: "not found"}
	}
	return agenttools.CatalogGetResult{Item: item}, nil
}

func TestResolveDashboardTurnReferencesUsesCompiledMetadata(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{
		{ID: "orders-chart", Kind: "visual", Visual: "orders_chart"},
		{ID: "orders-table", Kind: "visual", Visual: "orders", Title: "Recent orders"},
	}}
	resolved := ResolveDashboardTurnReferences([]agent.TurnReference{
		{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.orders_chart"}, Name: "Ignore browser title", VisualType: "script", Href: "javascript:alert(1)", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Forged"}},
		{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.orders"}, Name: "Ignore browser table title", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Forged"}},
		{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.secret"}, Name: "Not on page", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Forged"}},
	}, DashboardTurnReferenceContext{
		Resource:    agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"},
		DashboardID: "executive-sales", DashboardTitle: "Executive Sales", Page: page,
	}, map[string]visualizationdefinition.Definition{
		"orders_chart": {ID: "orders_chart", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders by status"}, Mark: visualizationir.VisualizationCartesianMarkBar}}},
		"secret":       {ID: "secret", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Secret"}, Mark: visualizationir.VisualizationCartesianMarkLine}}},
		"orders":       {ID: "orders", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "table", Title: "Orders"}, Kind: "table"}}},
	})
	want := []agent.TurnReference{
		{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.orders_chart"}, ComponentID: "orders-chart", VisualID: "orders_chart", Name: "Orders by status", VisualType: "bar", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"}, Hierarchy: []string{"Demo", "Executive Sales", "Overview"}, Href: "/dashboards/executive-sales/pages/overview", Locations: []agent.TurnReferenceLocation{{DashboardID: "executive-sales", DashboardName: "Executive Sales", PageID: "overview", PageName: "Overview", Href: "/dashboards/executive-sales/pages/overview"}}, Context: []string{"current_page", "current_dashboard"}},
		{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.orders"}, ComponentID: "orders-table", VisualID: "orders", Name: "Recent orders", VisualType: "table", Resource: agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"}, Hierarchy: []string{"Demo", "Executive Sales", "Overview"}, Href: "/dashboards/executive-sales/pages/overview", Locations: []agent.TurnReferenceLocation{{DashboardID: "executive-sales", DashboardName: "Executive Sales", PageID: "overview", PageName: "Overview", Href: "/dashboards/executive-sales/pages/overview"}}, Context: []string{"current_page", "current_dashboard"}},
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved references = %#v, want %#v", resolved, want)
	}
}

func TestResolveChatTurnContextUsesAuthorizedCatalogMetadata(t *testing.T) {
	module := &Module{projectID: projectgraph.ResourceID("project_demo"), catalog: contextCatalog{items: map[string]agenttools.CatalogItem{
		"dashboard_sales": {Ref: agenttools.CatalogRef{ID: "dashboard_sales", Kind: "dashboard"}, Name: "Sales dashboard"},
	}}}
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{PrincipalID: "principal_1"}, agent.TurnContext{
		Surface:    "chat",
		References: []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "dashboard", ID: "dashboard_sales"}, Name: "untrusted"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 1 || resolved.References[0].Name != "Sales dashboard" {
		t.Fatalf("resolved references = %#v", resolved.References)
	}
}

func TestResolveChatTurnContextRejectsUnknownReference(t *testing.T) {
	module := &Module{projectID: projectgraph.ResourceID("project_demo"), catalog: contextCatalog{items: map[string]agenttools.CatalogItem{}}}
	_, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/chats/new", nil), agent.Scope{PrincipalID: "principal_1"}, agent.TurnContext{
		Surface:    "chat",
		References: []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "dashboard", ID: "missing"}}},
	})
	if err == nil {
		t.Fatal("unknown catalog reference was accepted")
	}
}

func TestContextCredentialUsesCanonicalCapability(t *testing.T) {
	scope := agent.Scope{Credential: agent.CredentialScope{Restricted: true, Capabilities: []string{string(access.CapabilityResourceUse)}}}
	if !contextCredentialAllowsCapability(scope, access.CapabilityResourceUse) {
		t.Fatal("canonical capability was rejected")
	}
	if contextCredentialAllowsCapability(scope, access.CapabilityResourceEdit) {
		t.Fatal("ungranted capability was accepted")
	}
}

func TestContextCredentialPreservesDynamicAndDenyAllTokenSemantics(t *testing.T) {
	dynamic := agent.Scope{Credential: agent.CredentialScope{Restricted: true}}
	if !contextCredentialAllowsCapability(dynamic, access.CapabilityResourceRead) {
		t.Fatal("dynamic token scope should defer to the active authorization snapshot")
	}
	denyAll := agent.Scope{Credential: agent.CredentialScope{Restricted: true, Capabilities: []string{}}}
	if contextCredentialAllowsCapability(denyAll, access.CapabilityResourceRead) {
		t.Fatal("explicit empty token scope should deny every capability")
	}
}

func TestResolveContextResourceUsesServerBoundProject(t *testing.T) {
	called := false
	module := &Module{
		projectID: projectgraph.ResourceID("active_project"),
		resolveResource: func(_ context.Context, scope agenttools.Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			called = true
			if scope.ProjectID != "active_project" {
				t.Fatalf("resolver project = %q, want active_project", scope.ProjectID)
			}
			return id, nil
		},
	}
	if _, err := module.resolveContextResource(context.Background(), agent.Scope{ProjectID: "client_project", PrincipalID: "principal"}, "semantic_sales", projectgraph.KindSemanticModel, access.CapabilityResourceUse); err != nil {
		t.Fatalf("resolve context resource: %v", err)
	}
	if !called {
		t.Fatal("resolver was not called")
	}
}
