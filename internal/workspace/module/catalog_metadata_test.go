package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

func TestCatalogDashboardMetadataUsesRequestEnvironmentAndSharesModelRefresh(t *testing.T) {
	refreshCalls := 0
	m := &Module{
		runtimeEnvironment: "production",
		environment:        func(*http.Request) string { return "preview" },
		dashboardPopularity: func(context.Context, int) (map[string]PopularityLevel, error) {
			return map[string]PopularityLevel{"sales.executive": uisignals.PopularityLevelHigh}, nil
		},
		dashboardRefreshedAt: func(_ context.Context, workspaceID, environment, modelID string) (string, bool, error) {
			refreshCalls++
			if workspaceID != "sales" || environment != "preview" || modelID != "commerce" {
				t.Fatalf("refresh lookup = (%q, %q, %q)", workspaceID, environment, modelID)
			}
			return "2026-08-12T09:42:00Z", true, nil
		},
	}
	catalogs := []catalog.Catalog{{
		Workspace: catalog.Workspace{ID: "sales", Title: "Sales"},
		Dashboards: []catalog.Dashboard{
			{ID: "executive", SemanticModel: "commerce"},
			{ID: "pipeline", SemanticModel: "commerce"},
		},
	}}

	metadata := m.catalogDashboardMetadata(httptest.NewRequest("GET", "/", nil), catalogs)
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want one shared model lookup", refreshCalls)
	}
	if metadata["sales.executive"].Popularity != uisignals.PopularityLevelHigh {
		t.Fatalf("popularity = %q", metadata["sales.executive"].Popularity)
	}
	for _, dashboardID := range []string{"sales.executive", "sales.pipeline"} {
		if got := metadata[dashboardID].LastRefreshedAt; got != "2026-08-12T09:42:00Z" {
			t.Fatalf("%s last refreshed = %q", dashboardID, got)
		}
	}
}
