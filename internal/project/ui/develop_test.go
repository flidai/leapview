package ui

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestSemanticModelDetailProjectionRendersDatasetsMetricsRelationshipsAndGraph(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order_line": {Type: "primary", Fields: []string{"order_id", "line_number"}},
					"customer":   {Type: "foreign", Fields: []string{"customer_id"}},
				},
				GrainEntity: "order_line",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id":    {Label: "Order ID"},
					"line_number": {Label: "Line number"},
					"customer_id": {Label: "Customer ID"},
					"status":      {Label: "Status"},
				},
			},
			"customers": {ModelName: "customers", Entities: map[string]semanticmodel.EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Dataset: "orders", Aggregation: "count_distinct", Label: "Orders", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{
			"orders_customer": {From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Fields: []string{"customer_id"}}, To: semanticmodel.RelationshipEndpointSpec{Dataset: "customers", Fields: []string{"customer_id"}}},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customer", FromDataset: "orders", FromFields: []string{"customer_id"},
			ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	asset := projectview.DevelopAssetView{
		ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales",
		Payload: projectview.SemanticModelAssetPayload(model, compiled),
	}
	project := projectview.DevelopView{ID: "project:test", Title: "Test"}
	details := projectAssetDetailsSignal(project, asset, []projectview.DevelopAssetView{asset}, nil)
	if len(details.Sections) != 3 {
		t.Fatalf("detail sections = %d, want datasets/metrics/relationships", len(details.Sections))
	}
	for _, want := range []string{"Datasets (2)", "Metrics (1)", "Relationships (1)"} {
		found := false
		for _, section := range details.Sections {
			if section.Title == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("detail sections = %#v, missing %q", details.Sections, want)
		}
	}
	if details.SemanticModelGraph == nil || len(details.SemanticModelGraph.Nodes) != 2 || len(details.SemanticModelGraph.Edges) != 1 {
		t.Fatalf("semantic graph = %#v, want two nodes and one edge", details.SemanticModelGraph)
	}
	var ordersNode *uisignals.SemanticModelGraphNodeSignal
	for index := range details.SemanticModelGraph.Nodes {
		if details.SemanticModelGraph.Nodes[index].ID == "orders" {
			ordersNode = &details.SemanticModelGraph.Nodes[index]
			break
		}
	}
	if ordersNode == nil || ordersNode.GrainEntity == nil || *ordersNode.GrainEntity != "order_line" {
		t.Fatalf("orders graph node = %#v, want order_line grain", ordersNode)
	}
	if ordersNode.Entities == nil || len(*ordersNode.Entities) != 2 {
		t.Fatalf("orders graph entities = %#v, want primary and foreign entities", ordersNode.Entities)
	}
	if got := (*ordersNode.Entities)[1]; got.Name != "order_line" || got.Type != "primary" || !slices.Equal(got.Fields, []string{"order_id", "line_number"}) || got.Grain == nil || !*got.Grain {
		t.Fatalf("orders graph grain entity = %#v, want ordered composite order_line", got)
	}
	grainFields := []string{}
	for _, field := range ordersNode.Fields {
		if field.Grain != nil && *field.Grain {
			grainFields = append(grainFields, field.Name)
		}
	}
	if !slices.Equal(grainFields, []string{"line_number", "order_id"}) {
		t.Fatalf("orders graph grain fields = %v, want composite grain fields", grainFields)
	}
	metricTable := semanticMetricsTable(project.ID, asset, []projectview.DevelopAssetView{asset}, asset.Payload)
	if len(metricTable.Rows) != 1 {
		t.Fatalf("metric rows = %#v, want one row", metricTable.Rows)
	}
	metricRow := metricTable.Rows[0]
	if metricRow["dataset"] != "orders" || metricRow["input"] != "orders.order_id" {
		t.Fatalf("metric row = %#v, want canonical dataset and input", metricRow)
	}
	if aggregation := metricRow["aggregation"].(recordTableBadge).Label; aggregation != "count_distinct" {
		t.Fatalf("metric aggregation = %#v, want count_distinct", aggregation)
	}

	bootstrap := ProjectAssetBootstrapSignalsForEnvironment(catalog.Catalog{}, project, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetRefreshState{}, AssetVersionsState{})
	encoded, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Datasets (2)", "Metrics (1)", "Relationships (1)"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("bootstrap JSON = %s, missing %q", encoded, want)
		}
	}
	var rendered bytes.Buffer
	if err := ProjectAssetPageWithRefreshAndVersionsForEnvironment(catalog.Catalog{}, project, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetRefreshState{}, AssetVersionsState{}, "").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	dom := rendered.String()
	if !strings.Contains(dom, "<lv-project-asset-page") || !strings.Contains(dom, "/static/semantic-model-graph.js") {
		t.Fatalf("semantic-model detail DOM missing route root or graph asset: %s", dom)
	}
}

func TestSemanticModelGraphProjectsCompiledEntityAndCompositeEndpoints(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order_line": {Type: "primary", Fields: []string{"order_id", "line_number"}},
					"customer":   {Type: "foreign", Fields: []string{"customer_id", "customer_region"}},
				},
				GrainEntity: "order_line",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"}, "line_number": {Label: "Line number"},
					"customer_id": {Label: "Customer ID"}, "customer_region": {Label: "Customer region"},
				},
			},
			"customers": {
				ModelName:   "customers",
				Entities:    map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id", "customer_region"}}},
				GrainEntity: "customer",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Label: "Customer ID"}, "customer_region": {Label: "Customer region"},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{
			"orders_customers": {
				From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Entity: "customer"},
				To:   semanticmodel.RelationshipEndpointSpec{Dataset: "customers", Entity: "customer"},
			},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id", "customer_region"},
			ToDataset: "customers", ToFields: []string{"customer_id", "customer_region"}, Cardinality: "one_to_one",
		}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	asset := projectview.DevelopAssetView{
		ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales",
		Payload: projectview.SemanticModelAssetPayload(model, compiled),
	}
	details := projectAssetDetailsSignal(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil)
	if details.SemanticModelGraph == nil || len(details.SemanticModelGraph.Edges) != 1 {
		t.Fatalf("semantic graph = %#v, want one composite edge", details.SemanticModelGraph)
	}
	edge := details.SemanticModelGraph.Edges[0]
	if edge.SourceField != "customer_id, customer_region" || edge.TargetField != "customer_id, customer_region" {
		t.Fatalf("composite edge fields = (%q, %q), want ordered physical tuple", edge.SourceField, edge.TargetField)
	}
	if edge.Cardinality != "one_to_one" || edge.Label != "1:1" {
		t.Fatalf("composite edge cardinality = (%q, %q), want inferred safe marker", edge.Cardinality, edge.Label)
	}
	var orders *uisignals.SemanticModelGraphNodeSignal
	for index := range details.SemanticModelGraph.Nodes {
		if details.SemanticModelGraph.Nodes[index].ID == "orders" {
			orders = &details.SemanticModelGraph.Nodes[index]
			break
		}
	}
	if orders == nil {
		t.Fatal("orders graph node missing")
	}
	for _, field := range orders.Fields {
		if field.Name == "customer_id" || field.Name == "customer_region" {
			if field.Join == nil || !*field.Join {
				t.Fatalf("orders field %q = %#v, want join handle", field.Name, field)
			}
		}
	}
}

func TestModelAndSemanticDataTabsStayOnAssetRoutes(t *testing.T) {
	project := projectview.DevelopView{ID: "project:test", Title: "Test"}
	for _, test := range []struct {
		asset projectview.DevelopAssetView
		href  string
	}{
		{asset: projectview.DevelopAssetView{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders"}, href: "/models/model:orders/data"},
		{asset: projectview.DevelopAssetView{ID: "semantic-model:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales"}, href: "/semantic-models/semantic-model:sales/data"},
	} {
		page := projectAssetPageSignal(project, test.asset, []projectview.DevelopAssetView{test.asset}, nil, "data", assetLineageModel{})
		var dataTab *uisignals.ResourceTabSignal
		for index := range page.Tabs {
			if page.Tabs[index].ID == "data" {
				dataTab = &page.Tabs[index]
				break
			}
		}
		if dataTab == nil || dataTab.Href != test.href || !dataTab.Active {
			t.Fatalf("asset %s data tab = %#v, want active %s", test.asset.ID, dataTab, test.href)
		}
	}
	source := projectview.DevelopAssetView{ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders"}
	page := projectAssetPageSignal(project, source, []projectview.DevelopAssetView{source}, nil, "details", assetLineageModel{})
	for _, tab := range page.Tabs {
		if tab.ID == "data" {
			t.Fatalf("source unexpectedly exposes unsupported data tab: %#v", tab)
		}
	}
}

func TestModelTableDetailProjectionRendersCompiledDefinition(t *testing.T) {
	const configuration = "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:zip_geolocations, name: zip_geolocations}\n"
	table := semanticmodel.Table{
		Execution:          semanticmodel.ExecutionDefinition{SQL: "SELECT zip_prefix FROM source.\"olist.geolocation\""},
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix", Description: "ZIP code prefix"}},
		Entities:           map[string]semanticmodel.EntityDefinition{"zip_prefix": {Type: "primary", Fields: []string{"zip_prefix"}}},
		GrainEntity:        "zip_prefix",
		SourceDependencies: []string{"olist.geolocation"},
		Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
			{Name: "zip_prefix", Ordinal: 0, PhysicalType: "VARCHAR"},
			{Name: "observation_count", Ordinal: 1, PhysicalType: "BIGINT"},
		}},
	}
	asset := projectview.DevelopAssetView{
		ID: "model:zip_geolocations", Type: string(projectview.AssetTypeModelTable), Key: "zip_geolocations", Title: "ZIP locations",
		Payload: projectview.ModelTableAssetPayloadWithAuthoredSource(table, nil, configuration),
	}
	asset.Payload["Physical"] = map[string]any{
		"RowCount": int64(99_441), "ColumnCount": int64(5), "FileCount": int64(2),
		"SizeBytes": int64(1_572_864), "SnapshotID": int64(17),
	}
	project := projectview.DevelopView{ID: "project:test", Title: "Test"}
	details := projectAssetDetailsSignal(project, asset, []projectview.DevelopAssetView{asset}, nil)
	if got := factValue(details.Overview, "Fields"); got != "2" {
		t.Fatalf("fields fact = %q, want 2", got)
	}
	if got := factValue(details.Overview, "Documented fields"); got != "1" {
		t.Fatalf("documented fields fact = %q, want 1", got)
	}
	if got := factValue(details.Overview, "Contracted fields"); got != "0" {
		t.Fatalf("contracted fields fact = %q, want 0", got)
	}
	if got := factValue(details.Overview, "Input sources"); got != "1" {
		t.Fatalf("input sources fact = %q, want 1", got)
	}
	if got := factValue(details.Overview, "Mode"); got != "Definition" {
		t.Fatalf("mode fact = %q, want Definition", got)
	}
	if got := factValue(details.Overview, "Rows"); got != "99,441" {
		t.Fatalf("rows fact = %q, want 99,441", got)
	}
	if got := factValue(details.Overview, "Physical size"); got != "1.5 MiB" {
		t.Fatalf("physical size fact = %q, want 1.5 MiB", got)
	}
	if got := factValue(details.Overview, "Data files"); got != "2" {
		t.Fatalf("data files fact = %q, want 2", got)
	}
	if got := factValue(details.Overview, "DuckLake snapshot"); got != "17" {
		t.Fatalf("snapshot fact = %q, want 17", got)
	}
	if len(details.Sections) != 2 || details.Sections[0].Title != "Entities (1)" || details.Sections[1].Title != "Fields (2)" {
		t.Fatalf("detail sections = %#v, want entities and fields only", details.Sections)
	}
	if len(details.Sections[0].Table.Rows) != 1 || details.Sections[0].Table.Rows[0]["name"] != "zip_prefix" || details.Sections[0].Table.Rows[0]["grain"] != "Yes" {
		t.Fatalf("entity rows = %#v, want grain zip_prefix entity", details.Sections[0].Table.Rows)
	}
	definition := projectAssetDefinitionSignal(asset)
	if len(definition.Sections) != 2 {
		t.Fatalf("definition sections = %#v, want configuration and SQL", definition.Sections)
	}
	if uisignals.ValueOrZero(definition.Sections[0].Code) != configuration || uisignals.ValueOrZero(definition.Sections[0].Lang) != "yaml" {
		t.Fatalf("configuration section = %#v, want authored YAML", definition.Sections[0])
	}
	if uisignals.ValueOrZero(definition.Sections[1].Code) != table.Execution.SQL || uisignals.ValueOrZero(definition.Sections[1].Lang) != "sql" {
		t.Fatalf("SQL section = %#v, want compiled transform SQL", definition.Sections[1])
	}
	if len(details.Sections[1].Table.Rows) != 2 {
		t.Fatalf("field rows = %#v, want two rows", details.Sections[1].Table.Rows)
	}
	columns := details.Sections[1].Table.Columns
	if len(columns) != 4 || columns[0].ID != "field" || columns[1].ID != "type" || columns[2].ID != "description" || columns[3].ID != "status" {
		t.Fatalf("field columns = %#v, want compact catalog columns", columns)
	}
	row := details.Sections[1].Table.Rows[0]
	field := asMap(row["field"])
	if got := metaString(field, "label"); got != "zip_prefix" {
		t.Fatalf("field label = %q", got)
	}
	if got := metaString(field, "description"); got != "ZIP prefix" {
		t.Fatalf("field display label = %q", got)
	}
	if got := metaString(field, "href"); got != "/models/model:zip_geolocations/details?field=zip_prefix" {
		t.Fatalf("field drawer href = %q", got)
	}
	if got := row["status"].(recordTableBadge).Label; got != "Documented" {
		t.Fatalf("documented field status = %q", got)
	}
	for key, want := range map[string]any{
		"fieldKey": "zip_prefix", "label": "ZIP prefix", "logicalType": "String", "physicalType": "VARCHAR",
		"nullable": "Not profiled", "metadataStatus": "Documented", "entities": "zip_prefix", "grain": "Yes",
		"description": "ZIP code prefix", "duckLakeSnapshot": "17",
	} {
		if got := row[key]; got != want {
			t.Fatalf("field drawer %s = %#v, want %#v", key, got, want)
		}
	}
	if got := details.Sections[1].Table.Rows[1]["status"].(recordTableBadge).Label; got != "Observed" {
		t.Fatalf("observed field status = %q", got)
	}
}

func TestAuthoredAssetsExposeDefinitionTabWithStableSectionHref(t *testing.T) {
	for _, asset := range []projectview.DevelopAssetView{
		{ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders", Payload: map[string]any{"Configuration": "kind: Source\n"}},
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Payload: map[string]any{"Configuration": "kind: Model\n"}},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Payload: map[string]any{"Configuration": "kind: SemanticModel\n"}},
		{ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales", Payload: map[string]any{"Configuration": "kind: Dashboard\n"}},
		{ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Payload: map[string]any{"Configuration": "kind: Pipeline\n"}},
	} {
		page := projectAssetPageSignal(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "definition", assetLineageModel{})
		if page.ActiveSection != "definition" || page.Definition == nil {
			t.Fatalf("%s page = %#v, want active definition payload", asset.Type, page)
		}
		found := false
		for _, tab := range page.Tabs {
			if tab.ID == "definition" {
				found = tab.Active && strings.HasSuffix(tab.Href, "/definition")
			}
		}
		if !found {
			t.Fatalf("%s tabs = %#v, want active stable definition tab", asset.Type, page.Tabs)
		}
		if asset.Type == string(projectview.AssetTypeRefreshPipeline) {
			for _, action := range uisignals.ValueOrZero(page.Actions) {
				if action.Label == "Run now" {
					t.Fatalf("pipeline definition actions = %#v, refresh action requires operational state", uisignals.ValueOrZero(page.Actions))
				}
			}
		}
	}
	connection := projectview.DevelopAssetView{ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Payload: map[string]any{"Configuration": "kind: Connection\n"}}
	page := connectionAssetPageSignal(projectview.DevelopView{ID: "project:test"}, connection, []projectview.DevelopAssetView{connection}, nil, "definition", assetLineageModel{})
	if page.ActiveSection != "definition" || page.Definition == nil {
		t.Fatalf("connection page = %#v, want active redacted definition payload", page)
	}
	if len(page.Tabs) < 2 || page.Tabs[1].ID != "definition" || !page.Tabs[1].Active || !strings.HasSuffix(page.Tabs[1].Href, "/definition") {
		t.Fatalf("connection tabs = %#v, want active stable definition tab", page.Tabs)
	}
}

func TestConnectionAssetBootstrapIncludesInjectedApplicationChrome(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Title: "Warehouse"}
	patch := ConnectionAssetBootstrapSignalsForEnvironment(catalog.Catalog{}, projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetVersionsState{}, testLayoutProvider())
	if _, ok := patch["chrome"]; !ok {
		t.Fatalf("bootstrap signals = %#v, want injected application chrome", patch)
	}
}

func factValue(facts []uisignals.DefinitionFactSignal, label string) string {
	for _, fact := range facts {
		if fact.Label == label {
			return fact.Value
		}
	}
	return ""
}

func TestDevelopCatalogUsesStableDashboardLinksWithoutProjectPicker(t *testing.T) {
	page := catalogPageSignal(catalog.Catalog{
		Project:    catalog.Project{ID: "sales", Title: "Sales"},
		Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive", Appearance: dashboardappearance.Value{Icon: "house", Color: "orange"}}},
	}, "")
	if len(page.Dashboards) != 1 || page.Dashboards[0].Href != "/dashboards/executive" {
		t.Fatalf("dashboard link = %#v, want stable dashboard route", page.Dashboards)
	}
	if page.Dashboards[0].AppearanceIcon != "house" || page.Dashboards[0].AppearanceColor != "orange" {
		t.Fatalf("dashboard appearance = %#v", page.Dashboards[0])
	}
}

func TestDashboardDetailOwnsAppearanceSignalAndTypedCommand(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales", Title: "Sales"}
	projectCatalog := catalog.Catalog{Project: catalog.Project{ID: "project:test"}, Dashboards: []catalog.Dashboard{{
		ID: asset.ID, Appearance: dashboardappearance.Value{Icon: "house", Color: "orange"}, AppearanceRevision: 4,
	}}}
	page := projectAssetPageSignal(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", assetLineageModel{})
	attachDashboardAppearance(&page, projectCatalog, asset)
	if page.DashboardAppearance == nil || page.DashboardAppearance.Icon != "house" || page.DashboardAppearance.Color != "orange" || page.DashboardAppearance.Revision != 4 {
		t.Fatalf("dashboard appearance signal = %#v", page.DashboardAppearance)
	}
	var rendered bytes.Buffer
	if err := ProjectAssetPageWithRefreshAndVersionsForEnvironment(projectCatalog, projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetRefreshState{}, AssetVersionsState{}, "csrf-test").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	document := rendered.String()
	if !strings.Contains(document, "lv-dashboard-appearance-change") || !strings.Contains(document, "updateDashboardAppearance") || !strings.Contains(document, "csrf-test") {
		t.Fatalf("dashboard appearance command bridge is missing: %s", document)
	}
}

func TestDashboardDetailUsesCompiledDefinitionAndShowsPublications(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales", Title: "Sales",
		Payload: map[string]any{
			"SemanticModel":     "semantic:sales",
			"Pages":             []any{map[string]any{"ID": "overview", "Title": "Overview", "Description": "Summary"}},
			"FilterDefinitions": map[string]any{"region": map[string]any{"Label": "Region", "Field": "region"}},
			"Visualizations":    map[string]any{"revenue": map[string]any{"RendererID": "echarts", "Query": map[string]any{"Aggregate": map[string]any{"Metrics": []any{"revenue"}, "Dimensions": []any{"region"}}}}},
			"Publications":      []map[string]any{{"Name": "publication:website", "Dashboard": "dashboard:sales", "DefaultPage": "overview", "AllowedOrigins": []any{"https://example.test"}, "ConfigurationDigest": "sha256:abc"}},
		},
	}
	details := projectAssetDetailsSignal(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil)
	if got := factValue(details.Overview, "Semantic model"); got != "semantic:sales" {
		t.Fatalf("semantic model fact = %q, want semantic:sales", got)
	}
	for _, want := range []string{"Pages (1)", "Filters (1)", "Visuals (1)", "Publications (1)"} {
		found := false
		for _, section := range details.Sections {
			if section.Title == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("dashboard sections = %#v, missing %q", details.Sections, want)
		}
	}
	publicationTable := details.Sections[3].Table
	if len(publicationTable.Rows) != 1 || publicationTable.Rows[0]["publication"] != "publication:website" || publicationTable.Rows[0]["default_page"] != "overview" {
		t.Fatalf("publication rows = %#v, want configured publication", publicationTable.Rows)
	}
	pagesTable := details.Sections[0].Table
	if pagesTable.Rows[0]["pageHref"] != nil || pagesTable.Columns[0].Kind != nil || pagesTable.Columns[0].HrefKey != nil {
		t.Fatalf("synthetic page row remains clickable: columns=%#v row=%#v", pagesTable.Columns, pagesTable.Rows[0])
	}
	if got := pagesTable.Rows[0]["runtimeHref"]; got != "/dashboards/dashboard:sales/pages/overview" {
		t.Fatalf("dashboard runtime href = %q, want stable dashboard ID route", got)
	}
	for _, section := range details.Sections[1:3] {
		if section.Table.Columns[0].Kind != nil || section.Table.Columns[0].HrefKey != nil {
			t.Fatalf("synthetic dashboard child remains clickable in %q: %#v", section.Title, section.Table.Columns[0])
		}
	}
}

func TestExplicitJSONConfigurationIsPrettyPrintedAndLabeledJSON(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders",
		Payload: map[string]any{"Configuration": `{"kind":"Source","metadata":{"id":"source:orders"}}`},
	}
	definition := projectAssetDefinitionSignal(asset)
	if len(definition.Sections) != 1 {
		t.Fatalf("definition sections = %#v, want configuration", definition.Sections)
	}
	section := definition.Sections[0]
	if got := uisignals.ValueOrZero(section.Lang); got != "json" {
		t.Fatalf("configuration language = %q, want json", got)
	}
	code := uisignals.ValueOrZero(section.Code)
	if !strings.Contains(code, "\n  \"kind\": \"Source\"") || !strings.HasSuffix(code, "\n") {
		t.Fatalf("configuration code = %q, want pretty-printed JSON", code)
	}
}

func TestDashboardYAMLConfigurationRemainsYAML(t *testing.T) {
	const configuration = "apiVersion: leapview.dev/v1\nkind: Dashboard\nspec:\n  semanticModel: sales\n"
	asset := projectview.DevelopAssetView{
		ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales",
		Payload: map[string]any{"Configuration": configuration},
	}
	definition := projectAssetDefinitionSignal(asset)
	if len(definition.Sections) != 1 {
		t.Fatalf("definition sections = %#v, want configuration", definition.Sections)
	}
	section := definition.Sections[0]
	if got := uisignals.ValueOrZero(section.Lang); got != "yaml" {
		t.Fatalf("configuration language = %q, want yaml", got)
	}
	if got := uisignals.ValueOrZero(section.Code); got != configuration {
		t.Fatalf("configuration code = %q, want canonical YAML", got)
	}
}

func TestDashboardVersionsSectionIsReachableWhenHistoryExists(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "dashboard:sales", Type: string(projectview.AssetTypeDashboard), Key: "sales", Title: "Sales"}
	versions := AssetVersionsState{CurrentContentHash: "sha256:current", Versions: []AssetVersionState{{ContentHash: "sha256:current"}}}
	page := projectAssetPageSignalWithRefreshAndVersions(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "versions", assetLineageModel{}, AssetRefreshState{}, versions)
	if page.ActiveSection != "versions" || page.Versions == nil {
		t.Fatalf("dashboard versions page = %#v, want active versions payload", page)
	}
	found := false
	for _, tab := range page.Tabs {
		if tab.ID == "versions" {
			found = tab.Active && strings.HasSuffix(tab.Href, "/versions")
		}
	}
	if !found {
		t.Fatalf("dashboard tabs = %#v, want active Versions tab", page.Tabs)
	}
}

func TestSourceAndPipelineDetailsConsumeTypedAssetProjections(t *testing.T) {
	source := projectview.DevelopAssetView{
		ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders", Title: "Orders",
		Payload: projectview.SourceAssetPayload(semanticmodel.Source{
			Format: "csv", Connection: "warehouse", Path: "s3://bucket/orders.csv", SchemaMode: "compatible",
			Fields: map[string]semanticmodel.SourceField{"order_id": {Type: "int", Description: "Order identifier"}},
			Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
				{Name: "review_id", Ordinal: 0, PhysicalType: "VARCHAR"},
				{Name: "order_id", Ordinal: 1, PhysicalType: "BIGINT"},
			}},
		}),
	}
	source.Payload["SchemaObservation"] = map[string]any{
		"Status": "success", "ObservedAt": "2026-08-24T07:30:00Z", "SchemaDigest": "sha256:observed",
	}
	sourceDetails := assetDetailModelForAsset(projectview.DevelopView{ID: "project:test"}, source, []projectview.DevelopAssetView{source}, nil)
	if got := detailFactValue(sourceDetails.Overview, "Connection"); got != "warehouse" {
		t.Fatalf("source connection fact = %q, want warehouse", got)
	}
	if got := detailFactValue(sourceDetails.Overview, "Schema mode"); got != "compatible" {
		t.Fatalf("source schema mode = %q, want compatible", got)
	}
	if got := detailFactValue(sourceDetails.Overview, "Schema status"); got != "success" {
		t.Fatalf("source schema status = %q, want success", got)
	}
	if got := detailFactValue(sourceDetails.Overview, "Observed fields"); got != "2" {
		t.Fatalf("source observed fields = %q, want 2", got)
	}
	if got := detailFactValue(sourceDetails.Overview, "Contract fields"); got != "1" {
		t.Fatalf("source contract fields = %q, want 1", got)
	}
	if len(sourceDetails.Sections) != 1 || len(sourceDetails.Sections[0].Table.Rows) != 2 {
		t.Fatalf("source field section = %#v, want two observed fields", sourceDetails.Sections)
	}
	if sourceDetails.Sections[0].Table.Rows[0]["contract"].(recordTableBadge).Label != "Observed only" || sourceDetails.Sections[0].Table.Rows[1]["contract"].(recordTableBadge).Label != "Declared" {
		t.Fatalf("source contract badges = %#v, want observed-only then declared", sourceDetails.Sections[0].Table.Rows)
	}

	pipeline := projectview.DevelopAssetView{
		ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Title: "Sales refresh",
		Payload: projectview.RefreshPipelineAssetPayload(refreshschedule.Definition{
			ID: "pipeline:sales", Name: "sales", SemanticModelID: projectgraph.ResourceID("semantic:sales"),
			Timezone: "UTC", ConcurrencyPolicy: refreshschedule.ConcurrencyForbid,
			Schedules: []refreshschedule.Schedule{{Expression: "0 * * * *"}},
		}),
	}
	pipelineDetails := assetDetailModelForAsset(projectview.DevelopView{ID: "project:test"}, pipeline, []projectview.DevelopAssetView{pipeline}, nil)
	if got := detailFactValue(pipelineDetails.Overview, "Semantic model"); got != "semantic:sales" {
		t.Fatalf("pipeline semantic model fact = %q, want semantic:sales", got)
	}
	if got := detailFactValue(pipelineDetails.Overview, "Schedule"); !strings.Contains(got, "0 * * * *") || !strings.Contains(got, "UTC") {
		t.Fatalf("pipeline schedule fact = %q, want cron and timezone", got)
	}
}

func TestSourceListPreservesAuthorizedConnectionContext(t *testing.T) {
	source := projectview.DevelopAssetView{ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders", Title: "Orders"}
	connection := projectview.DevelopAssetView{ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Title: "Warehouse"}
	edges := []projectview.DevelopEdgeView{{FromAssetID: source.ID, ToAssetID: connection.ID, Type: string(projectview.AssetEdgeUsesConnection)}}
	page := projectPageSignalWithContext(projectview.DevelopView{ID: "project:test"}, []projectview.DevelopAssetView{source}, []projectview.DevelopAssetView{source, connection}, edges, "sources", string(projectview.AssetTypeSource), "", "")
	item := (*page.AssetList).Assets[0]
	if uisignals.ValueOrZero(item.ParentTitle) != "Warehouse" || uisignals.ValueOrZero(item.ParentHref) != "/connections/connection:warehouse/details" {
		t.Fatalf("source summary = %#v, want authorized parent connection context", item)
	}
}

func TestSourceAndModelSchemaUseLogicalFallbacksAndExplicitUnknowns(t *testing.T) {
	fields := map[string]any{
		"physical": map[string]any{"Datatype": "logical_text", "Nullable": false},
		"logical":  map[string]any{"Datatype": "integer", "Nullable": false},
		"unknown":  map[string]any{},
	}
	schema := map[string]any{"Columns": []any{
		map[string]any{"Name": "physical", "Ordinal": 0, "PhysicalType": "VARCHAR", "Nullable": true},
		map[string]any{"Name": "logical", "Ordinal": 1},
		map[string]any{"Name": "unknown", "Ordinal": 2},
	}}
	for name, table := range map[string]recordTable{
		"source": sourceFieldsGrid(fields, schema),
		"model": modelTableFieldsGrid(
			projectview.DevelopAssetView{ID: "model:table", Type: string(projectview.AssetTypeModelTable)},
			map[string]any{"Dimensions": fields, "Schema": schema},
		),
	} {
		rows := map[string]map[string]any{}
		for _, row := range table.Rows {
			rows[row["name"].(string)] = row
		}
		if got := rows["physical"]["physical_type"].(recordTableBadge).Label; got != "VARCHAR" {
			t.Fatalf("%s physical type = %q, want physical-over-logical VARCHAR", name, got)
		}
		if got := rows["physical"]["nullable"]; got != "Yes" {
			t.Fatalf("%s physical nullable = %q, want schema-over-logical Yes", name, got)
		}
		logicalKey, wantLogical, wantUnknown := "physical_type", "integer", "Not profiled"
		if name == "model" {
			logicalKey, wantLogical, wantUnknown = "logical_type", "integer", "Opaque"
			if got := rows["logical"]["physical_type"].(recordTableBadge).Label; got != "Not observed" {
				t.Fatalf("model physical type = %q, want Not observed", got)
			}
		}
		if got := rows["logical"][logicalKey].(recordTableBadge).Label; got != wantLogical {
			t.Fatalf("%s logical type = %q, want %s", name, got, wantLogical)
		}
		if got := rows["logical"]["nullable"]; got != "No" {
			t.Fatalf("%s logical nullable = %q, want explicit No", name, got)
		}
		if got := rows["unknown"][logicalKey].(recordTableBadge).Label; got != wantUnknown {
			t.Fatalf("%s unknown type = %q, want %s", name, got, wantUnknown)
		}
		if got := rows["unknown"]["nullable"]; got != "Not profiled" {
			t.Fatalf("%s unknown nullable = %q, want Not profiled", name, got)
		}
	}
}

func TestUnavailablePipelineExplainsRecoveryWithoutUnrelatedActions(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Title: "Sales refresh"}
	page := projectAssetPageSignalWithRefresh(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", assetLineageModel{}, AssetRefreshState{Unavailable: true})
	if page.Details == nil || factValue(page.Details.Overview, "Refresh status") != "unavailable" || !strings.Contains(factValue(page.Details.Overview, "Refresh guidance"), "refresh runtime") {
		t.Fatalf("pipeline details = %#v, want unavailable status and recovery guidance", page.Details)
	}
	actions := uisignals.ValueOrZero(page.Actions)
	if len(actions) < 2 || actions[0].Disabled == nil || !*actions[0].Disabled || !strings.Contains(actions[0].Label, "unavailable") {
		t.Fatalf("pipeline actions = %#v, want explanatory disabled Run now", actions)
	}
	for _, action := range actions {
		if uisignals.ValueOrZero(action.Href) == "/connections" {
			t.Fatalf("pipeline actions = %#v, must not infer a connection failure", actions)
		}
	}
}

func TestReadOnlyPipelineDetailDisablesRunWithoutUnavailableGuidance(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Title: "Sales refresh"}
	page := projectAssetPageSignalWithRefresh(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", assetLineageModel{}, AssetRefreshState{CanRun: false})
	actions := uisignals.ValueOrZero(page.Actions)
	if len(actions) == 0 || actions[0].Label != "Run now" || actions[0].Disabled == nil || !*actions[0].Disabled {
		t.Fatalf("read-only pipeline actions = %#v, want disabled Run now", actions)
	}
	if len(actions) > 1 && actions[1].Label == "Review connections" {
		t.Fatalf("read-only pipeline actions = %#v, must not claim unavailable refresh infrastructure", actions)
	}
}

func TestPipelineDetailUsesCanonicalPipelineCommandBridge(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Title: "Sales refresh"}
	refresh := AssetRefreshState{CanRun: true, RunCommand: refreshgen.GenUIActionCreateRefreshRun()}
	var rendered bytes.Buffer
	if err := ProjectAssetPageWithRefreshAndVersionsForEnvironment(catalog.Catalog{}, projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", refresh, AssetVersionsState{}, "").Render(&rendered); err != nil {
		t.Fatal(err)
	}
	dom := rendered.String()
	if !strings.Contains(dom, `data-on:lv-run-refresh-pipeline=`) || !strings.Contains(dom, "/pipelines/command?surface=asset") || !strings.Contains(dom, "asset=pipeline%3Asales") || !strings.Contains(dom, "pipelineCommand") {
		t.Fatalf("pipeline detail command bridge = %s, want typed pipelineCommand POST", dom)
	}
	if strings.Contains(dom, "/pipelines/pipeline:sales/refresh") {
		t.Fatalf("pipeline detail still posts to removed refresh route: %s", dom)
	}
	bootstrap := ProjectAssetBootstrapSignalsForEnvironment(catalog.Catalog{}, projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", refresh, AssetVersionsState{})
	if _, ok := bootstrap["pipelineCommand"]; !ok {
		t.Fatalf("pipeline detail bootstrap = %#v, missing pipelineCommand signal", bootstrap)
	}
	if _, ok := bootstrap["pipelineCommandStatus"]; !ok {
		t.Fatalf("pipeline detail bootstrap = %#v, missing pipelineCommandStatus signal", bootstrap)
	}
}

func TestProjectAssetSectionsAreResourceAware(t *testing.T) {
	for _, test := range []struct {
		assetType string
		section   string
		want      bool
	}{
		{string(projectview.AssetTypeSource), "details", true},
		{string(projectview.AssetTypeSource), "definition", true},
		{string(projectview.AssetTypeSource), "data", false},
		{string(projectview.AssetTypeModelTable), "data", true},
		{string(projectview.AssetTypeRefreshPipeline), "refreshes", true},
		{string(projectview.AssetTypeDashboard), "refreshes", false},
		{string(projectview.AssetTypeDashboard), "bogus", false},
	} {
		if got := ValidProjectAssetSection(test.assetType, test.section); got != test.want {
			t.Fatalf("ValidProjectAssetSection(%q, %q) = %t, want %t", test.assetType, test.section, got, test.want)
		}
	}
	if got := normalizeProjectAssetSection("  details  "); got != "details" {
		t.Fatalf("normalized section = %q, want details", got)
	}
}

func detailFactValue(facts []definitionFact, label string) string {
	for _, fact := range facts {
		if fact.Label == label {
			return fact.Value
		}
	}
	return ""
}

func TestDevelopAssetLinksStayInResourceArea(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "model_table:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders"}
	page := projectAssetSummarySignal("sales", asset, map[string]projectview.DevelopAssetView{}, nil)
	if !strings.HasPrefix(page.DetailHref, "/models/") {
		t.Fatalf("detail link = %q, want /models resource area", page.DetailHref)
	}
	if page.DetailHref == "/projects/sales/assets/model_table:orders/details" {
		t.Fatal("legacy project-prefixed asset link escaped into resource signal")
	}
}

func TestModelDetailUsesNamedEntitiesAndExactGrain(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders",
		Payload: map[string]any{
			"Entities": map[string]any{
				"order_line": map[string]any{"Type": "primary", "Fields": []any{"order_id", "line_number"}},
			},
			"GrainEntity": "order_line",
			"Dimensions": map[string]any{
				"order_id": map[string]any{}, "line_number": map[string]any{}, "amount": map[string]any{},
			},
		},
	}
	details := projectAssetDetailsSignal(projectview.DevelopView{ID: "project:test"}, asset, []projectview.DevelopAssetView{asset}, nil)
	overview := map[string]string{}
	for _, fact := range details.Overview {
		overview[fact.Label] = fact.Value
	}
	if overview["Grain entity"] != "order_line" || overview["Entities"] != "1" {
		t.Fatalf("overview = %#v, want named entity and grain", overview)
	}
	if _, exists := overview["Primary key"]; exists {
		t.Fatalf("overview = %#v, removed scalar primary-key contract is still exposed", overview)
	}
	if len(details.Sections) != 2 || details.Sections[0].Title != "Entities (1)" || details.Sections[0].Table == nil {
		t.Fatalf("sections = %#v, want entities and fields", details.Sections)
	}
	entityRows := details.Sections[0].Table.Rows
	if len(entityRows) != 1 || entityRows[0]["name"] != "order_line" || entityRows[0]["fields"] != "order_id, line_number" || entityRows[0]["grain"] != "Yes" {
		t.Fatalf("entity rows = %#v, want ordered composite grain", entityRows)
	}
	fieldRows := details.Sections[1].Table.Rows
	for _, row := range fieldRows {
		if row["name"] == "order_id" || row["name"] == "line_number" {
			if row["entities"] != "order_line" || row["grain"] != "Yes" {
				t.Fatalf("grain field row = %#v, want entity membership and grain", row)
			}
		}
	}
}

func TestAssetDetailNavigationFollowsResourceArea(t *testing.T) {
	tests := []struct {
		assetType string
		area      string
		label     string
	}{
		{assetType: string(projectview.AssetTypeSource), area: "sources", label: "Sources"},
		{assetType: string(projectview.AssetTypeModelTable), area: "models", label: "Models"},
		{assetType: string(projectview.AssetTypeSemanticModel), area: "semantic-models", label: "Semantic models"},
		{assetType: string(projectview.AssetTypeRefreshPipeline), area: "pipelines", label: "Pipelines"},
	}
	for _, tt := range tests {
		t.Run(tt.area, func(t *testing.T) {
			asset := projectview.DevelopAssetView{ID: tt.assetType + ":orders", Type: tt.assetType, Key: "orders", Title: "Orders"}
			page := projectAssetPageSignal(projectview.DevelopView{ID: "project:test", Title: "Test"}, asset, []projectview.DevelopAssetView{asset}, nil, "details", assetLineageModel{})
			if projectAreaForAssetType(asset.Type) != tt.area {
				t.Fatalf("active area = %q, want %q", projectAreaForAssetType(asset.Type), tt.area)
			}
			if len(page.Breadcrumbs) != 2 || page.Breadcrumbs[0].Label != tt.label || page.Breadcrumbs[0].Href == nil || *page.Breadcrumbs[0].Href != "/"+tt.area {
				t.Fatalf("breadcrumbs = %#v, want %s / Orders", page.Breadcrumbs, tt.label)
			}
			if page.Breadcrumbs[1].Label != "Orders" || page.Breadcrumbs[1].Current == nil || !*page.Breadcrumbs[1].Current {
				t.Fatalf("current breadcrumb = %#v, want Orders", page.Breadcrumbs[1])
			}
		})
	}
}

func TestProjectAreaSignalsUseCanonicalBaseAndAssetLinks(t *testing.T) {
	project := projectview.DevelopView{ID: "sales", Title: "Sales"}
	tests := []struct {
		name      string
		area      string
		typ       string
		base      string
		assetID   string
		assetHref string
	}{
		{name: "sources", area: "sources", typ: string(projectview.AssetTypeSource), base: "/sources", assetID: "source:orders", assetHref: "/sources/source:orders/details"},
		{name: "models", area: "models", typ: string(projectview.AssetTypeModelTable), base: "/models", assetID: "model:orders", assetHref: "/models/model:orders/details"},
		{name: "semantic models", area: "semantic-models", typ: string(projectview.AssetTypeSemanticModel), base: "/semantic-models", assetID: "semantic:orders", assetHref: "/semantic-models/semantic:orders/details"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset := projectview.DevelopAssetView{ID: tt.assetID, Type: tt.typ, Key: "orders", Title: "Orders"}
			page := projectPageSignal(project, []projectview.DevelopAssetView{asset}, nil, tt.area, tt.typ, "", "")
			list := *page.AssetList
			if list.SearchHref != tt.base {
				t.Fatalf("search href = %q, want %q", list.SearchHref, tt.base)
			}
			if len(list.Tabs) != 0 {
				t.Fatalf("tabs = %#v, want no redundant type filter", list.Tabs)
			}
			if len(list.Assets) != 1 || list.Assets[0].DetailHref != tt.assetHref {
				t.Fatalf("asset links = %#v, want %q", list.Assets, tt.assetHref)
			}
		})
	}
}

func TestProjectAreaFilterBridgeUsesCanonicalSearchEndpoint(t *testing.T) {
	for _, tt := range []struct {
		area     string
		endpoint string
	}{
		{area: "sources", endpoint: "/sources/search"},
		{area: "models", endpoint: "/models/search"},
		{area: "semantic-models", endpoint: "/semantic-models/search"},
	} {
		t.Run(tt.area, func(t *testing.T) {
			var rendered strings.Builder
			if err := projectAssetFilterRouteBridge(tt.area)[0].Render(&rendered); err != nil {
				t.Fatalf("render filter bridge: %v", err)
			}
			if !strings.Contains(rendered.String(), "@get(&#39;"+tt.endpoint+"&#39;") {
				t.Fatalf("filter bridge = %q, want GET %s", rendered.String(), tt.endpoint)
			}
		})
	}
}
