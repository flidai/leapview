package datastar

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/ui"
)

func ProjectAssetStreamID(projectID, assetID, section string) string {
	return "project-asset:" + projectID + ":" + assetID + ":" + section
}

func ProjectAssetRefreshSections() []string {
	return []string{"details", "refreshes", "lineage", "versions"}
}

func ProjectAssetUpdateSection(r *http.Request) string {
	switch strings.TrimSpace(r.URL.Query().Get("section")) {
	case "refreshes":
		return "refreshes"
	case "lineage":
		return "lineage"
	case "versions":
		return "versions"
	default:
		return "details"
	}
}

func ProjectAssetRefreshSignals(view project.DevelopView, asset project.DevelopAssetView, assets []project.DevelopAssetView, edges []project.DevelopEdgeView, refresh ui.AssetRefreshState, section string) map[string]any {
	return ui.ProjectAssetRefreshSignals(view, asset, assets, edges, refresh, section)
}
