package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type contextCatalog struct {
	items     map[string]agenttools.CatalogItem
	authorize func(agenttools.Scope, agenttools.CatalogGetRequest) error
}

func (c contextCatalog) Search(context.Context, agenttools.Scope, agenttools.CatalogSearchRequest) (agenttools.CatalogPage, error) {
	return agenttools.CatalogPage{}, nil
}

func (c contextCatalog) List(context.Context, agenttools.Scope, agenttools.CatalogListRequest) (agenttools.CatalogPage, error) {
	return agenttools.CatalogPage{}, nil
}

func (c contextCatalog) Get(_ context.Context, scope agenttools.Scope, request agenttools.CatalogGetRequest) (agenttools.CatalogGetResult, error) {
	if c.authorize != nil {
		if err := c.authorize(scope, request); err != nil {
			return agenttools.CatalogGetResult{}, err
		}
	}
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

func TestResolveDashboardTurnReferencesBindsMissingResourceAndRejectsMismatch(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{
		{ID: "orders-chart", Kind: "visual", Visual: "orders_chart"},
	}}
	for _, tc := range []struct {
		name, resourceID string
		count            int
	}{
		{"missing resource", "", 1},
		{"wrong resource", "other_project", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := ResolveDashboardTurnReferences([]agent.TurnReference{
				{Reference: agent.TurnReferenceKey{Kind: "visual", ID: "executive-sales.orders_chart"}, Resource: agent.TurnReferenceResource{ID: tc.resourceID}},
			}, DashboardTurnReferenceContext{
				Resource:    agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"},
				DashboardID: "executive-sales", DashboardTitle: "Executive Sales", Page: page,
			}, map[string]visualizationdefinition.Definition{
				"orders_chart": {ID: "orders_chart", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders by status"}, Mark: visualizationir.VisualizationCartesianMarkBar}}},
			})
			if len(resolved) != tc.count {
				t.Fatalf("resolved references = %#v, want %d", resolved, tc.count)
			}
			if tc.count > 0 && resolved[0].Resource != (agent.TurnReferenceResource{ID: "project_demo", Name: "Demo"}) {
				t.Fatalf("resolved resource = %#v, want server-bound project", resolved[0].Resource)
			}
		})
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

func TestTurnContextsPassTypedTokenPairsToCanonicalResourceResolver(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	dashboardID := projectgraph.ResourceID("dashboard_sales")
	modelID := projectgraph.ResourceID("semantic_sales")
	dashboard, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	dashboardRead, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	model, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	semanticQuery, err := access.NewExactPermissionPair(access.ActionSemanticQuery, projectID, model)
	if err != nil {
		t.Fatal(err)
	}
	semanticPermissions, err := access.RequiredPermissionPairs(semanticQuery)
	if err != nil {
		t.Fatal(err)
	}

	datasetID := "orders"
	dataSpec := &exploration.ExplorationSpec{SchemaVersion: 1, ModelID: modelID.String(), DatasetID: &datasetID, Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	called := map[projectgraph.Kind]bool{}
	module := &Module{
		projectID: projectID,
		resolveResource: func(_ context.Context, scope Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
			if !CredentialAllowsResource(scope, id, kind, capability) {
				return "", access.ErrForbidden
			}
			called[kind] = true
			return id, nil
		},
	}
	for _, test := range []struct {
		name        string
		permissions []access.PermissionPair
		candidate   agent.TurnContext
		kind        projectgraph.Kind
	}{
		{name: "dashboard", permissions: []access.PermissionPair{dashboardRead}, candidate: agent.TurnContext{Surface: "dashboard", DashboardID: dashboardID.String(), PageID: "overview"}, kind: projectgraph.KindDashboard},
		{name: "data", permissions: semanticPermissions, candidate: agent.TurnContext{Surface: "data", ModelID: modelID.String(), DatasetID: datasetID, Exploration: dataSpec}, kind: projectgraph.KindSemanticModel},
	} {
		t.Run(test.name, func(t *testing.T) {
			called[test.kind] = false
			scope := agent.Scope{PrincipalID: "principal-1", Credential: agent.CredentialScope{Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: test.permissions}}
			_, _ = module.ResolveTurnContext(httptest.NewRequest(http.MethodGet, "/agent", nil), scope, test.candidate)
			if !called[test.kind] {
				t.Fatalf("typed %s permission was rejected before the canonical resolver", test.name)
			}
		})
	}
}

func TestResolveContextResourceUsesServerBoundProject(t *testing.T) {
	called := false
	module := &Module{
		projectID: projectgraph.ResourceID("active_project"),
		resolveResource: func(_ context.Context, scope Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
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

type dataContextMetrics struct {
	queryruntime.Metrics
	model *semanticmodel.Model
}

func (m dataContextMetrics) SemanticModel(id string) (*semanticmodel.Model, bool) {
	return m.model, id == "semantic_sales"
}

func dataReferenceContextFixture(t *testing.T) (*Module, agent.TurnContext) {
	t.Helper()
	dataset := "orders"
	model := &semanticmodel.Model{
		Name:     "sales",
		Tables:   map[string]semanticmodel.Table{"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders", DisplayName: "Trusted orders", Description: "Trusted dataset description"}},
	}
	module := &Module{
		projectID: "project_demo",
		resolveResource: func(_ context.Context, scope Scope, id projectgraph.ResourceID, _ projectgraph.Kind, _ access.Capability) (projectgraph.ResourceID, error) {
			if scope.ProjectID != "project_demo" {
				t.Fatalf("model resolver project=%q", scope.ProjectID)
			}
			return id, nil
		},
		dashboardMetrics: func(project string) (queryruntime.Metrics, bool) {
			return dataContextMetrics{model: model}, project == "project_demo"
		},
		catalog: contextCatalog{items: map[string]agenttools.CatalogItem{
			"model_notes": {Ref: agenttools.CatalogRef{ID: "model_notes", Kind: "model"}, Name: "Trusted order notes", Description: "Authorized catalog description"},
		}, authorize: func(scope agenttools.Scope, _ agenttools.CatalogGetRequest) error {
			if scope.ProjectID != "project_demo" || scope.PrincipalID != "owner" {
				return access.ErrForbidden
			}
			return nil
		}},
	}
	return module, agent.TurnContext{
		Surface: "data", ModelID: "semantic_sales", DatasetID: dataset,
		Exploration: &exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic_sales", DatasetID: &dataset, Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 12},
		References:  []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "model", ID: "model_notes"}, Name: "Forged browser name", Href: "javascript:forged", Context: []string{"Forged instructions"}, Resource: agent.TurnReferenceResource{ID: "project_demo"}}},
	}
}

func TestResolveDataTurnContextPreservesAuthorizedCatalogReferences(t *testing.T) {
	module, candidate := dataReferenceContextFixture(t)
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), agent.Scope{ProjectID: "untrusted_browser_project", PrincipalID: "owner"}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	expected := TurnReferenceFromCatalog(agenttools.CatalogItem{Ref: agenttools.CatalogRef{ID: "model_notes", Kind: "model"}, Name: "Trusted order notes", Description: "Authorized catalog description"}, "project_demo")
	if !reflect.DeepEqual(resolved.References, []agent.TurnReference{expected}) {
		t.Fatalf("resolved data references=%#v, want server-authorized catalog reference", resolved.References)
	}
	if resolved.ModelID != candidate.ModelID || resolved.DatasetID != candidate.DatasetID || !reflect.DeepEqual(resolved.Exploration, candidate.Exploration) {
		t.Fatalf("reference changed governed exploration: %#v", resolved)
	}
	// This final typed context is the value serialized in leapview_context for
	// the provider; browser labels and instructions must never survive resolution.
	payload, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "Trusted order notes") || strings.Contains(string(payload), "Forged") || strings.Contains(string(payload), "javascript:") {
		t.Fatalf("provider context lost trusted metadata or retained browser metadata: %s", payload)
	}
}

func TestResolveDataTurnContextRejectsUnauthorizedCatalogReferences(t *testing.T) {
	for _, scenario := range []string{"unknown", "other principal", "other project", "restricted credential"} {
		t.Run(scenario, func(t *testing.T) {
			module, candidate := dataReferenceContextFixture(t)
			scope := agent.Scope{PrincipalID: "owner"}
			switch scenario {
			case "unknown":
				candidate.References[0].Reference.ID = "missing"
			case "other principal":
				scope.PrincipalID = "other"
			case "other project":
				candidate.References[0].Resource.ID = "project_foreign"
			case "restricted credential":
				scope.Credential = agent.CredentialScope{Restricted: true, PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{}}
			}
			if _, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), scope, candidate); err == nil {
				t.Fatal("unauthorized data reference was accepted")
			}
		})
	}
}

func TestResolveDataTurnContextProjectsOnlyTheCurrentDatasetPin(t *testing.T) {
	module, candidate := dataReferenceContextFixture(t)
	candidate.References = append([]agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "dataset", ID: "semantic_sales/orders"}, Name: "Forged dataset", Description: "Forged description", Href: "javascript:forged", Context: []string{"Forged instructions"}}}, candidate.References...)
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), agent.Scope{PrincipalID: "owner"}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 2 {
		t.Fatalf("current dataset pin or catalog ref dropped: %#v", resolved.References)
	}
	pin := resolved.References[0]
	if pin.Reference != candidate.References[0].Reference || pin.Name != "Trusted orders" || pin.Description != "Trusted dataset description" || pin.ModelID != "semantic_sales" || pin.DatasetID != "orders" || pin.Resource.ID != "project_demo" || pin.Href != "/explore?mode=explore&semanticModel=semantic_sales&dataset=orders" || !reflect.DeepEqual(pin.Context, []string{"active_project_generation"}) {
		t.Fatalf("dataset pin did not use trusted semantic metadata: %#v", pin)
	}
	if resolved.References[1].Name != "Trusted order notes" || !reflect.DeepEqual(resolved.Exploration, candidate.Exploration) {
		t.Fatalf("mixed references changed ordering or governed exploration: %#v", resolved)
	}
	t.Run("canonical whitespace", func(t *testing.T) {
		module, candidate := dataReferenceContextFixture(t)
		candidate.References = []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: " DATASET ", ID: " semantic_sales/orders "}, Resource: agent.TurnReferenceResource{ID: " project_demo "}}}
		resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), agent.Scope{PrincipalID: "owner"}, candidate)
		if err != nil || len(resolved.References) != 1 || resolved.References[0].Reference != (agent.TurnReferenceKey{Kind: "dataset", ID: "semantic_sales/orders"}) {
			t.Fatalf("dataset pin failed canonical normalization: %#v, %v", resolved, err)
		}
	})
	for _, id := range []string{"semantic_sales/other_dataset", "other_model/orders", "random_unknown_pin", ""} {
		t.Run(id, func(t *testing.T) {
			candidate.References[0].Reference.ID = id
			if _, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), agent.Scope{PrincipalID: "owner"}, candidate); err == nil {
				t.Fatal("unmatched dataset pin accepted")
			}
		})
	}
}

func TestResolveDataTurnContextPreservesSemanticModelPickerWireKind(t *testing.T) {
	module, candidate := dataReferenceContextFixture(t)
	catalog := module.catalog.(contextCatalog)
	catalog.items["semantic-model:operations"] = agenttools.CatalogItem{Ref: agenttools.CatalogRef{Kind: "semantic_model", ID: "semantic-model:operations"}, Name: "Operations"}
	module.catalog = catalog
	candidate.References = []agent.TurnReference{{Reference: agent.TurnReferenceKey{Kind: "semantic_model", ID: "semantic-model:operations"}, Name: "Forged browser label"}}
	resolved, err := module.ResolveTurnContext(httptest.NewRequest(http.MethodPost, "/chats/turns", nil), agent.Scope{PrincipalID: "owner"}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.References) != 1 || resolved.References[0].Reference != candidate.References[0].Reference || resolved.References[0].Name != "Operations" || resolved.ModelID != "semantic_sales" {
		t.Fatalf("offered catalog wire kind was dropped or changed governed model: %#v", resolved)
	}
}
