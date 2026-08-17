package ui

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectview "github.com/flidai/leapview/internal/project"
)

func TestConnectionLifecycleTreatsAnonymousConnectionAsNotRequired(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "connection:public", Type: string(projectview.AssetTypeConnection), Key: "public", Title: "Public files",
		Payload: projectview.ConnectionAssetPayload(semanticmodel.Connection{Kind: "s3", Scope: "s3://public/", Credentials: semanticmodel.ConnectionCredentials{Provider: "none"}}),
	}
	lifecycle := ConnectionLifecycleForAsset(asset, []projectview.DevelopAssetView{asset}, nil, ConnectionAdministrationView{})
	if lifecycle.State != "not_required" || lifecycle.StatusLabel != "Not required" {
		t.Fatalf("anonymous lifecycle = %#v, want not required", lifecycle)
	}
}

func TestConnectionLifecycleShowsMissingRequiredCredentials(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Title: "Warehouse",
		Payload: map[string]any{"Kind": "postgres", "Scope": "warehouse", "credentials_configured": false, "credentials_required": true},
	}
	lifecycle := ConnectionLifecycleForAsset(asset, []projectview.DevelopAssetView{asset}, nil, ConnectionAdministrationView{})
	if lifecycle.State != "missing" || lifecycle.StatusLabel != "Not configured" {
		t.Fatalf("required lifecycle = %#v, want missing/not configured", lifecycle)
	}
}
