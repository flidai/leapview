package ui

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestSemanticModelDetailProjectionRendersDatasetsMetricsRelationshipsAndGraph(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Entities: map[string]semanticmodel.ModelEntitySpec{
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
			"customers": {Entities: map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id"},
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
	}
	asset := projectview.DevelopAssetView{
		ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales",
		Payload: projectview.SemanticModelAssetPayload(model),
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
	if err := ProjectAssetPageWithRefreshAndVersionsForEnvironment(catalog.Catalog{}, project, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetRefreshState{}, AssetVersionsState{}).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	dom := rendered.String()
	if !strings.Contains(dom, "<lv-project-asset-page") || !strings.Contains(dom, "/static/semantic-model-graph.js") {
		t.Fatalf("semantic-model detail DOM missing route root or graph asset: %s", dom)
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
	table := semanticmodel.Table{
		Sources:            []string{"olist.geolocation"},
		Transform:          semanticmodel.Transform{SQL: "SELECT zip_prefix FROM source.\"olist.geolocation\""},
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix", Description: "ZIP code prefix"}},
		Entities:           map[string]semanticmodel.ModelEntitySpec{"zip_prefix": {Type: "primary", Fields: []string{"zip_prefix"}}},
		GrainEntity:        "zip_prefix",
		SourceDependencies: []string{"olist.geolocation"},
		Schema:             semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "zip_prefix", Ordinal: 0, PhysicalType: "VARCHAR"}}},
	}
	asset := projectview.DevelopAssetView{
		ID: "model:zip_geolocations", Type: string(projectview.AssetTypeModelTable), Key: "zip_geolocations", Title: "ZIP locations",
		Payload: projectview.ModelTableAssetPayload(table),
	}
	project := projectview.DevelopView{ID: "project:test", Title: "Test"}
	details := projectAssetDetailsSignal(project, asset, []projectview.DevelopAssetView{asset}, nil)
	if got := factValue(details.Overview, "Fields"); got != "1" {
		t.Fatalf("fields fact = %q, want 1", got)
	}
	if got := factValue(details.Overview, "Input sources"); got != "1" {
		t.Fatalf("input sources fact = %q, want 1", got)
	}
	if got := factValue(details.Overview, "Mode"); got != "Transform" {
		t.Fatalf("mode fact = %q, want Transform", got)
	}
	if len(details.Sections) != 3 || details.Sections[0].Title != "Entities (1)" || details.Sections[1].Title != "Fields (1)" || details.Sections[2].Title != "SQL" {
		t.Fatalf("sections = %#v, want entities, fields, and SQL", details.Sections)
	}
	if len(details.Sections[0].Table.Rows) != 1 || details.Sections[0].Table.Rows[0]["name"] != "zip_prefix" || details.Sections[0].Table.Rows[0]["grain"] != "Yes" {
		t.Fatalf("entity rows = %#v, want grain zip_prefix entity", details.Sections[0].Table.Rows)
	}
	if uisignals.ValueOrZero(details.Sections[2].Code) != table.Transform.SQL || uisignals.ValueOrZero(details.Sections[2].Lang) != "sql" {
		t.Fatalf("SQL section = %#v, want compiled transform SQL", details.Sections[2])
	}
	if len(details.Sections[1].Table.Rows) != 1 {
		t.Fatalf("field rows = %#v, want one row", details.Sections[1].Table.Rows)
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
		Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive"}},
	}, "")
	if len(page.Dashboards) != 1 || page.Dashboards[0].Href != "/dashboards/executive" {
		t.Fatalf("dashboard link = %#v, want stable dashboard route", page.Dashboards)
	}
}

func TestSourceAndPipelineDetailsConsumeTypedAssetProjections(t *testing.T) {
	source := projectview.DevelopAssetView{
		ID: "source:orders", Type: string(projectview.AssetTypeSource), Key: "orders", Title: "Orders",
		Payload: projectview.SourceAssetPayload(semanticmodel.Source{
			Format: "csv", Connection: "warehouse", Path: "s3://bucket/orders.csv",
			Fields: map[string]semanticmodel.SourceField{"order_id": {Type: "int"}},
		}),
	}
	sourceDetails := assetDetailModelForAsset(projectview.DevelopView{ID: "project:test"}, source, []projectview.DevelopAssetView{source}, nil)
	if got := detailFactValue(sourceDetails.Overview, "Connection"); got != "warehouse" {
		t.Fatalf("source connection fact = %q, want warehouse", got)
	}
	if len(sourceDetails.Sections) != 1 || len(sourceDetails.Sections[0].Table.Rows) != 1 {
		t.Fatalf("source field section = %#v, want one projected field", sourceDetails.Sections)
	}

	pipeline := projectview.DevelopAssetView{
		ID: "pipeline:sales", Type: string(projectview.AssetTypeRefreshPipeline), Key: "sales", Title: "Sales refresh",
		Payload: projectview.RefreshPipelineAssetPayload(refreshschedule.Definition{
			ID: "pipeline:sales", Name: "sales", SemanticModelID: projectgraph.ResourceID("semantic:sales"),
			Schedules: []refreshschedule.Schedule{{Expression: "0 * * * *", Timezone: "UTC"}},
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
			if !strings.Contains(rendered.String(), "@post(&#39;"+tt.endpoint+"&#39;") {
				t.Fatalf("filter bridge = %q, want POST %s", rendered.String(), tt.endpoint)
			}
		})
	}
}
