package ui

import (
	"strings"
	"testing"

	projectview "github.com/flidai/leapview/internal/project"
	catalog "github.com/flidai/leapview/internal/project/navigation"
)

func TestDevelopCatalogUsesStableDashboardLinksWithoutProjectPicker(t *testing.T) {
	page := catalogPageSignal(catalog.Catalog{
		Project:    catalog.Project{ID: "sales", Title: "Sales"},
		Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive"}},
	}, "")
	if len(page.Dashboards) != 1 || page.Dashboards[0].Href != "/dashboards/executive" {
		t.Fatalf("dashboard link = %#v, want stable dashboard route", page.Dashboards)
	}
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
		{name: "data", area: "data", typ: string(projectview.AssetTypeSource), base: "/data", assetID: "source:orders", assetHref: "/data/source:orders/details"},
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
		{area: "data", endpoint: "/data/search"},
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
