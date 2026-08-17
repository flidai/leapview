package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

func TestSemanticModelDetailProjectionRendersTablesMeasuresRelationshipsAndGraph(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}},
			},
			"customers": {PrimaryKey: "customer_id"},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count_distinct", Label: "Orders", Input: semanticmodel.MeasureInput{Field: "orders.order_id"}},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customer", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
		}},
	}
	asset := projectview.DevelopAssetView{
		ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales",
		Payload: projectview.SemanticModelAssetPayload(model),
	}
	project := projectview.DevelopView{ID: "project:test", Title: "Test"}
	details := projectAssetDetailsSignal(project, asset, []projectview.DevelopAssetView{asset}, nil)
	if len(details.Sections) != 3 {
		t.Fatalf("detail sections = %d, want tables/measures/relationships", len(details.Sections))
	}
	for _, want := range []string{"Model tables (2)", "Measures (1)", "Relationships (1)"} {
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
	measureTable := semanticMeasuresTable(project.ID, asset, []projectview.DevelopAssetView{asset}, asset.Payload)
	if len(measureTable.Rows) != 1 {
		t.Fatalf("measure rows = %#v, want one row", measureTable.Rows)
	}
	measureRow := measureTable.Rows[0]
	if measureRow["table"] != "orders" || measureRow["input"] != "orders.order_id" {
		t.Fatalf("measure row = %#v, want canonical fact and input", measureRow)
	}
	if aggregation := measureRow["aggregation"].(recordTableBadge).Label; aggregation != "count_distinct" {
		t.Fatalf("measure aggregation = %#v, want count_distinct", aggregation)
	}

	bootstrap := ProjectAssetBootstrapSignalsForEnvironment(catalog.Catalog{}, project, asset, []projectview.DevelopAssetView{asset}, nil, "details", "dev", "", AssetRefreshState{}, AssetVersionsState{})
	encoded, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Model tables (2)", "Measures (1)", "Relationships (1)"} {
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

func TestModelTableDetailProjectionRendersCompiledDefinition(t *testing.T) {
	table := semanticmodel.Table{
		Sources:            []string{"olist.geolocation"},
		Transform:          semanticmodel.Transform{SQL: "SELECT zip_prefix FROM source.\"olist.geolocation\""},
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix", Description: "ZIP code prefix"}},
		PrimaryKey:         "zip_prefix",
		Grain:              "zip_prefix",
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
	if len(details.Sections) != 2 || details.Sections[0].Title != "Fields (1)" || details.Sections[1].Title != "SQL" {
		t.Fatalf("sections = %#v, want fields and SQL", details.Sections)
	}
	if uisignals.ValueOrZero(details.Sections[1].Code) != table.Transform.SQL || uisignals.ValueOrZero(details.Sections[1].Lang) != "sql" {
		t.Fatalf("SQL section = %#v, want compiled transform SQL", details.Sections[1])
	}
	if len(details.Sections[0].Table.Rows) != 1 {
		t.Fatalf("field rows = %#v, want one row", details.Sections[0].Table.Rows)
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
