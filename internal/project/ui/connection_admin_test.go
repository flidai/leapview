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

func TestConnectionLifecycleShowsConfigureOnlyToProjectAdministrators(t *testing.T) {
	asset := projectview.DevelopAssetView{ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Payload: map[string]any{"credentials_required": true}}
	managed := ConnectionAdministrationView{CanManage: true, RequiresBinding: map[string]bool{"warehouse": true}, Bindings: map[string]ConnectionBindingView{}}
	if lifecycle := ConnectionLifecycleForAsset(asset, []projectview.DevelopAssetView{asset}, nil, managed); len(lifecycle.Actions) != 0 {
		t.Fatalf("resource manager create actions = %#v, want none without project admin", lifecycle.Actions)
	}
	managed.CanCreate = true
	if lifecycle := ConnectionLifecycleForAsset(asset, []projectview.DevelopAssetView{asset}, nil, managed); len(lifecycle.Actions) != 1 || lifecycle.Actions[0].ID != "configure" {
		t.Fatalf("project admin create actions = %#v", lifecycle.Actions)
	}
}

func TestConnectionLifecycleNeverHydratesCredentialReferences(t *testing.T) {
	asset := projectview.DevelopAssetView{
		ID: "connection:warehouse", Type: string(projectview.AssetTypeConnection), Key: "warehouse", Title: "Warehouse",
		Payload: map[string]any{"Kind": "postgres", "credentials_required": true},
	}
	view := ConnectionAdministrationView{
		CanCreate: true,
		CanManage: true,
		Bindings: map[string]ConnectionBindingView{"warehouse": {
			ID: "binding_warehouse", LogicalConnection: "warehouse", ConnectorKind: "postgres", AuthenticationMode: "external_bundle",
			Enabled: true, Health: "healthy", Revision: 3,
			CredentialProjectID: "project:secrets", CredentialEnvironment: "prod", SecretPath: "/connections/warehouse", SecretKey: "bundle",
		}},
		RequiresBinding: map[string]bool{"warehouse": true},
	}
	lifecycle := ConnectionLifecycleForAsset(asset, []projectview.DevelopAssetView{asset}, nil, view)
	if lifecycle.CredentialProjectID != "" || lifecycle.CredentialEnvironment != "" || lifecycle.SecretPath != "" || lifecycle.SecretKey != "" {
		t.Fatalf("credential references leaked into lifecycle signal: %#v", lifecycle)
	}
}
