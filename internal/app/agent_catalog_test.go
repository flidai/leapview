package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/flidai/leapview/internal/access"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	"github.com/flidai/leapview/internal/platform"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type sharedCatalogMetrics struct{ fakeMetrics }

func (m sharedCatalogMetrics) Resolver() dashboardresolver.Resolver {
	return fixtureResolver{provider: m, workspaceID: "test-workspace"}
}

func (sharedCatalogMetrics) Pages(dashboardID string) []dashboard.Page {
	pages := fakeMetrics{}.Pages(dashboardID)
	if len(pages) == 2 {
		pages[1].Visuals = append(pages[1].Visuals, dashboard.PageVisual{
			ID: "shared-orders-chart", Kind: "donut_chart", Visual: "orders",
		})
	}
	return pages
}

func (m sharedCatalogMetrics) dashboardDefinition(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	report, model, ok := m.fakeMetrics.dashboardDefinition(dashboardID)
	if ok {
		report.Pages = m.Pages(dashboardID)
		orders := model.Tables["orders"]
		orders.Dimensions["customer_id"] = semanticmodel.MetricDimension{Expr: "customer_id", Type: "string"}
		model.Tables["orders"] = orders
		model.Tables["customers"] = semanticmodel.Table{
			Source: "customers", PrimaryKey: "customer_id", Grain: "customer_id",
			Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Expr: "customer_id", Type: "string"},
				"status":      {Expr: "status", Type: "string"},
			},
		}
		model.Relationships = []semanticmodel.Relationship{{
			ID: "orders_customer", Description: "Each order belongs to one customer",
			From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
		}}
		model.Dimensions = map[string]semanticmodel.SemanticDimension{
			"order_status": {
				Label: "Order status", Description: "Status shared across facts", Type: "string",
				Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}},
			},
		}
		model.Metrics = map[string]semanticmodel.Metric{
			"orders_per_order": {
				Label: "Orders per order", Description: "Fixture derived metric",
				Expression: "safe_divide(${order_count}, ${order_count})", Format: "decimal",
			},
		}
	}
	return report, model, ok
}

func (m sharedCatalogMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	_, model, ok := m.dashboardDefinition("executive-sales")
	if !ok || model.Name != modelID {
		return nil, false
	}
	return model, true
}

func TestAgentCatalogBrowsesEverySupportedRelationship(t *testing.T) {
	server := catalogTestServer(t)
	service := catalogServiceForTest(server.appTestHarness)
	scope := agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true}
	ctx := context.Background()

	root := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{Limit: 50})
	assertCatalogRefs(t, root.Items, "workspace:test", "workspace:test-workspace")

	workspacePage := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{
		Parent: catalogRefPointer("test-workspace", agenttools.CatalogTypeWorkspace, "test-workspace"), Limit: 50,
	})
	assertCatalogRefs(t, workspacePage.Items, "dashboard:executive-sales", "semantic_model:test")

	dashboardPage := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{
		Parent: catalogRefPointer("test-workspace", agenttools.CatalogTypeDashboard, "executive-sales"), Limit: 50,
	})
	assertCatalogRefs(t, dashboardPage.Items, "page:executive-sales.operations", "page:executive-sales.overview")

	pageChildren := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{
		Parent: catalogRefPointer("test-workspace", agenttools.CatalogTypePage, "executive-sales.overview"), Limit: 50,
	})
	assertCatalogRefs(t, pageChildren.Items,
		"filter:executive-sales.state",
		"visual:executive-sales.order_rows",
		"visual:executive-sales.orders",
	)

	modelChildren := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{
		Parent: catalogRefPointer("test-workspace", agenttools.CatalogTypeSemanticModel, "test"), Limit: 50,
	})
	assertCatalogRefs(t, modelChildren.Items,
		"field:test.order_status",
		"measure:test.order_count",
		"measure:test.orders_per_order",
		"semantic_table:test.customers",
		"semantic_table:test.orders",
	)

	tableChildren := catalogListForTest(t, service, ctx, scope, agenttools.CatalogListRequest{
		Parent: catalogRefPointer("test-workspace", agenttools.CatalogTypeSemanticTable, "test.orders"), Limit: 50,
	})
	assertCatalogRefs(t, tableChildren.Items,
		"field:test.orders.customer_id",
		"field:test.orders.order_id",
		"field:test.orders.revenue",
		"field:test.orders.status",
	)
}

func TestAgentCatalogGetRequiresSharedLocationAndReturnsTypedDetails(t *testing.T) {
	server := catalogTestServer(t)
	service := catalogServiceForTest(server.appTestHarness)
	scope := agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true}
	ctx := context.Background()
	visualRef := agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeVisual, ID: "executive-sales.orders"}

	_, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{Ref: visualRef})
	var catalogErr *agenttools.CatalogError
	if !errors.As(err, &catalogErr) || catalogErr.Code != "catalog_location_required" {
		t.Fatalf("shared visual error = %v, want catalog_location_required", err)
	}
	result, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{
		Ref: visualRef,
		Location: &agenttools.CatalogLocationSelection{
			DashboardID: "executive-sales",
			PageID:      "overview",
		},
	})
	if err != nil {
		t.Fatalf("get shared visual: %v", err)
	}
	if result.Details["type"] != "visual" || result.Details["visualType"] != "donut" || result.Details["query"] == nil {
		t.Fatalf("visual details = %#v", result.Details)
	}
	if len(result.Item.Locations) != 2 ||
		result.Item.Locations[0].DashboardName == "" ||
		result.Item.Locations[0].PageName == "" ||
		result.Item.Locations[0].Href == "" {
		t.Fatalf("visual locations = %#v, want named navigable locations", result.Item.Locations)
	}

	for _, ref := range []agenttools.CatalogRef{
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeWorkspace, ID: "test-workspace"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeDashboard, ID: "executive-sales"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypePage, ID: "executive-sales.overview"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeSemanticModel, ID: "test"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeSemanticTable, ID: "test.orders"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeField, ID: "test.order_status"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeField, ID: "test.orders.status"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeMeasure, ID: "test.order_count"},
		{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeMeasure, ID: "test.orders_per_order"},
	} {
		got, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{Ref: ref})
		if err != nil {
			t.Fatalf("get %s %s: %v", ref.Type, ref.ID, err)
		}
		if got.Details["type"] != string(ref.Type) {
			t.Fatalf("get %s details = %#v", ref.Type, got.Details)
		}
	}
}

func TestAgentCatalogGetReturnsSemanticDefinitions(t *testing.T) {
	service := catalogServiceForTest(catalogTestServer(t).appTestHarness)
	scope := agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true}
	ctx := context.Background()

	model, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeSemanticModel, ID: "test"},
	})
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	for key, want := range map[string]any{
		"semanticTableCount":      2,
		"conformedDimensionCount": 1,
		"atomicMeasureCount":      1,
		"metricCount":             1,
		"factCount":               1,
		"relationshipCount":       1,
	} {
		if got := model.Details[key]; got != want {
			t.Fatalf("model %s = %#v, want %#v", key, got, want)
		}
	}
	relationships, ok := model.Details["relationships"].([]map[string]any)
	if !ok || len(relationships) != 1 ||
		relationships[0]["fromFieldRef"] != catalogRefValue("test-workspace", agenttools.CatalogTypeField, "test.orders.customer_id") ||
		relationships[0]["toFieldRef"] != catalogRefValue("test-workspace", agenttools.CatalogTypeField, "test.customers.customer_id") {
		t.Fatalf("model relationships = %#v", model.Details["relationships"])
	}

	table, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeSemanticTable, ID: "test.orders"},
	})
	if err != nil {
		t.Fatalf("get table: %v", err)
	}
	if roles, ok := table.Details["roles"].([]string); !ok || !slices.Equal(roles, []string{"fact"}) {
		t.Fatalf("table roles = %#v, want fact", table.Details["roles"])
	}

	field, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeField, ID: "test.order_status"},
	})
	if err != nil {
		t.Fatalf("get conformed field: %v", err)
	}
	bindings, ok := field.Details["bindings"].([]map[string]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("field bindings = %#v", field.Details["bindings"])
	}
	if got := bindings[0]["semanticTableRef"]; got != catalogRefValue("test-workspace", agenttools.CatalogTypeSemanticTable, "test.orders") {
		t.Fatalf("semanticTableRef = %#v", got)
	}
	if got := bindings[0]["fieldRef"]; got != catalogRefValue("test-workspace", agenttools.CatalogTypeField, "test.orders.status") {
		t.Fatalf("fieldRef = %#v", got)
	}
	if path, ok := bindings[0]["relationshipPath"].([]string); !ok || len(path) != 0 {
		t.Fatalf("relationshipPath = %#v", bindings[0]["relationshipPath"])
	}

	metric, err := service.Get(ctx, scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeMeasure, ID: "test.orders_per_order"},
	})
	if err != nil {
		t.Fatalf("get metric: %v", err)
	}
	if metric.Details["expression"] != "safe_divide(${order_count}, ${order_count})" {
		t.Fatalf("metric expression = %#v", metric.Details["expression"])
	}
	dependencies, ok := metric.Details["dependencyRefs"].([]agenttools.CatalogRef)
	if !ok || !slices.Equal(dependencies, []agenttools.CatalogRef{
		catalogRefValue("test-workspace", agenttools.CatalogTypeMeasure, "test.order_count"),
	}) {
		t.Fatalf("metric dependencies = %#v", metric.Details["dependencyRefs"])
	}
}

func TestAgentCatalogSearchIsGlobalNormalizedAndBounded(t *testing.T) {
	server := catalogTestServer(t)
	service := catalogServiceForTest(server.appTestHarness)
	page, err := service.Search(context.Background(), agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true}, agenttools.CatalogSearchRequest{
		Query: "orders", Limit: 10,
	})
	if err != nil {
		t.Fatalf("catalog search: %v", err)
	}
	if len(page.Items) == 0 || len(page.Items) > 10 {
		t.Fatalf("catalog search items = %#v", page.Items)
	}
	for _, item := range page.Items {
		if item.Ref.WorkspaceID != "test-workspace" || item.Workspace.Ref.Type != agenttools.CatalogTypeWorkspace || len(item.Capabilities) == 0 {
			t.Fatalf("unnormalized catalog item = %#v", item)
		}
	}
}

func TestAgentCatalogCredentialRestrictionDoesNotLeakOtherWorkspaces(t *testing.T) {
	server := catalogTestServer(t)
	principal := testPrincipal(t, context.Background(), server.store, "catalog-owner@example.com", "Catalog Owner", access.RoleOwner)
	scope := agenttools.Scope{
		PrincipalID: principal.ID,
		Credential: agenttools.CredentialScope{
			WorkspaceID: "test-workspace",
			Restricted:  true,
			Privileges:  []string{string(access.PrivilegeViewItem)},
		},
	}
	service := catalogServiceForTest(server.appTestHarness)
	root, err := service.List(context.Background(), scope, agenttools.CatalogListRequest{Limit: 50})
	if err != nil {
		t.Fatalf("restricted root list: %v", err)
	}
	assertCatalogRefs(t, root.Items, "workspace:test-workspace")

	_, inaccessibleErr := service.Get(context.Background(), scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test", Type: agenttools.CatalogTypeWorkspace, ID: "test"},
	})
	_, missingErr := service.Get(context.Background(), scope, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeDashboard, ID: "missing"},
	})
	for name, err := range map[string]error{"inaccessible": inaccessibleErr, "missing": missingErr} {
		var catalogErr *agenttools.CatalogError
		if !errors.As(err, &catalogErr) || catalogErr.Code != "catalog_not_found" {
			t.Fatalf("%s ref error = %v, want catalog_not_found", name, err)
		}
	}
}

func TestAgentCatalogSemanticModelUsageFiltersUnauthorizedDashboards(t *testing.T) {
	server := catalogTestServer(t)
	ctx := context.Background()
	repository := testAccessRepository(server.store)
	principal, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "catalog-model-only", Email: "catalog-model-only@example.com", DisplayName: "Catalog Model Only",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	modelObject := access.ItemObjectWithParent(
		access.SecurableSemanticModel,
		"test-workspace",
		"test",
		access.WorkspaceObject("test-workspace"),
	)
	if _, err := repository.CreateGrant(ctx, access.GrantInput{
		Object: modelObject, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem,
	}); err != nil {
		t.Fatalf("grant semantic model view: %v", err)
	}

	result, err := catalogServiceForTest(server.appTestHarness).Get(ctx, agenttools.Scope{PrincipalID: principal.ID}, agenttools.CatalogGetRequest{
		Ref: agenttools.CatalogRef{WorkspaceID: "test-workspace", Type: agenttools.CatalogTypeSemanticModel, ID: "test"},
	})
	if err != nil {
		t.Fatalf("get semantic model: %v", err)
	}
	if got := result.Details["dashboardCount"]; got != 0 {
		t.Fatalf("dashboardCount = %#v, want 0 for unauthorized dashboard", got)
	}
	if got, ok := result.Details["dashboardUsage"].([]agenttools.CatalogRef); !ok || len(got) != 0 {
		t.Fatalf("dashboardUsage = %#v, want no unauthorized refs", result.Details["dashboardUsage"])
	}
}

func TestAgentCatalogWorkspaceLookupPropagatesRepositoryFailures(t *testing.T) {
	sentinel := errors.New("workspace repository unavailable")
	service := agentmodule.BuildCatalog(agentmodule.CatalogConfig{
		Workspaces:  activeMetadataWorkspaceRepo{err: sentinel},
		Environment: "dev",
	})
	_, err := service.Get(
		context.Background(),
		agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true},
		agenttools.CatalogGetRequest{
			Ref: agenttools.CatalogRef{WorkspaceID: "test", Type: agenttools.CatalogTypeWorkspace, ID: "test"},
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("workspace lookup error = %v, want %v", err, sentinel)
	}
}

func TestAgentCatalogProviderOutputMatchesClosedSchema(t *testing.T) {
	server := catalogTestServer(t)
	definitions := server.routes.agentModule.CatalogToolProvider().Definitions(agenttools.Scope{PrincipalID: "dev", DevAuthBypass: true})
	catalog, err := agentcore.NewToolCatalog(definitions)
	if err != nil {
		t.Fatalf("compile catalog tools: %v", err)
	}
	result, err := catalog.Execute(context.Background(), agentcore.ToolCall{
		ID: "catalog-list", Name: agenttools.CatalogListToolName, Arguments: json.RawMessage(`{}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("execute catalog_list: result=%#v err=%v", result, err)
	}
	result, err = catalog.Execute(context.Background(), agentcore.ToolCall{
		ID: "catalog-get", Name: agenttools.CatalogGetToolName,
		Arguments: json.RawMessage(`{
			"ref":{"workspaceId":"test-workspace","type":"visual","id":"executive-sales.orders"},
			"location":{"dashboardId":"executive-sales","pageId":"overview"}
		}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("execute catalog_get: result=%#v err=%v", result, err)
	}
	for _, args := range []string{
		`{"ref":{"workspaceId":"test-workspace","type":"workspace","id":"test-workspace"}}`,
		`{"ref":{"workspaceId":"test-workspace","type":"dashboard","id":"executive-sales"}}`,
		`{"ref":{"workspaceId":"test-workspace","type":"page","id":"executive-sales.overview"}}`,
		`{
			"ref":{"workspaceId":"test-workspace","type":"filter","id":"executive-sales.state"},
			"location":{"dashboardId":"executive-sales","pageId":"overview"}
		}`,
	} {
		result, err = catalog.Execute(context.Background(), agentcore.ToolCall{
			ID: "catalog-get-domain", Name: agenttools.CatalogGetToolName, Arguments: json.RawMessage(args),
		})
		if err != nil || result.IsError {
			t.Fatalf("execute catalog_get %s: result=%#v err=%v", args, result, err)
		}
	}
	for _, ref := range []string{
		`{"workspaceId":"test-workspace","type":"semantic_model","id":"test"}`,
		`{"workspaceId":"test-workspace","type":"semantic_table","id":"test.orders"}`,
		`{"workspaceId":"test-workspace","type":"field","id":"test.order_status"}`,
		`{"workspaceId":"test-workspace","type":"field","id":"test.orders.status"}`,
		`{"workspaceId":"test-workspace","type":"measure","id":"test.order_count"}`,
		`{"workspaceId":"test-workspace","type":"measure","id":"test.orders_per_order"}`,
	} {
		result, err = catalog.Execute(context.Background(), agentcore.ToolCall{
			ID: "catalog-get-semantic", Name: agenttools.CatalogGetToolName,
			Arguments: json.RawMessage(`{"ref":` + ref + `}`),
		})
		if err != nil || result.IsError {
			t.Fatalf("execute catalog_get %s: result=%#v err=%v", ref, result, err)
		}
	}
}

type catalogTestHarness struct {
	*appTestHarness
	store *platform.Store
}

func catalogServiceForTest(server *appTestHarness) agenttools.Catalog {
	service := server.routes.agentModule.CatalogToolProvider().Catalog
	_, ok := service.(agentmodule.CatalogService)
	if !ok {
		panic("agent catalog service is not configured")
	}
	return service
}

func catalogTestServer(t *testing.T) *catalogTestHarness {
	t.Helper()
	ctx := context.Background()
	store := testStore(t)
	workspaceID := workspace.WorkspaceID("test-workspace")
	repository := workspacesqlite.NewRepository(store.SQLDB())
	if err := repository.Ensure(ctx, workspace.EnsureInput{ID: workspaceID, Title: "Test Workspace", Description: "Fixture workspace"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	servingRepository := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := servingRepository.Create(ctx, servingstate.CreateInput{
		WorkspaceID: servingstate.WorkspaceID(workspaceID),
		Environment: servingstate.DefaultEnvironment,
		CreatedBy:   "tester",
	})
	if err != nil {
		t.Fatalf("create serving state: %v", err)
	}
	metrics := sharedCatalogMetrics{}
	report, model, ok := metrics.dashboardDefinition("executive-sales")
	if !ok {
		t.Fatal("fixture report missing")
	}
	definition := &projectmanifest.Workspace{
		Catalog: projectmanifest.Catalog{
			Workspace:      projectmanifest.CatalogWorkspace{ID: string(workspaceID), Title: "Test Workspace", Description: "Fixture workspace"},
			SemanticModels: []projectmanifest.CatalogModel{{ID: "test", Title: "Test Model", Description: "Fixture model"}},
			Dashboards:     []projectmanifest.CatalogDashboard{{ID: "executive-sales", Title: report.Title, Description: "Fixture report"}},
		},
		Models:               map[string]*semanticmodel.Model{"test": model},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{"executive-sales": report},
	}
	graph, err := projectcompiler.ExtractLineage(workspaceID, workspace.ServingStateID(created.ID), definition)
	if err != nil {
		t.Fatalf("extract catalog lineage: %v", err)
	}
	for index := range graph.Assets {
		graph.Assets[index].SourceFile = "dashboards/leapview.yaml"
	}
	artifact := zeroArtifact(created.ID, string(workspaceID))
	artifact.Environment = servingstate.DefaultEnvironment
	validation := completeTestValidation(string(workspaceID), servingstate.Validation{
		Digest: "catalog-test", ManifestJSON: "{}", ProjectID: "catalog-test", Graph: testSnapshotGraph(t, graph),
	})
	if _, err := servingRepository.SaveValidated(ctx, created.ID, validation, artifact); err != nil {
		t.Fatalf("save catalog serving state: %v", err)
	}
	if _, err := servingRepository.Activate(ctx, servingstate.WorkspaceID(workspaceID), servingstate.DefaultEnvironment, created.ID); err != nil {
		t.Fatalf("activate catalog serving state: %v", err)
	}
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{
		WorkspaceRepo: repository, WorkspaceID: string(workspaceID), DefaultEnvironment: string(servingstate.DefaultEnvironment),
	}))
	return &catalogTestHarness{appTestHarness: server, store: store}
}

func catalogListForTest(t *testing.T, service agenttools.Catalog, ctx context.Context, scope agenttools.Scope, request agenttools.CatalogListRequest) agenttools.CatalogPage {
	t.Helper()
	page, err := service.List(ctx, scope, request)
	if err != nil {
		t.Fatalf("catalog list %#v: %v", request.Parent, err)
	}
	return page
}

func catalogRefValue(workspaceID string, typ agenttools.CatalogType, id string) agenttools.CatalogRef {
	return agenttools.CatalogRef{WorkspaceID: workspaceID, Type: typ, ID: id}
}

func catalogRefPointer(workspaceID string, typ agenttools.CatalogType, id string) *agenttools.CatalogRef {
	return &agenttools.CatalogRef{WorkspaceID: workspaceID, Type: typ, ID: id}
}

func assertCatalogRefs(t *testing.T, items []agenttools.CatalogItem, want ...string) {
	t.Helper()
	got := make([]string, 0, len(items))
	for _, item := range items {
		got = append(got, string(item.Ref.Type)+":"+item.Ref.ID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("catalog refs = %#v, want %#v", got, want)
	}
}
