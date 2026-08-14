package ui

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	workspaceview "github.com/flidai/leapview/internal/workspace"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	g "maragu.dev/gomponents"
)

func TestWorkspaceCatalogSignalUsesWorkspaceItems(t *testing.T) {
	page := workspaceCatalogPageSignal([]workspaceview.WorkspaceView{{
		ID:                   "operations",
		Title:                "Operations Workspace",
		Description:          "Fulfillment and delivery analysis.",
		ActiveServingStateID: "serving-state",
	}})

	items := uisignals.ValueOrZero(page.Workspaces)
	if len(items) != 1 || items[0].ID != "operations" {
		t.Fatalf("workspace catalog items = %#v, want operations workspace", items)
	}
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{`"cards"`, `"servingLabel"`} {
		if strings.Contains(string(payload), stale) {
			t.Fatalf("workspace catalog payload contains stale field %s: %s", stale, payload)
		}
	}
}

func TestListPagesUseDebouncedPostSearchCommands(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	tests := []struct {
		name string
		page g.Node
		path string
	}{
		{name: "catalog", page: CatalogPage(catalog), path: "/catalog/search"},
		{name: "workspaces", page: WorkspacesPage(catalog, []workspaceview.WorkspaceView{workspace}, "Owner"), path: "/workspaces/search"},
		{name: "workspace assets", page: WorkspacePage(catalog, workspace, assets, "", "", "Owner", WorkspaceAccessResponse{}, "", testAccessCommandBindings()), path: "/workspaces/leapview/search"},
		{name: "connections", page: ConnectionsPage(catalog, workspace.ID, assets, edges, "", "Owner"), path: "/connections/search"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out strings.Builder
			if err := test.page.Render(&out); err != nil {
				t.Fatal(err)
			}
			rendered := html.UnescapeString(out.String())
			if !strings.Contains(rendered, `data-on:lv-entity-list-query__debounce.200ms=`) && !strings.Contains(rendered, `data-on:lv-workspace-asset-filter__debounce.200ms=`) {
				t.Fatalf("list page missing debounced search bridge:\n%s", rendered)
			}
			if !strings.Contains(rendered, "@post('"+test.path+"'") {
				t.Fatalf("list page missing POST search command %q:\n%s", test.path, rendered)
			}
		})
	}
}

func TestCatalogPageScopesDashboardAppearanceCommandToEventWorkspace(t *testing.T) {
	_, workspaceCatalog, _, _ := testWorkspaceAssetFixtures()
	var out strings.Builder
	if err := CatalogPage(workspaceCatalog).Render(&out); err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	want := `@post('/workspaces/' + encodeURIComponent($dashboardAppearanceCommand.workspaceId) + '/catalog/appearance'`
	if !strings.Contains(rendered, want) {
		t.Fatalf("catalog page appearance command is not workspace-scoped: %s", rendered)
	}
}

func TestListResultPatchesOnlyReplaceServerOwnedRows(t *testing.T) {
	workspace, workspaceCatalog, assets, edges := testWorkspaceAssetFixtures()
	tests := []struct {
		name   string
		patch  map[string]any
		field  string
		nested string
	}{
		{name: "catalog", patch: CatalogListPatchForCatalogsQuery([]catalog.Catalog{workspaceCatalog}, "sales"), field: "dashboards"},
		{name: "workspaces", patch: WorkspacesListResultsPatch([]workspaceview.WorkspaceView{workspace}), field: "workspaces"},
		{name: "workspace assets", patch: WorkspaceAssetListResultsPatch(workspace.ID, assets, edges), field: "assetList", nested: "assets"},
		{name: "connections", patch: ConnectionsListResultsPatch(assets, edges), field: "connections"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, ok := test.patch["page"].(map[string]any)
			if !ok || len(test.patch) != 1 || len(page) != 1 {
				t.Fatalf("list patch must contain only one page result field: %#v", test.patch)
			}
			value, ok := page[test.field]
			if !ok {
				t.Fatalf("list patch missing page.%s: %#v", test.field, test.patch)
			}
			if test.nested == "" {
				return
			}
			nested, ok := value.(map[string]any)
			if !ok || len(nested) != 1 {
				t.Fatalf("list patch must contain only page.%s.%s: %#v", test.field, test.nested, test.patch)
			}
			if _, ok := nested[test.nested]; !ok {
				t.Fatalf("list patch missing page.%s.%s: %#v", test.field, test.nested, test.patch)
			}
		})
	}
}

func TestCatalogSignalIncludesDashboardListMetadata(t *testing.T) {
	workspaceCatalog := catalog.Catalog{
		Workspace:  catalog.Workspace{ID: "sales", Title: "Sales"},
		Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive"}},
	}
	dashboardID := workspaceCatalog.Workspace.ID + "." + workspaceCatalog.Dashboards[0].ID
	page := catalogPageForCatalogs([]catalog.Catalog{workspaceCatalog}, CatalogListOptions{Metadata: map[string]CatalogDashboardMetadata{
		dashboardID: {Popularity: uisignals.PopularityLevelMedium, LastRefreshedAt: "2026-08-12T09:42:00Z"},
	}})
	if len(page.Dashboards) == 0 {
		t.Fatal("catalog dashboard missing")
	}
	dashboard := page.Dashboards[0]
	if dashboard.Popularity == nil || *dashboard.Popularity != uisignals.PopularityLevelMedium {
		t.Fatalf("catalog dashboard popularity = %#v", dashboard.Popularity)
	}
	if dashboard.LastRefreshedAt == nil || *dashboard.LastRefreshedAt != "2026-08-12T09:42:00Z" {
		t.Fatalf("catalog dashboard last refreshed = %#v", dashboard.LastRefreshedAt)
	}
	if dashboard.Workspace != "Sales" {
		t.Fatalf("catalog dashboard workspace = %q, want Sales", dashboard.Workspace)
	}
}

func TestCatalogSignalFiltersDashboardsByWorkspaceAndKeepsOptions(t *testing.T) {
	catalogs := []catalog.Catalog{
		{Workspace: catalog.Workspace{ID: "sales", Title: "Sales"}, Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive"}}},
		{Workspace: catalog.Workspace{ID: "operations", Title: "Operations"}, Dashboards: []catalog.Dashboard{{ID: "fulfillment", Title: "Fulfillment"}}},
	}

	page := catalogPageForCatalogs(catalogs, CatalogListOptions{WorkspaceFilter: "operations"})
	if len(page.Dashboards) != 1 || page.Dashboards[0].ID != "operations.fulfillment" {
		t.Fatalf("filtered dashboards = %#v", page.Dashboards)
	}
	if got := uisignals.ValueOrZero(page.ListFilter); got != "operations" {
		t.Fatalf("list filter = %q", got)
	}
	if len(page.WorkspaceFilters) != 2 || page.WorkspaceFilters[0].ID != "sales" || page.WorkspaceFilters[1].ID != "operations" {
		t.Fatalf("workspace filters = %#v", page.WorkspaceFilters)
	}
}

func TestWorkspaceAssetDetailsRenderSharedShapeForSemanticModel(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "model")

	var out strings.Builder
	err := WorkspaceAssetPage(catalog, workspace, asset, assets, edges, "details", "Owner", testLayoutProvider()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, edges, "details", "Owner", AssetRefreshState{}, AssetVersionsState{}))

	for _, want := range []string{
		"<lv-app-shell",
		"<lv-workspace-asset-page",
		`"breadcrumbs":[`,
		"Workspaces",
		"LeapView Workspace",
		"Olist Commerce",
		"Details",
		"Lineage",
		`<script type="module" src="/static/semantic-model-graph.js?v=dev"></script>`,
		`"overview":[`,
		`"semanticModelGraph":`,
		"Model tables (1)",
		"Measures (1)",
		"Relationships (1)",
		`"header":"Name"`,
		`"id":"name"`,
		`"header":"Primary key"`,
		`"id":"primary_key"`,
		`"header":"Cardinality"`,
		`"id":"cardinality"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("semantic model details did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		"Default connection",
		"Connections (1)",
		"Sources (1)",
		"Fields (1)",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("semantic model details rendered non-composition content %q:\n%s", notWant, rendered)
		}
	}
	if strings.Contains(rendered, "Published from Git/YAML") {
		t.Fatalf("semantic model details rendered hardcoded publication source:\n%s", rendered)
	}
}

func TestWorkspacePagesLoadThemeBeforeDeferredRouteModules(t *testing.T) {
	workspace, catalog, assets, _ := testWorkspaceAssetFixtures()

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, assets, "", "", "Owner", WorkspaceAccessResponse{}, "", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())

	themeScript := `<script src="/static/theme.js?v=dev"></script>`
	routeScript := `<script type="module" src="/static/workspace-page.js?v=dev"></script>`
	if !strings.Contains(rendered, themeScript) {
		t.Fatalf("workspace page did not render blocking theme script %q:\n%s", themeScript, rendered)
	}
	if strings.Contains(rendered, `<script type="module" src="/static/theme.js?v=dev"></script>`) {
		t.Fatalf("workspace page rendered theme script as deferred module:\n%s", rendered)
	}
	if strings.Index(rendered, themeScript) > strings.Index(rendered, routeScript) {
		t.Fatalf("workspace page rendered theme script after route module:\n%s", rendered)
	}
}

func TestWorkspaceAssetDetailSignalsUseSharedGridShape(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	byType := map[string]workspaceview.AssetView{}
	for _, asset := range assets {
		if _, ok := byType[asset.Type]; !ok {
			byType[asset.Type] = asset
		}
	}

	semanticPage := workspaceAssetPageSignal(workspace, byType["semantic_model"], assets, edges, "details", assetLineage(workspace.ID, byType["semantic_model"], assets, edges))
	modelTablesTable := detailSectionTable(t, semanticPage.Details.Sections, "Model tables")
	relationshipsGrid := detailSectionTable(t, semanticPage.Details.Sections, "Relationships")
	assertTableHeaders(t, modelTablesTable, []string{"Name", "Primary key", "Fields", "Measures", "Last refreshed", "Refresh status", "Description"})
	assertTableHeaders(t, relationshipsGrid, []string{"ID", "From table", "From field", "To table", "To field", "Cardinality", "Active"})
	assertTableMissingHeaders(t, modelTablesTable, []string{"Source", "Reads", "SQL preview"})
	assertNoDetailSection(t, semanticPage.Details.Sections, "Connections")
	assertNoDetailSection(t, semanticPage.Details.Sections, "Sources")
	assertNoDetailSection(t, semanticPage.Details.Sections, "Fields")

	dashboardPage := workspaceAssetPageSignal(workspace, byType["dashboard"], assets, edges, "details", assetLineage(workspace.ID, byType["dashboard"], assets, edges))
	for _, title := range []string{"Pages", "Filters", "Visuals"} {
		detailSectionTable(t, dashboardPage.Details.Sections, title)
	}

	lineagePage := workspaceAssetPageSignal(workspace, byType["dashboard"], assets, edges, "lineage", assetLineage(workspace.ID, byType["dashboard"], assets, edges))
	if len(lineagePage.Lineage.Graph.Nodes) == 0 || len(lineagePage.Lineage.UsesTable.Columns) == 0 || len(lineagePage.Lineage.UsedByTable.Columns) == 0 {
		t.Fatalf("lineage page did not seed graph and relation tables: %#v", lineagePage.Lineage)
	}
}

func TestWorkspaceAssetPageExposesVersionsSurface(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "dashboard")
	versions := AssetVersionsState{
		CurrentContentHash: "hash_current",
		Versions: []AssetVersionState{{
			ServingStateID: "state_1",
			Status:         "active",
			CreatedBy:      "tester",
			CreatedAt:      "2026-01-01",
			ContentHash:    "hash_current",
			SourceFile:     "dashboards/test.yaml",
		}},
	}

	page := workspaceAssetPageSignalWithRefreshAndVersions(workspace, asset, assets, edges, "versions", assetLineage(workspace.ID, asset, assets, edges), AssetRefreshState{}, versions)
	foundVersions := false
	for _, tab := range page.Tabs {
		if tab.ID == "versions" && tab.Label == "Versions" && tab.Active {
			foundVersions = true
		}
	}
	if !foundVersions {
		t.Fatalf("workspace asset tabs missing active versions tab: %#v", page.Tabs)
	}
	if !ValidWorkspaceAssetSection("versions") {
		t.Fatal("versions section is not valid")
	}

	var out strings.Builder
	err := WorkspaceAssetPageWithRefreshAndVersions(catalog, workspace, asset, assets, edges, "versions", "Owner", AssetRefreshState{}, versions).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, edges, "versions", "Owner", AssetRefreshState{}, versions))
	for _, want := range []string{`"label":"Versions"`, `"versions":`, `route=workspace_asset`, `section=versions`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("workspace asset page missing versions surface %q:\n%s", want, rendered)
		}
	}
}

func TestConnectionAssetPagesHideVersionsSurface(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	connection := testAssetByID(t, assets, "connection")
	source := testAssetByID(t, assets, "source")

	connectionPage := connectionAssetPageSignal(workspace, connection, assets, edges, "details", assetLineage(workspace.ID, connection, assets, edges))
	for _, tab := range connectionPage.Tabs {
		if tab.ID == "versions" || tab.Label == "Versions" {
			t.Fatalf("connection asset tabs include versions: %#v", connectionPage.Tabs)
		}
	}

	sourcePage := connectionSourceAssetPageSignal(workspace, connection, source, assets, edges, "details", assetLineage(workspace.ID, source, assets, edges))
	for _, tab := range sourcePage.Tabs {
		if tab.ID == "versions" || tab.Label == "Versions" {
			t.Fatalf("source asset tabs include versions: %#v", sourcePage.Tabs)
		}
	}
}

func TestSemanticModelDetailsSignalIncludesModelGraph(t *testing.T) {
	workspace := workspaceview.WorkspaceView{ID: "leapview", Title: "LeapView Workspace"}
	asset := workspaceview.AssetView{
		ID:          "semantic_model:commerce",
		WorkspaceID: workspace.ID,
		Type:        "semantic_model",
		Key:         "commerce",
		Title:       "Commerce Model",
		Payload: map[string]any{
			"Measures": map[string]any{"order_count": map[string]any{"Fact": "orders"}},
			"Tables": map[string]any{
				"orders": map[string]any{
					"PrimaryKey":  "order_id",
					"Description": "One row per order.",
					"Dimensions": map[string]any{
						"order_id":    map[string]any{"Label": "Order ID"},
						"customer_id": map[string]any{"Label": "Customer ID"},
						"state":       map[string]any{"Label": "State"},
					},
					"Schema": map[string]any{"Columns": []any{
						map[string]any{"Name": "order_id", "Ordinal": float64(1), "PhysicalType": "VARCHAR", "PrimaryKey": true},
						map[string]any{"Name": "customer_id", "Ordinal": float64(2), "PhysicalType": "VARCHAR"},
						map[string]any{"Name": "state", "Ordinal": float64(3), "PhysicalType": "VARCHAR"},
					}},
				},
				"customers": map[string]any{
					"PrimaryKey": "customer_id",
					"Dimensions": map[string]any{
						"customer_id": map[string]any{"Label": "Customer ID"},
						"segment":     map[string]any{"Label": "Segment"},
					},
				},
			},
			"Relationships": []any{
				map[string]any{"ID": "orders_customers", "From": "orders.customer_id", "To": "customers.customer_id", "Cardinality": "many_to_one"},
			},
		},
	}

	page := workspaceAssetPageSignal(workspace, asset, []workspaceview.AssetView{asset}, nil, "details", assetLineage(workspace.ID, asset, []workspaceview.AssetView{asset}, nil))
	graph := page.Details.SemanticModelGraph
	if graph == nil {
		t.Fatalf("semantic model details did not include graph: %#v", page.Details)
	}
	facts := uisignals.ValueOrZero(graph.Facts)
	if len(facts) != 1 || facts[0] != "orders" {
		t.Fatalf("graph facts = %v, want orders", facts)
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("graph nodes = %d, want 2: %#v", len(graph.Nodes), graph.Nodes)
	}
	orders := graphNodeByID(t, graph.Nodes, "orders")
	if orders.Title != "orders" || uisignals.ValueOrZero(orders.PrimaryKey) != "order_id" {
		t.Fatalf("orders node = %#v, want title orders and primary key order_id", orders)
	}
	assertGraphField(t, orders.Fields, "order_id", true, false, nil)
	assertGraphField(t, orders.Fields, "customer_id", false, true, []string{"orders_customers"})
	customers := graphNodeByID(t, graph.Nodes, "customers")
	assertGraphField(t, customers.Fields, "customer_id", true, true, []string{"orders_customers"})
	if len(graph.Edges) != 1 {
		t.Fatalf("graph edges = %d, want 1: %#v", len(graph.Edges), graph.Edges)
	}
	edge := graph.Edges[0]
	if edge.ID != "orders_customers" || edge.Source != "orders" || edge.Target != "customers" || edge.SourceField != "customer_id" || edge.TargetField != "customer_id" {
		t.Fatalf("graph edge endpoints = %#v, want orders.customer_id -> customers.customer_id", edge)
	}
	if edge.Cardinality != "many_to_one" || edge.Label != "*:1" {
		t.Fatalf("graph edge cardinality = %#v, want many_to_one labeled *:1", edge)
	}
}

func TestNonSemanticAssetDetailsSignalOmitsModelGraph(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "dashboard")

	page := workspaceAssetPageSignal(workspace, asset, assets, edges, "details", assetLineage(workspace.ID, asset, assets, edges))
	if page.Details.SemanticModelGraph != nil {
		t.Fatalf("dashboard details included semantic model graph: %#v", page.Details.SemanticModelGraph)
	}
}

func TestWorkspaceAssetDetailsRenderModelTableComposition(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "table-transform")

	var out strings.Builder
	err := WorkspaceAssetPage(catalog, workspace, asset, assets, edges, "details", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, edges, "details", "Owner", AssetRefreshState{}, AssetVersionsState{}))

	for _, want := range []string{
		"<lv-workspace-asset-page",
		`"overview":[`,
		"Fields (2)",
		`"title":"SQL"`,
		`"code":"SELECT order_id, SUM(payment_value) AS revenue FROM source.payments GROUP BY order_id"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("model table details did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		"Source / transform",
		"/static/code-block.js",
		"semantic-model-graph.js",
		`"semanticModelGraph":`,
		`<lv-code-block language="sql"`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("model table details rendered source definition content %q:\n%s", notWant, rendered)
		}
	}
	if strings.Contains(rendered, "Measures (") {
		t.Fatalf("model table details rendered measures:\n%s", rendered)
	}
}

func TestWorkspaceAssetDetailsRenderDirectSourceModelTableWithoutSQL(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "table-model")

	var out strings.Builder
	err := WorkspaceAssetPage(catalog, workspace, asset, assets, edges, "details", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, edges, "details", "Owner", AssetRefreshState{}, AssetVersionsState{}))

	for _, want := range []string{
		"<lv-workspace-asset-page",
		`"overview":[`,
		"Fields (2)",
		`"header":"Physical type"`,
		`"id":"physical_type"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("direct source model table details did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		"SQL",
		"Source / transform",
		"/static/code-block.js",
		`<lv-code-block language="sql"`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("direct source model table details rendered %q:\n%s", notWant, rendered)
		}
	}
}

func TestWorkspaceAssetDetailsRenderSourceSchema(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "source")

	var out strings.Builder
	err := WorkspaceAssetPage(catalog, workspace, asset, assets, edges, "details", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, edges, "details", "Owner", AssetRefreshState{}, AssetVersionsState{}))

	for _, want := range []string{
		"<lv-workspace-asset-page",
		`"overview":[`,
		"Fields (2)",
		`"header":"Physical type"`,
		`"id":"physical_type"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("source details did not render %q:\n%s", want, rendered)
		}
	}
	page := workspaceAssetPageSignal(workspace, asset, assets, edges, "details", assetLineage(workspace.ID, asset, assets, edges))
	grid := detailSectionTable(t, page.Details.Sections, "Fields")
	assertTableHeaders(t, grid, []string{"Name", "Description", "Physical type", "Nullable"})
	if len(grid.Rows) != 2 {
		t.Fatalf("source field rows = %#v, want 2 rows", grid.Rows)
	}
	state := grid.Rows[1]
	if state["name"] != "customer_id" || fmt.Sprint(state["description"]) != "Raw customer identifier." {
		t.Fatalf("unexpected source field row: %#v", state)
	}
}

func TestWorkspaceAssetDetailSignalsIncludeModelTableDefinition(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()

	directAsset := testAssetByID(t, assets, "table-model")
	directPage := workspaceAssetPageSignal(workspace, directAsset, assets, edges, "details", assetLineage(workspace.ID, directAsset, assets, edges))
	directFields := detailSectionTable(t, directPage.Details.Sections, "Fields")
	assertTableHeaders(t, directFields, []string{"Name", "Label", "Physical type", "Nullable", "Key", "Description"})
	assertTableMissingHeaders(t, directFields, []string{"Expression", "Filter", "Order", "Model table", "Measures"})
	assertNoDetailSection(t, directPage.Details.Sections, "Definition")
	assertNoDetailSection(t, directPage.Details.Sections, "SQL")

	transformAsset := testAssetByID(t, assets, "table-transform")
	transformPage := workspaceAssetPageSignal(workspace, transformAsset, assets, edges, "details", assetLineage(workspace.ID, transformAsset, assets, edges))
	assertNoDetailSection(t, transformPage.Details.Sections, "Definition")
	assertNoDetailSection(t, transformPage.Details.Sections, "Measures")
}

func TestWorkspaceAssetDetailsRenderUnknownNullableAsDash(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()

	source := testAssetByID(t, assets, "source-payments")
	source.Payload["Fields"] = map[string]any{
		"order_id": map[string]any{"Description": "Raw order identifier."},
	}
	sourcePage := workspaceAssetPageSignal(workspace, source, assets, edges, "details", assetLineage(workspace.ID, source, assets, edges))
	sourceGrid := detailSectionTable(t, sourcePage.Details.Sections, "Fields")
	if len(sourceGrid.Rows) != 1 {
		t.Fatalf("source fallback rows = %#v, want 1", sourceGrid.Rows)
	}
	if got := fmt.Sprint(sourceGrid.Rows[0]["nullable"]); got != "-" {
		t.Fatalf("source fallback nullable = %q, want -", got)
	}

	modelTable := testAssetByID(t, assets, "table-model")
	modelTable.Payload["Schema"] = map[string]any{"columns": []any{
		map[string]any{"name": "order_id", "ordinal": float64(1), "physicalType": "VARCHAR", "primaryKey": true},
	}}
	modelPage := workspaceAssetPageSignal(workspace, modelTable, assets, edges, "details", assetLineage(workspace.ID, modelTable, assets, edges))
	modelGrid := detailSectionTable(t, modelPage.Details.Sections, "Fields")
	if got := fmt.Sprint(modelGrid.Rows[0]["nullable"]); got != "-" {
		t.Fatalf("model table missing nullable = %q, want -", got)
	}
}

func TestAssetLineageProjectsRecursiveDependenciesAndContext(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "page-item")

	lineage := assetLineage(workspace.ID, asset, assets, edges)

	assertLineageNodeKindsExact(t, lineage.Graph, []string{
		"connection",
		"model_table",
		"dashboard",
		"semantic_model",
		"source",
	})
	assertLineageEdgeKinds(t, lineage.Graph, []string{
		"lineage_connection_source",
		"lineage_source_model_table",
		"lineage_model_table_semantic_model",
		"lineage_semantic_model_dashboard",
	})
	assertLineageEdgesMoveLeftToRight(t, lineage.Graph)
	assertLineageSelectedNode(t, lineage.Graph, "dashboard")
	assertTableRelations(t, lineage.Uses, []string{"Powers dashboard"})
	assertTableRelations(t, lineage.UsedBy, nil)
	if tableHasRelation(lineage.Uses, "Contains") || tableHasRelation(lineage.UsedBy, "Contains") {
		t.Fatalf("dependency tables included contains edges: uses=%#v usedBy=%#v", lineage.Uses.Rows, lineage.UsedBy.Rows)
	}
	if tableHasRelation(lineage.Uses, "Uses measure") || tableHasRelation(lineage.UsedBy, "Uses measure") {
		t.Fatalf("lineage tables included hidden measure edge: uses=%#v usedBy=%#v", lineage.Uses.Rows, lineage.UsedBy.Rows)
	}
	if lineage.Count != 1 {
		t.Fatalf("lineage count included non-graph edges, got %d", lineage.Count)
	}
}

func TestAssetLineageProjectsRecursiveConsumers(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "semantic-table")

	lineage := assetLineage(workspace.ID, asset, assets, edges)

	assertLineageNodeKindsExact(t, lineage.Graph, []string{
		"connection",
		"dashboard",
		"model_table",
		"semantic_model",
		"source",
	})
	assertLineageEdgesMoveLeftToRight(t, lineage.Graph)
	assertLineageSelectedNode(t, lineage.Graph, "semantic_model")
	assertTableRelations(t, lineage.Uses, []string{"Feeds semantic model"})
	assertTableRelations(t, lineage.UsedBy, []string{"Powers dashboard"})
	if tableHasRelation(lineage.Uses, "Contains") || tableHasRelation(lineage.UsedBy, "Contains") {
		t.Fatalf("consumer/dependency tables included contains edges: uses=%#v usedBy=%#v", lineage.Uses.Rows, lineage.UsedBy.Rows)
	}
	if tableHasRelation(lineage.Uses, "Uses semantic table") || tableHasRelation(lineage.UsedBy, "Uses semantic table") {
		t.Fatalf("lineage tables included hidden semantic table edge: uses=%#v usedBy=%#v", lineage.Uses.Rows, lineage.UsedBy.Rows)
	}
}

func TestAssetLineageDashboardDerivesMeasureConsumers(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "dashboard")

	lineage := assetLineage(workspace.ID, asset, assets, edges)

	assertLineageNodeKindsExact(t, lineage.Graph, []string{
		"connection",
		"dashboard",
		"model_table",
		"semantic_model",
		"source",
	})
	assertLineageEdgeKinds(t, lineage.Graph, []string{"lineage_semantic_model_dashboard"})
	assertLineageEdgesMoveLeftToRight(t, lineage.Graph)
	node := assertLineageNode(t, lineage.Graph, "dashboard")
	if uisignals.ValueOrZero(node.VisibleUpstreamCount) != 1 || uisignals.ValueOrZero(node.VisibleDownstreamCount) != 0 {
		t.Fatalf("dashboard visible counts = upstream %d downstream %d, want 1/0: %#v", uisignals.ValueOrZero(node.VisibleUpstreamCount), uisignals.ValueOrZero(node.VisibleDownstreamCount), node)
	}
	if uisignals.ValueOrZero(node.UsesCount) != 1 || uisignals.ValueOrZero(node.UsedByCount) != 0 {
		t.Fatalf("dashboard full-fidelity counts = uses %d usedBy %d, want 1/0: %#v", uisignals.ValueOrZero(node.UsesCount), uisignals.ValueOrZero(node.UsedByCount), node)
	}
	if uisignals.ValueOrZero(node.ContainedCount) != 4 || uisignals.ValueOrZero(node.ContainedSummary) != "1 filter, 1 page, 2 visuals" {
		t.Fatalf("dashboard contained summary = %d %q, want 4 dashboard children: %#v", uisignals.ValueOrZero(node.ContainedCount), uisignals.ValueOrZero(node.ContainedSummary), node)
	}
	assertTableRelations(t, lineage.Uses, []string{"Powers dashboard"})
	assertTableRelations(t, lineage.UsedBy, nil)
}

func TestAssetLineageSemanticModelDerivesMeasureDashboardPath(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "model")

	lineage := assetLineage(workspace.ID, asset, assets, edges)

	assertLineageNodeKindsExact(t, lineage.Graph, []string{
		"connection",
		"dashboard",
		"model_table",
		"semantic_model",
		"source",
	})
	assertLineageEdgeKinds(t, lineage.Graph, []string{"lineage_semantic_model_dashboard"})
	assertLineageEdgesMoveLeftToRight(t, lineage.Graph)
	node := assertLineageNode(t, lineage.Graph, "model")
	if uisignals.ValueOrZero(node.VisibleUpstreamCount) != 1 || uisignals.ValueOrZero(node.VisibleDownstreamCount) != 1 {
		t.Fatalf("semantic model visible counts = upstream %d downstream %d, want 1/1: %#v", uisignals.ValueOrZero(node.VisibleUpstreamCount), uisignals.ValueOrZero(node.VisibleDownstreamCount), node)
	}
	if uisignals.ValueOrZero(node.UsesCount) != 0 || uisignals.ValueOrZero(node.UsedByCount) != 1 {
		t.Fatalf("semantic model full-fidelity counts = uses %d usedBy %d, want 0/1: %#v", uisignals.ValueOrZero(node.UsesCount), uisignals.ValueOrZero(node.UsedByCount), node)
	}
	if uisignals.ValueOrZero(node.ContainedCount) != 3 || uisignals.ValueOrZero(node.ContainedSummary) != "1 measure, 1 relationship, 1 semantic table" {
		t.Fatalf("semantic model contained summary = %d %q, want 3 semantic children: %#v", uisignals.ValueOrZero(node.ContainedCount), uisignals.ValueOrZero(node.ContainedSummary), node)
	}
	assertTableRelations(t, lineage.Uses, []string{"Feeds semantic model"})
	assertTableRelations(t, lineage.UsedBy, []string{"Powers dashboard"})
}

func TestAssetLineageFallsBackToContainsWhenNoDependenciesExist(t *testing.T) {
	workspace, _, assets, edges := testWorkspaceAssetFixtures()
	asset := testAssetByID(t, assets, "catalog")

	lineage := assetLineage(workspace.ID, asset, assets, edges)

	assertLineageEdgeKinds(t, lineage.Graph, []string{"contains"})
	assertTableRelations(t, lineage.Uses, []string{"Contains"})
	assertTableRelations(t, lineage.UsedBy, nil)
	if lineage.Count != 5 {
		t.Fatalf("contains fallback should count direct hierarchy context, got %d", lineage.Count)
	}
}

func TestLineageProjectionPolicy(t *testing.T) {
	layerTests := []struct {
		typ     string
		want    int
		visible bool
	}{
		{typ: "connection", want: 0, visible: true},
		{typ: "source", want: 1, visible: true},
		{typ: "model_table", want: 2, visible: true},
		{typ: "semantic_model", want: 3, visible: true},
		{typ: "dashboard", want: 4, visible: true},
		{typ: "measure", want: -1, visible: false},
		{typ: "field", want: -1, visible: false},
	}
	for _, tt := range layerTests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := lineageVisualLayer(tt.typ); got != tt.want {
				t.Fatalf("lineageVisualLayer(%q) = %d, want %d", tt.typ, got, tt.want)
			}
			if got := isLineageVisibleGraphAsset(tt.typ); got != tt.visible {
				t.Fatalf("isLineageVisibleGraphAsset(%q) = %v, want %v", tt.typ, got, tt.visible)
			}
		})
	}

	edgeTests := []struct {
		name       string
		sourceType string
		targetType string
		fallback   string
		wantKind   string
		wantLabel  string
	}{
		{
			name:       "connection source",
			sourceType: "connection",
			targetType: "source",
			fallback:   "uses_connection",
			wantKind:   "lineage_connection_source",
			wantLabel:  "Provides source",
		},
		{
			name:       "source model table",
			sourceType: "source",
			targetType: "model_table",
			fallback:   "reads_source",
			wantKind:   "lineage_source_model_table",
			wantLabel:  "Feeds model table",
		},
		{
			name:       "model table semantic model",
			sourceType: "model_table",
			targetType: "semantic_model",
			fallback:   "uses_model_table",
			wantKind:   "lineage_model_table_semantic_model",
			wantLabel:  "Feeds semantic model",
		},
		{
			name:       "semantic model dashboard",
			sourceType: "semantic_model",
			targetType: "dashboard",
			fallback:   "uses_semantic_model",
			wantKind:   "lineage_semantic_model_dashboard",
			wantLabel:  "Powers dashboard",
		},
		{
			name:       "fallback",
			sourceType: "field",
			targetType: "dashboard",
			fallback:   "filters_field",
			wantKind:   "filters_field",
			wantLabel:  "Filters field",
		},
	}
	for _, tt := range edgeTests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lineageCollapsedEdgeKind(tt.sourceType, tt.targetType, tt.fallback); got != tt.wantKind {
				t.Fatalf("lineageCollapsedEdgeKind(%q, %q, %q) = %q, want %q", tt.sourceType, tt.targetType, tt.fallback, got, tt.wantKind)
			}
			if got := lineageCollapsedEdgeLabel(tt.sourceType, tt.targetType, tt.fallback); got != tt.wantLabel {
				t.Fatalf("lineageCollapsedEdgeLabel(%q, %q, %q) = %q, want %q", tt.sourceType, tt.targetType, tt.fallback, got, tt.wantLabel)
			}
		})
	}
}

func TestCollapsedAssetLineageCollapsesMeasureUsageToSemanticModel(t *testing.T) {
	workspaceID := "leapview"
	assets := map[string]workspaceview.AssetView{
		"model-a": {
			ID:          "model-a",
			WorkspaceID: workspaceID,
			Type:        "semantic_model",
			Key:         "model_a",
			Title:       "Model A",
		},
		"measure-a": {
			ID:          "measure-a",
			WorkspaceID: workspaceID,
			Type:        "measure",
			Key:         "model_a.revenue",
			ParentID:    "model-a",
			Title:       "Revenue",
		},
		"dashboard-a": {
			ID:          "dashboard-a",
			WorkspaceID: workspaceID,
			Type:        "dashboard",
			Key:         "dashboard-a",
			Title:       "Dashboard A",
		},
		"model-b": {
			ID:          "model-b",
			WorkspaceID: workspaceID,
			Type:        "semantic_model",
			Key:         "model_b",
			Title:       "Model B",
		},
		"dashboard-b": {
			ID:          "dashboard-b",
			WorkspaceID: workspaceID,
			Type:        "dashboard",
			Key:         "dashboard-b",
			Title:       "Dashboard B",
		},
	}
	graph := assetLineageGraph{
		Nodes: []assetLineageNode{
			{ID: "model-a"},
			{ID: "measure-a"},
			{ID: "dashboard-a"},
			{ID: "model-b"},
			{ID: "dashboard-b"},
		},
		Edges: []assetLineageEdge{
			{ID: "dashboard-a-measure-a", Source: "dashboard-a", Target: "measure-a", Kind: "uses_measure"},
			{ID: "dashboard-a-model-a", Source: "dashboard-a", Target: "model-a", Kind: "uses_semantic_model"},
			{ID: "dashboard-b-model-b", Source: "dashboard-b", Target: "model-b", Kind: "uses_semantic_model"},
		},
	}

	lineage := collapsedAssetLineageGraph(workspaceID, assets["dashboard-b"], graph, assets, nil)

	assertLineageHasEdge(t, lineage, "model-a", "dashboard-a", "lineage_semantic_model_dashboard")
	assertLineageMissingNode(t, lineage, "measure-a")
	assertLineageHasEdge(t, lineage, "model-b", "dashboard-b", "lineage_semantic_model_dashboard")
}

func TestConnectionAssetDetailsRenderConnectionSurface(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	var connection workspaceview.AssetView
	for _, asset := range assets {
		if asset.Type == "connection" {
			connection = asset
			break
		}
	}

	var out strings.Builder
	err := ConnectionAssetPage(catalog, workspace, connection, assets, edges, "details", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(ConnectionAssetBootstrapSignals(catalog, workspace, connection, assets, edges, "details", "Owner", AssetVersionsState{}))

	for _, want := range []string{
		"<lv-workspace-asset-page",
		`assetWorkspace=leapview`,
		`"breadcrumbs":[`,
		"Olist connection",
		`"overview":[`,
		"Type",
		"Connection",
		"Kind",
		"Credentials",
		"Sources",
		`Back to connections`,
		`"href":"/connections/connection:olist.olist/lineage"`,
		`"/connections/connection:olist.olist/sources/source:olist.orders/details"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("connection details did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		"Published from Git/YAML",
		">Parent</span>",
		"Back to workspace",
		`href="/workspaces/leapview/assets/connection:olist.olist/details"`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("connection details rendered workspace-only content %q:\n%s", notWant, rendered)
		}
	}
}

func TestConnectionsPageListsOnlyConnectionsWithConnectionFields(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()

	var out strings.Builder
	err := ConnectionsPage(catalog, workspace.ID, assets, edges, "", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(ConnectionsBootstrapSignals(catalog, workspace.ID, assets, edges, "", "Owner"))

	for _, want := range []string{
		`<lv-connections-page`,
		`data-on:lv-entity-list-query__debounce.200ms=`,
		`@post('/connections/search'`,
		`"connections":[`,
		`"kind":"local"`,
		`"sourceCount":1`,
		`"credentialStatus":"Not configured"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("connections page did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{`"typeLabel":"Source"`, `?type=source`, `"title":"orders"`} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("connections page mixed source content %q:\n%s", notWant, rendered)
		}
	}
	if strings.Contains(rendered, `data-workspace-asset-toolbar`) {
		t.Fatalf("connections page rendered workspace toolbar:\n%s", rendered)
	}
}

func TestConnectionLifecycleUsesOneStateDrivenPrimaryAction(t *testing.T) {
	_, _, assets, edges := testWorkspaceAssetFixtures()
	connection := testAssetByID(t, assets, "connection")
	logical := ConnectionLogicalName(connection, assets, edges)
	tests := []struct {
		name        string
		binding     *ConnectionBindingView
		wantState   string
		wantPrimary string
	}{
		{name: "missing", wantState: "missing", wantPrimary: "configure"},
		{name: "pending", binding: &ConnectionBindingView{ID: logical, LogicalConnection: logical, Enabled: true, Health: "pending", Revision: 1}, wantState: "pending", wantPrimary: "test"},
		{name: "healthy", binding: &ConnectionBindingView{ID: logical, LogicalConnection: logical, Enabled: true, Health: "healthy", Revision: 2}, wantState: "healthy", wantPrimary: "refresh"},
		{name: "degraded", binding: &ConnectionBindingView{ID: logical, LogicalConnection: logical, Enabled: true, Health: "degraded", Revision: 3}, wantState: "degraded", wantPrimary: "refresh"},
		{name: "disabled", binding: &ConnectionBindingView{ID: logical, LogicalConnection: logical, Health: "disabled", Revision: 4}, wantState: "disabled", wantPrimary: "enable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			administration := ConnectionAdministrationView{
				Bindings: map[string]ConnectionBindingView{}, CanManage: true, CanTest: true,
				RequiresBinding: map[string]bool{logical: true},
			}
			if test.binding != nil {
				administration.Bindings[logical] = *test.binding
			}
			lifecycle := ConnectionLifecycleForAsset(connection, assets, edges, administration)
			if lifecycle.State != test.wantState {
				t.Fatalf("state = %q, want %q", lifecycle.State, test.wantState)
			}
			primary := ""
			primaryCount := 0
			for _, action := range lifecycle.Actions {
				if action.Primary {
					primary, primaryCount = action.ID, primaryCount+1
				}
			}
			if primary != test.wantPrimary || primaryCount != 1 {
				t.Fatalf("primary action = %q count=%d, want %q count=1: %#v", primary, primaryCount, test.wantPrimary, lifecycle.Actions)
			}
		})
	}
}

func TestConnectionSourceAssetDetailsRenderConnectionChrome(t *testing.T) {
	workspace, catalog, assets, edges := testWorkspaceAssetFixtures()
	source := testAssetByID(t, assets, "source")
	connection := testAssetByID(t, assets, "connection")
	logical := ConnectionLogicalName(connection, assets, edges)
	administration := ConnectionAdministrationView{
		Bindings: map[string]ConnectionBindingView{}, CanManage: true, CanTest: true,
		RequiresBinding: map[string]bool{logical: false},
	}

	var out strings.Builder
	err := ConnectionSourceAssetPageWithAdministrationForEnvironment(catalog, workspace, connection, source, assets, edges, "details", "dev", "Owner", AssetVersionsState{}, administration, ConnectionCommandBindings{}, "csrf", nil).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(ConnectionSourceAssetBootstrapSignalsWithAdministrationForEnvironment(catalog, workspace, connection, source, assets, edges, "details", "dev", "Owner", AssetVersionsState{}, administration, testLayoutProvider()))

	for _, want := range []string{
		`<lv-workspace-asset-page`,
		`"drawerParent":`,
		`assetWorkspace=leapview`,
		"Connections",
		"Olist connection",
		"Sources",
		"orders",
		"Fields",
		`"href":"/connections/connection:olist.olist/sources/source:olist.orders/lineage"`,
		`"state":"not_required"`,
		`"statusLabel":"Not required"`,
		`"chrome":`,
		`"active":"connections"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("connection-scoped source details did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		"Workspaces /",
		"Back to workspace",
		`href="/workspaces/leapview/assets/source:olist.orders/details"`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("connection-scoped source details rendered workspace content %q:\n%s", notWant, rendered)
		}
	}
}

func TestWorkspaceAssetTabsUseWorkspaceAssetTypes(t *testing.T) {
	workspace, catalog, assets, _ := testWorkspaceAssetFixtures()

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, assets, "", "", "Owner", testWorkspaceAccess(workspace, true), "csrf", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	access := testWorkspaceAccess(workspace, true)
	rendered += bootstrapJSON(WorkspaceBootstrapSignals(catalog, workspace, assets, "", "", "Owner", access))

	for _, want := range []string{
		`<lv-workspace-page`,
		`"href":"/workspaces/leapview"`,
		`"label":"All"`,
		`"href":"/workspaces/leapview?type=model_table"`,
		`"label":"Model table"`,
		`"href":"/workspaces/leapview?type=semantic_model"`,
		`"label":"Semantic model"`,
		`"href":"/workspaces/leapview?type=dashboard"`,
		`"label":"Dashboard"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("workspace tabs did not render %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `type=connection`) {
		t.Fatalf("workspace tabs rendered connection filter:\n%s", rendered)
	}
}

func TestWorkspaceAssetRowsUseDetailLinksForModelAndMetricAssets(t *testing.T) {
	workspace, _, assets, _ := testWorkspaceAssetFixtures()
	byType := map[string]workspaceview.AssetView{}
	byID := map[string]workspaceview.AssetView{}
	for _, asset := range assets {
		byID[asset.ID] = asset
		if _, ok := byType[asset.Type]; !ok {
			byType[asset.Type] = asset
		}
	}

	for _, typ := range []string{"semantic_model", "dashboard"} {
		summary := workspaceAssetSummarySignal(workspace.ID, byType[typ], byID, nil)
		if strings.Contains(summary.DetailHref, `/models`) || strings.Contains(summary.DetailHref, `/metrics`) {
			t.Fatalf("%s asset summary rendered removed legacy detail link: %s", typ, summary.DetailHref)
		}
		if want := "/workspaces/leapview/assets/" + byType[typ].ID + "/details"; summary.DetailHref != want {
			t.Fatalf("%s asset summary detail href = %q, want %q", typ, summary.DetailHref, want)
		}
	}
}

func TestWorkspaceAssetRowsRenderTokenBackedIconColors(t *testing.T) {
	workspace, catalog, assets, _ := testWorkspaceAssetFixtures()
	visibleAssets := []workspaceview.AssetView{
		testAssetByID(t, assets, "model"),
		testAssetByID(t, assets, "table-model"),
		testAssetByID(t, assets, "dashboard"),
	}

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, visibleAssets, "", "", "Owner", testWorkspaceAccess(workspace, true), "csrf", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	access := testWorkspaceAccess(workspace, true)
	rendered += bootstrapJSON(WorkspaceBootstrapSignals(catalog, workspace, visibleAssets, "", "", "Owner", access))

	for _, want := range []string{
		`<lv-workspace-page`,
		`"typeLabel":"Dashboard"`,
		`"typeLabel":"Model table"`,
		`"typeLabel":"Semantic model"`,
		`"detailHref":"/workspaces/leapview/assets/semantic_model:olist/details"`,
		`"title":"Olist Commerce"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("workspace asset rows did not seed route payload %q:\n%s", want, rendered)
		}
	}
	dashboardIndex := strings.Index(rendered, `"title":"Executive Sales Dashboard"`)
	tableIndex := strings.Index(rendered, `"title":"orders"`)
	modelIndex := strings.Index(rendered, `"title":"Olist Commerce"`)
	if dashboardIndex < 0 || tableIndex < 0 || modelIndex < 0 {
		t.Fatalf("workspace asset rows missing expected titles:\n%s", rendered)
	}
	if !(dashboardIndex < tableIndex && tableIndex < modelIndex) {
		t.Fatalf("workspace asset rows order = dashboard:%d table:%d model:%d, want dashboard, model table, semantic model:\n%s", dashboardIndex, tableIndex, modelIndex, rendered)
	}
}

func TestWorkspaceAccessControlRendersForManagers(t *testing.T) {
	workspace, catalog, assets, _ := testWorkspaceAssetFixtures()
	access := testWorkspaceAccess(workspace, true)

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, []workspaceview.AssetView{assets[0]}, "", "", "Owner", access, "csrf", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceBootstrapSignals(catalog, workspace, []workspaceview.AssetView{assets[0]}, "", "", "Owner", access))

	for _, want := range []string{
		`<lv-workspace-page`,
		`data-on:lv-workspace-asset-filter__debounce.200ms=`,
		`@post('/workspaces/leapview/search'`,
		`data-on:lv-workspace-access-search__debounce.200ms=`,
		`/workspaces/leapview/access/search`,
		`data-on:lv-workspace-access-upsert=`,
		`data-on:lv-workspace-access-remove=`,
		`workspaceAccess`,
		`command`,
		`search`,
		`meta name="csrf-token" content="csrf"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("workspace access control did not render %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{
		`workspaceaccess=`,
		`data-attr:workspaceaccess="$workspaceAccess"`,
		`workspaceAccessCommand`,
		`workspaceAccessSearch`,
		`_csrfMeta`,
		`csrfToken`,
		`updatesUrl`,
		`routeKey`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("workspace access control rendered root-level access signal %q:\n%s", notWant, rendered)
		}
	}
}

func bootstrapJSON(signals map[string]any) string {
	return html.UnescapeString(jsonString(signals))
}

func TestWorkspaceAccessControlDoesNotRenderForViewers(t *testing.T) {
	workspace, catalog, assets, _ := testWorkspaceAssetFixtures()

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, []workspaceview.AssetView{assets[0]}, "", "", "Viewer", testWorkspaceAccess(workspace, false), "", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())

	for _, notWant := range []string{
		`workspaceaccess=`,
		`data-on:lv-workspace-access-upsert=`,
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("workspace access control rendered for viewer %q:\n%s", notWant, rendered)
		}
	}
}

func testWorkspaceAccess(workspace workspaceview.WorkspaceView, canManage bool) WorkspaceAccessResponse {
	return WorkspaceAccessResponse{
		Workspace: workspace,
		Roles: []workspaceview.RoleView{
			{Name: "viewer"},
			{Name: "editor"},
			{Name: "admin"},
		},
		Bindings: []workspaceview.RoleBindingView{
			{PrincipalID: "principal_1", WorkspaceID: workspace.ID, Email: "owner@example.com", DisplayName: "Owner", Role: "owner"},
		},
		CanManage: canManage,
	}
}

func testAccessCommandBindings() AccessCommandBindings {
	return AccessCommandBindings{
		CreateRoleBinding: accessgen.GenUIActionCreateRoleBinding(),
		UpdateRoleBinding: accessgen.GenUIActionUpdateRoleBinding(),
		DeleteRoleBinding: accessgen.GenUIActionDeleteRoleBinding(),
		CreateGrant:       accessgen.GenUIActionCreateGrant(),
		DeleteGrant:       accessgen.GenUIActionDeleteGrant(),
	}
}

func testAssetByID(t *testing.T, assets []workspaceview.AssetView, id string) workspaceview.AssetView {
	t.Helper()
	id = testLogicalAssetID(id)
	for _, asset := range assets {
		if asset.ID == id {
			return asset
		}
	}
	t.Fatalf("asset %q not found", id)
	return workspaceview.AssetView{}
}

func testLogicalAssetID(id string) string {
	if logicalID, ok := testAssetAliases[id]; ok {
		return logicalID
	}
	return id
}

var testAssetAliases = map[string]string{
	"catalog":         "catalog:leapview",
	"model":           "semantic_model:olist",
	"connection":      "connection:olist.olist",
	"source":          "source:olist.orders",
	"source-payments": "source:olist.payments",
	"table-model":     "model_table:olist.orders",
	"table-transform": "model_table:olist.payments",
	"semantic-table":  "semantic_table:olist.orders",
	"field":           "field:olist.orders.state",
	"measure":         "measure:olist.revenue",
	"relationship":    "relationship:olist.orders_customers",
	"dashboard":       "dashboard:executive-sales",
	"page":            "page:executive-sales.overview",
	"page-item":       "page_item:executive-sales.overview.revenue",
	"filter":          "filter:executive-sales.state",
	"visual":          "visual:executive-sales.revenue",
	"table":           "visual:executive-sales.orders",
}

func detailSectionTable(t *testing.T, sections []uisignals.WorkspaceDetailSectionSignal, title string) recordTable {
	t.Helper()
	for _, section := range sections {
		if detailSectionTitleMatches(section.Title, title) && section.Table != nil && len(section.Table.Columns) > 0 {
			return *section.Table
		}
	}
	t.Fatalf("detail sections missing grid %q: %#v", title, sections)
	return recordTable{}
}

func graphNodeByID(t *testing.T, nodes []uisignals.SemanticModelGraphNodeSignal, id string) uisignals.SemanticModelGraphNodeSignal {
	t.Helper()
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("graph node %q not found in %#v", id, nodes)
	return uisignals.SemanticModelGraphNodeSignal{}
}

func assertGraphField(t *testing.T, fields []uisignals.SemanticModelGraphFieldSignal, name string, primaryKey, join bool, relationships []string) {
	t.Helper()
	for _, field := range fields {
		if field.Name != name {
			continue
		}
		if uisignals.ValueOrZero(field.PrimaryKey) != primaryKey || uisignals.ValueOrZero(field.Join) != join || strings.Join(uisignals.ValueOrZero(field.Relationships), ",") != strings.Join(relationships, ",") {
			t.Fatalf("graph field %q = %#v, want primaryKey=%t join=%t relationships=%v", name, field, primaryKey, join, relationships)
		}
		return
	}
	t.Fatalf("graph field %q not found in %#v", name, fields)
}

func assertNoDetailSection(t *testing.T, sections []uisignals.WorkspaceDetailSectionSignal, title string) {
	t.Helper()
	for _, section := range sections {
		if detailSectionTitleMatches(section.Title, title) {
			t.Fatalf("detail sections unexpectedly contained %q: %#v", title, sections)
		}
	}
}

func detailSectionTitleMatches(got, want string) bool {
	return got == want || strings.HasPrefix(got, want+" (")
}

func assertLineageNodeKinds(t *testing.T, graph assetLineageGraph, expected []string) {
	t.Helper()
	got := map[string]struct{}{}
	for _, node := range graph.Nodes {
		got[node.Kind] = struct{}{}
	}
	for _, kind := range expected {
		if _, ok := got[kind]; !ok {
			t.Fatalf("lineage graph missing node kind %q: %#v", kind, graph.Nodes)
		}
	}
}

func assertLineageNodeKindsExact(t *testing.T, graph assetLineageGraph, expected []string) {
	t.Helper()
	got := map[string]int{}
	for _, node := range graph.Nodes {
		got[node.Kind]++
	}
	if len(got) != len(expected) {
		t.Fatalf("lineage graph node kinds = %#v, want exactly %#v; nodes=%#v", got, expected, graph.Nodes)
	}
	for _, kind := range expected {
		if got[kind] == 0 {
			t.Fatalf("lineage graph missing node kind %q: %#v", kind, graph.Nodes)
		}
	}
}

func assertLineageEdgeKinds(t *testing.T, graph assetLineageGraph, expected []string) {
	t.Helper()
	got := map[string]struct{}{}
	for _, edge := range graph.Edges {
		got[edge.Kind] = struct{}{}
	}
	for _, kind := range expected {
		if _, ok := got[kind]; !ok {
			t.Fatalf("lineage graph missing edge kind %q: %#v", kind, graph.Edges)
		}
	}
}

func assertTableHeaders(t *testing.T, grid recordTable, expected []string) {
	t.Helper()
	got := make([]string, 0, len(grid.Columns))
	for _, column := range grid.Columns {
		got = append(got, column.Header)
	}
	if strings.Join(got, "|") != strings.Join(expected, "|") {
		t.Fatalf("grid headers = %#v, want %#v", got, expected)
	}
}

func assertTab(t *testing.T, tabs []uisignals.WorkspaceTabSignal, id, href string, active bool) {
	t.Helper()
	for _, tab := range tabs {
		if tab.ID != id {
			continue
		}
		if tab.Href != href || tab.Active != active {
			t.Fatalf("tab %q = %#v, want href %q active %v", id, tab, href, active)
		}
		return
	}
	t.Fatalf("tab %q not found in %#v", id, tabs)
}

func assertTableMissingHeaders(t *testing.T, grid recordTable, unexpected []string) {
	t.Helper()
	got := map[string]struct{}{}
	for _, column := range grid.Columns {
		got[column.Header] = struct{}{}
	}
	for _, header := range unexpected {
		if _, ok := got[header]; ok {
			t.Fatalf("grid unexpectedly includes header %q: %#v", header, grid.Columns)
		}
	}
}

func assertTableRowValue(t *testing.T, grid recordTable, column, expected string) {
	t.Helper()
	for _, row := range grid.Rows {
		if fmt.Sprint(row[column]) == expected {
			return
		}
	}
	t.Fatalf("grid missing row with %s=%q: %#v", column, expected, grid.Rows)
}

func assertTableNoRowValue(t *testing.T, grid recordTable, column, unexpected string) {
	t.Helper()
	for _, row := range grid.Rows {
		if fmt.Sprint(row[column]) == unexpected {
			t.Fatalf("grid unexpectedly includes row with %s=%q: %#v", column, unexpected, grid.Rows)
		}
	}
}

func assertTableRowContains(t *testing.T, grid recordTable, column, expected string) {
	t.Helper()
	for _, row := range grid.Rows {
		if strings.Contains(fmt.Sprint(row[column]), expected) {
			return
		}
	}
	t.Fatalf("grid missing row with %s containing %q: %#v", column, expected, grid.Rows)
}

func assertLineageEdgesMoveLeftToRight(t *testing.T, graph assetLineageGraph) {
	t.Helper()
	ranks := map[string]int{}
	for _, node := range graph.Nodes {
		ranks[node.ID] = int(node.Rank)
	}
	for _, edge := range graph.Edges {
		if ranks[edge.Source] >= ranks[edge.Target] {
			t.Fatalf("lineage edge does not move left-to-right: edge=%#v ranks=%#v nodes=%#v", edge, ranks, graph.Nodes)
		}
	}
}

func assertLineageSelectedNode(t *testing.T, graph assetLineageGraph, wantKind string) {
	t.Helper()
	for _, node := range graph.Nodes {
		if uisignals.ValueOrZero(node.Selected) {
			if node.Kind != wantKind {
				t.Fatalf("selected lineage node kind = %q, want %q: %#v", node.Kind, wantKind, graph.Nodes)
			}
			return
		}
	}
	t.Fatalf("lineage graph has no selected node: %#v", graph.Nodes)
}

func assertLineageNode(t *testing.T, graph assetLineageGraph, id string) assetLineageNode {
	t.Helper()
	id = testLogicalAssetID(id)
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("lineage graph missing node %q: %#v", id, graph.Nodes)
	return assetLineageNode{}
}

func assertLineageMissingNode(t *testing.T, graph assetLineageGraph, id string) {
	t.Helper()
	id = testLogicalAssetID(id)
	for _, node := range graph.Nodes {
		if node.ID == id {
			t.Fatalf("lineage graph included unwanted node %q: %#v", id, graph.Nodes)
		}
	}
}

func assertLineageHasEdge(t *testing.T, graph assetLineageGraph, source, target, kind string) {
	t.Helper()
	source = testLogicalAssetID(source)
	target = testLogicalAssetID(target)
	for _, edge := range graph.Edges {
		if edge.Source == source && edge.Target == target && edge.Kind == kind {
			return
		}
	}
	t.Fatalf("lineage graph missing edge %s -> %s (%s): %#v", source, target, kind, graph.Edges)
}

func assertLineageMissingEdge(t *testing.T, graph assetLineageGraph, source, target, kind string) {
	t.Helper()
	source = testLogicalAssetID(source)
	target = testLogicalAssetID(target)
	for _, edge := range graph.Edges {
		if edge.Source == source && edge.Target == target && edge.Kind == kind {
			t.Fatalf("lineage graph included unwanted edge %s -> %s (%s): %#v", source, target, kind, graph.Edges)
		}
	}
}

func assertTableRelations(t *testing.T, grid recordTable, expected []string) {
	t.Helper()
	if len(expected) == 0 {
		if len(grid.Rows) != 0 {
			t.Fatalf("expected no relations, got rows %#v", grid.Rows)
		}
		return
	}
	got := map[string]struct{}{}
	for _, row := range grid.Rows {
		got[fmt.Sprint(row["relation"])] = struct{}{}
	}
	for _, relation := range expected {
		if _, ok := got[relation]; !ok {
			t.Fatalf("grid missing relation %q: %#v", relation, grid.Rows)
		}
	}
}

func tableHasRelation(grid recordTable, relation string) bool {
	for _, row := range grid.Rows {
		if fmt.Sprint(row["relation"]) == relation {
			return true
		}
	}
	return false
}

func testWorkspaceAssetFixtures() (workspaceview.WorkspaceView, catalog.Catalog, []workspaceview.AssetView, []workspaceview.AssetEdgeView) {
	workspace := workspaceview.WorkspaceView{ID: "leapview", Title: "LeapView Workspace", Description: "Local BI workspace."}
	catalog := catalog.Catalog{Workspace: catalog.Workspace{ID: workspace.ID, Title: workspace.Title, Description: workspace.Description}}
	assets := []workspaceview.AssetView{
		{ID: "catalog:leapview", WorkspaceID: workspace.ID, Type: "catalog", Key: workspace.ID, Title: workspace.Title, Description: workspace.Description},
		{
			ID:          "semantic_model:olist",
			WorkspaceID: workspace.ID,
			Type:        "semantic_model",
			Key:         "olist",
			ParentID:    "catalog:leapview",
			Title:       "Olist Commerce",
			Description: "Brazilian ecommerce model.",
			Payload: map[string]any{
				"DefaultConnection": "olist",
				"Connections": map[string]any{
					"olist": map[string]any{"Kind": "local", "credentials_configured": false, "Defaults": map[string]any{"Options": map[string]any{"header": true}}},
				},
				"Sources": map[string]any{
					"orders": map[string]any{"Connection": "olist", "Format": "csv", "Path": "orders.csv"},
				},
				"Tables": map[string]any{
					"orders": map[string]any{
						"Source":      "orders",
						"PrimaryKey":  "order_id",
						"Description": "One row per order.",
						"Dimensions": map[string]any{
							"state": map[string]any{"Expr": "state"},
						},
					},
				},
				"Measures": map[string]any{
					"revenue": map[string]any{"Table": "orders", "Expression": "SUM(orders.revenue)", "Format": "currency"},
				},
				"Relationships": []any{map[string]any{"ID": "orders_customers", "From": "orders.customer_id", "To": "customers.customer_id", "Cardinality": "many_to_one", "Active": true}},
			},
		},
		{ID: "connection:olist.olist", WorkspaceID: workspace.ID, Type: "connection", Key: "olist.olist", ParentID: "catalog:leapview", Title: "Olist connection", Payload: map[string]any{"Kind": "local", "credentials_configured": false}},
		{ID: "source:olist.orders", WorkspaceID: workspace.ID, Type: "source", Key: "olist.orders", ParentID: "catalog:leapview", Title: "orders", Payload: map[string]any{
			"Connection": "olist",
			"Format":     "csv",
			"Path":       "orders.csv",
			"Fields": map[string]any{
				"order_id":    map[string]any{"Description": "Raw order identifier."},
				"customer_id": map[string]any{"Description": "Raw customer identifier."},
			},
			"Schema": map[string]any{"columns": []any{
				map[string]any{"name": "order_id", "ordinal": float64(1), "physicalType": "VARCHAR", "nullable": false},
				map[string]any{"name": "customer_id", "ordinal": float64(2), "physicalType": "VARCHAR", "nullable": true},
			}},
		}},
		{ID: "source:olist.payments", WorkspaceID: workspace.ID, Type: "source", Key: "olist.payments", ParentID: "catalog:leapview", Title: "payments", Payload: map[string]any{"Connection": "olist", "Format": "csv", "Path": "payments.csv"}},
		{ID: "model_table:olist.orders", WorkspaceID: workspace.ID, Type: "model_table", Key: "olist.orders", ParentID: "catalog:leapview", Title: "orders", Payload: map[string]any{
			"Source":     "orders",
			"PrimaryKey": "order_id",
			"Grain":      "order_id",
			"Dimensions": map[string]any{
				"order_id": map[string]any{"Label": "Order ID"},
				"state":    map[string]any{"Label": "State"},
			},
			"Schema": map[string]any{"columns": []any{
				map[string]any{"name": "order_id", "ordinal": float64(1), "physicalType": "VARCHAR", "nullable": false, "primaryKey": true},
				map[string]any{"name": "state", "ordinal": float64(2), "physicalType": "VARCHAR", "nullable": true},
			}},
		}},
		{ID: "model_table:olist.payments", WorkspaceID: workspace.ID, Type: "model_table", Key: "olist.payments", ParentID: "catalog:leapview", Title: "payments", Payload: map[string]any{
			"Sources":            []any{"payments"},
			"SourceDependencies": []any{"payments"},
			"PrimaryKey":         "order_id",
			"Grain":              "order_id",
			"Transform":          map[string]any{"SQL": "SELECT order_id, SUM(payment_value) AS revenue FROM source.payments GROUP BY order_id"},
			"Dimensions": map[string]any{
				"order_id": map[string]any{"Label": "Order ID"},
				"revenue":  map[string]any{"Label": "Revenue"},
			},
			"Schema": map[string]any{"columns": []any{
				map[string]any{"name": "order_id", "ordinal": float64(1), "physicalType": "VARCHAR", "nullable": false, "primaryKey": true},
				map[string]any{"name": "revenue", "ordinal": float64(2), "physicalType": "DOUBLE", "nullable": true},
			}},
		}},
		{ID: "semantic_table:olist.orders", WorkspaceID: workspace.ID, Type: "semantic_table", Key: "olist.orders", ParentID: "semantic_model:olist", Title: "Orders semantic table", Payload: map[string]any{"Table": "orders"}},
		{ID: "field:olist.orders.state", WorkspaceID: workspace.ID, Type: "field", Key: "olist.orders.state", ParentID: "semantic_table:olist.orders", Title: "State", Payload: map[string]any{"Label": "State"}},
		{ID: "measure:olist.revenue", WorkspaceID: workspace.ID, Type: "measure", Key: "olist.revenue", ParentID: "semantic_model:olist", Title: "Revenue", Payload: map[string]any{"Table": "orders", "Expression": "SUM(orders.revenue)", "Format": "currency"}},
		{ID: "relationship:olist.orders_customers", WorkspaceID: workspace.ID, Type: "relationship", Key: "olist.orders_customers", ParentID: "semantic_model:olist", Title: "Orders to customers", Payload: map[string]any{"From": "orders.customer_id", "To": "customers.customer_id"}},
		{ID: "dashboard:executive-sales", WorkspaceID: workspace.ID, Type: "dashboard", Key: "executive-sales", ParentID: "catalog:leapview", Title: "Executive Sales Dashboard", Description: "Sales overview.", Href: "/dashboards/executive-sales", Payload: map[string]any{"SemanticModel": "olist", "Tags": []any{"sales"}}},
		{ID: "page:executive-sales.overview", WorkspaceID: workspace.ID, Type: "page", Key: "executive-sales.overview", ParentID: "dashboard:executive-sales", Title: "Overview"},
		{ID: "page_item:executive-sales.overview.revenue", WorkspaceID: workspace.ID, Type: "page_item", Key: "executive-sales.overview.revenue", ParentID: "page:executive-sales.overview", Title: "Revenue tile"},
		{ID: "filter:executive-sales.state", WorkspaceID: workspace.ID, Type: "filter", Key: "executive-sales.state", ParentID: "dashboard:executive-sales", Title: "State", Payload: map[string]any{"Field": "orders.state", "Type": "multi_select"}},
		{ID: "visual:executive-sales.revenue", WorkspaceID: workspace.ID, Type: "visual", Key: "executive-sales.revenue", ParentID: "dashboard:executive-sales", Title: "Revenue by month", Payload: map[string]any{"Type": "line"}},
		{ID: "visual:executive-sales.orders", WorkspaceID: workspace.ID, Type: "visual", Key: "executive-sales.orders", ParentID: "dashboard:executive-sales", Title: "Orders", Payload: map[string]any{"Type": "table", "Query": map[string]any{"Table": "orders"}}},
	}
	edges := []workspaceview.AssetEdgeView{
		{ID: "catalog-model", FromAssetID: "catalog:leapview", ToAssetID: "semantic_model:olist", Type: "contains"},
		{ID: "catalog-connection", FromAssetID: "catalog:leapview", ToAssetID: "connection:olist.olist", Type: "contains"},
		{ID: "catalog-source", FromAssetID: "catalog:leapview", ToAssetID: "source:olist.orders", Type: "contains"},
		{ID: "catalog-model-table", FromAssetID: "catalog:leapview", ToAssetID: "model_table:olist.orders", Type: "contains"},
		{ID: "catalog-dashboard", FromAssetID: "catalog:leapview", ToAssetID: "dashboard:executive-sales", Type: "contains"},
		{ID: "model-semantic-table", FromAssetID: "semantic_model:olist", ToAssetID: "semantic_table:olist.orders", Type: "contains"},
		{ID: "model-measure", FromAssetID: "semantic_model:olist", ToAssetID: "measure:olist.revenue", Type: "contains"},
		{ID: "model-relationship", FromAssetID: "semantic_model:olist", ToAssetID: "relationship:olist.orders_customers", Type: "contains"},
		{ID: "semantic-table-field", FromAssetID: "semantic_table:olist.orders", ToAssetID: "field:olist.orders.state", Type: "contains"},
		{ID: "table-source", FromAssetID: "model_table:olist.orders", ToAssetID: "source:olist.orders", Type: "reads_source"},
		{ID: "source-connection", FromAssetID: "source:olist.orders", ToAssetID: "connection:olist.olist", Type: "uses_connection"},
		{ID: "semantic-table-model-table", FromAssetID: "semantic_table:olist.orders", ToAssetID: "model_table:olist.orders", Type: "uses_model_table"},
		{ID: "measure-semantic-table", FromAssetID: "measure:olist.revenue", ToAssetID: "semantic_table:olist.orders", Type: "uses_semantic_table"},
		{ID: "measure-field", FromAssetID: "measure:olist.revenue", ToAssetID: "field:olist.orders.state", Type: "uses_field"},
		{ID: "dashboard-model", FromAssetID: "dashboard:executive-sales", ToAssetID: "semantic_model:olist", Type: "uses_semantic_model"},
		{ID: "dashboard-page", FromAssetID: "dashboard:executive-sales", ToAssetID: "page:executive-sales.overview", Type: "contains"},
		{ID: "dashboard-filter", FromAssetID: "dashboard:executive-sales", ToAssetID: "filter:executive-sales.state", Type: "contains"},
		{ID: "dashboard-visual", FromAssetID: "dashboard:executive-sales", ToAssetID: "visual:executive-sales.revenue", Type: "contains"},
		{ID: "dashboard-table", FromAssetID: "dashboard:executive-sales", ToAssetID: "visual:executive-sales.orders", Type: "contains"},
		{ID: "page-item-edge", FromAssetID: "page:executive-sales.overview", ToAssetID: "page_item:executive-sales.overview.revenue", Type: "contains"},
		{ID: "page-item-visual", FromAssetID: "page_item:executive-sales.overview.revenue", ToAssetID: "visual:executive-sales.revenue", Type: "uses_visual"},
		{ID: "page-item-table", FromAssetID: "page_item:executive-sales.overview.revenue", ToAssetID: "visual:executive-sales.orders", Type: "uses_visual"},
		{ID: "page-item-filter", FromAssetID: "page_item:executive-sales.overview.revenue", ToAssetID: "filter:executive-sales.state", Type: "uses_filter"},
		{ID: "visual-measure", FromAssetID: "visual:executive-sales.revenue", ToAssetID: "measure:olist.revenue", Type: "uses_measure"},
		{ID: "visual-field", FromAssetID: "visual:executive-sales.revenue", ToAssetID: "field:olist.orders.state", Type: "uses_field"},
		{ID: "table-semantic-table", FromAssetID: "visual:executive-sales.orders", ToAssetID: "semantic_table:olist.orders", Type: "uses_semantic_table"},
		{ID: "table-field", FromAssetID: "visual:executive-sales.orders", ToAssetID: "field:olist.orders.state", Type: "uses_field"},
		{ID: "filter-field", FromAssetID: "filter:executive-sales.state", ToAssetID: "field:olist.orders.state", Type: "filters_field"},
	}
	return workspace, catalog, assets, edges
}
