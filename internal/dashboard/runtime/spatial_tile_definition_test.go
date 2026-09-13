package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestSpatialTileCapabilityRetainsDefinitionOverlay(t *testing.T) {
	service, draft := definitionOverlayService(t, "sales_model", false)
	view, err := service.definitionService(draft)
	if err != nil {
		t.Fatal(err)
	}
	for _, publicID := range []string{"", "public-draft"} {
		token, err := service.tiles.register(spatialTileRevision{DashboardID: draft.ID, PageID: "published-page", VisualID: "map", PublicID: publicID, Reports: view.reports})
		if err != nil {
			t.Fatal(err)
		}
		if publicID == "" {
			_, err = service.QueryVisualizationTile(context.Background(), draft.ID, "map", token, 0, 0, 0)
		} else {
			_, err = service.QueryPublicVisualizationTile(context.Background(), publicID, draft.ID, "map", token, 0, 0, 0)
		}
		// Resolve the exact overlay, then reach the existing model's readiness
		// guard. Looking in the published catalog instead fails before this guard.
		if !errors.Is(err, service.runtimes[projectgraph.ResourceID("sales_model")].missing) {
			t.Fatalf("tile did not reach the draft's data runtime: %v", err)
		}
	}
	if _, err := service.reports.Resolve(projectgraph.ResourceID(draft.ID)); err == nil {
		t.Fatal("tile capability leaked the draft into the published catalog")
	}
}

func TestSpatialTileCapabilityWithoutDefinitionFailsClosed(t *testing.T) {
	service, _ := definitionOverlayService(t, "sales_model", false)
	token, err := service.tiles.register(spatialTileRevision{DashboardID: "project", PageID: "project-page", VisualID: "map"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.QueryVisualizationTile(context.Background(), "project", "map", token, 0, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "definition is unavailable") {
		t.Fatalf("missing capability definition used the published catalog: %v", err)
	}
}
