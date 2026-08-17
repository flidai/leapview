package project

import "testing"

func TestFilterProjectLandingAssetsIncludesSources(t *testing.T) {
	assets := []DevelopAssetView{
		{ID: "source:orders", Type: string(AssetTypeSource), Key: "orders", Title: "Orders"},
		{ID: "model:orders", Type: string(AssetTypeModelTable), Key: "orders", Title: "Orders model"},
		{ID: "connection:warehouse", Type: string(AssetTypeConnection), Key: "warehouse", Title: "Warehouse"},
	}

	got := FilterProjectLandingAssets(assets, string(AssetTypeSource), "orders")
	if len(got) != 1 || got[0].ID != "source:orders" {
		t.Fatalf("source filter = %#v, want source:orders only", got)
	}
}

func TestFilterProjectLandingAssetsScopesEachFixedResourceType(t *testing.T) {
	assets := []DevelopAssetView{
		{ID: "source:orders", Type: string(AssetTypeSource), Key: "orders"},
		{ID: "model:orders", Type: string(AssetTypeModelTable), Key: "orders"},
		{ID: "semantic:orders", Type: string(AssetTypeSemanticModel), Key: "orders"},
	}

	for _, typ := range []string{string(AssetTypeSource), string(AssetTypeModelTable), string(AssetTypeSemanticModel)} {
		got := FilterProjectLandingAssets(assets, typ, "")
		if len(got) != 1 || got[0].Type != typ {
			t.Fatalf("type %q filter = %#v, want one matching asset", typ, got)
		}
	}
}
