package ui

import (
	"html"
	"strings"
	"testing"

	workspaceview "github.com/flidai/leapview/internal/workspace"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
)

func TestSemanticModelDesignWorkspaceVocabulary(t *testing.T) {
	workspace, catalog, assets, access := semanticDesignUIFixtures()

	var out strings.Builder
	err := WorkspacePage(catalog, workspace, assets, "", "", "Owner", access, "csrf", testAccessCommandBindings()).Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceBootstrapSignals(catalog, workspace, assets, "", "", "Owner", access))

	for _, want := range []string{"Semantic model", "Dashboard", "Olist Commerce", "Executive Sales Dashboard"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("workspace page missing %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{"Metric view", "Dataset", "Cache table"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("workspace page rendered legacy vocabulary %q:\n%s", notWant, rendered)
		}
	}
}

func TestSemanticModelDesignDetailsVocabulary(t *testing.T) {
	workspace, catalog, assets, _ := semanticDesignUIFixtures()
	asset := assets[0]

	var out strings.Builder
	err := WorkspaceAssetPage(catalog, workspace, asset, assets, nil, "details", "Owner").Render(&out)
	if err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	rendered += bootstrapJSON(WorkspaceAssetBootstrapSignals(catalog, workspace, asset, assets, nil, "details", "Owner", AssetRefreshState{}, AssetVersionsState{}))

	for _, want := range []string{"Model tables (1)", "Measures (1)", "Relationships (1)", "From table", "From field", "To table", "To field"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("semantic model details missing %q:\n%s", want, rendered)
		}
	}
	for _, notWant := range []string{"Sources (1)", "Connections (", "Fields (", "Metric view", "Datasets", "Cache tables", "Cache table"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("semantic model details rendered legacy vocabulary %q:\n%s", notWant, rendered)
		}
	}
}

func semanticDesignUIFixtures() (workspaceview.WorkspaceView, catalog.Catalog, []workspaceview.AssetView, WorkspaceAccessResponse) {
	workspace := workspaceview.WorkspaceView{ID: "leapview", Title: "LeapView Workspace", Description: "Local BI workspace."}
	catalog := catalog.Catalog{Workspace: catalog.Workspace{ID: workspace.ID, Title: workspace.Title, Description: workspace.Description}}
	assets := []workspaceview.AssetView{
		{
			ID:          "model",
			WorkspaceID: workspace.ID,
			Type:        "semantic_model",
			Key:         "olist",
			Title:       "Olist Commerce",
			Description: "Olist semantic model.",
			Payload: map[string]any{
				"Models":        map[string]any{"orders": map[string]any{"Source": "olist_orders"}},
				"Sources":       map[string]any{"olist_orders": map[string]any{"Connection": "olist", "Format": "csv", "Path": "orders.csv"}},
				"Measures":      map[string]any{"revenue": map[string]any{"Expr": "SUM(orders.revenue)", "Table": "orders", "Grain": "order_id"}},
				"Relationships": []any{map[string]any{"From": "orders.customer_id", "To": "customers.customer_id", "Cardinality": "many_to_one", "Active": true}},
			},
		},
		{ID: "dashboard", WorkspaceID: workspace.ID, Type: "dashboard", Key: "executive-sales", Title: "Executive Sales Dashboard", Description: "Sales overview."},
	}
	access := WorkspaceAccessResponse{Workspace: workspace, CanManage: false}
	return workspace, catalog, assets, access
}
