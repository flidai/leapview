package workspace

import "testing"

func TestWorkspaceViewHelpersFilterAndNavigateAssets(t *testing.T) {
	assets := []AssetView{
		{ID: "semantic_model:sales", Type: "semantic_model", Key: "sales", Title: "Sales Model"},
		{ID: "dashboard:exec", Type: "dashboard", Key: "exec", Title: "Executive Sales"},
		{ID: "connection:duckdb", Type: "connection", Key: "duckdb", Title: "DuckDB"},
		{ID: "source:orders", Type: "source", Key: "orders", Title: "Orders"},
	}
	landing := FilterWorkspaceLandingAssets(assets, "", "")
	if len(landing) != 2 {
		t.Fatalf("landing assets = %#v", landing)
	}
	if filtered := FilterWorkspaceLandingAssets(assets, "", "ord"); len(filtered) != 0 {
		t.Fatalf("workspace search leaked non-landing assets = %#v", filtered)
	}
	if filtered := FilterWorkspaceAssets(assets, "connection", "duck"); len(filtered) != 1 || filtered[0].ID != "connection:duckdb" {
		t.Fatalf("workspace dependency filter = %#v", filtered)
	}
	if filtered := FilterConnections(assets, "duck"); len(filtered) != 1 || filtered[0].ID != "connection:duckdb" {
		t.Fatalf("filtered connections = %#v", filtered)
	}
	if filtered := FilterConnections(assets, "ord"); len(filtered) != 0 {
		t.Fatalf("connection search leaked sources = %#v", filtered)
	}
	edges := []AssetEdgeView{{FromAssetID: "source:orders", ToAssetID: "connection:duckdb", Type: string(AssetEdgeUsesConnection)}}
	if got := SourceConnectionID("source:orders", edges); got != "connection:duckdb" {
		t.Fatalf("source connection ID = %q", got)
	}
}

func TestAssetViewFromCatalogRecordKeepsPayloadContract(t *testing.T) {
	payload := map[string]any{"kind": "duckdb", "credentials_configured": true}
	view := AssetViewFromCatalogRecord(AssetRecord{
		ID:             "connection:olist.olist",
		SnapshotID:     "asset_snapshot_123",
		WorkspaceID:    "leapview",
		ServingStateID: "deploy_a",
		Type:           AssetTypeConnection,
		Key:            "olist.olist",
		ParentID:       "catalog:leapview",
		Title:          "Olist connection",
		Description:    "Local files",
		PayloadSchema:  "connection.v1",
		Payload:        payload,
		ContentHash:    "hash",
	})
	if view.ID != "connection:olist.olist" || view.SnapshotID != "asset_snapshot_123" || view.ParentID != "catalog:leapview" {
		t.Fatalf("asset view identity = %#v", view)
	}
	if view.PayloadSchema != "connection.v1" || view.Payload["credentials_configured"] != true || view.ContentHash != "hash" {
		t.Fatalf("asset view payload = %#v", view)
	}
}

func TestDashboardAssetHrefUsesRuntimeDashboardID(t *testing.T) {
	got := AssetHref("operations", string(AssetTypeDashboard), "operations.fulfillment-operations")
	want := "/workspaces/operations/dashboards/fulfillment-operations"
	if got != want {
		t.Fatalf("dashboard asset href = %q, want %q", got, want)
	}
}
