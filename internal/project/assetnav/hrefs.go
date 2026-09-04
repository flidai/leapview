package assetnav

import (
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/project"
)

func ConnectionsHref(query string) string {
	if query = strings.TrimSpace(query); query == "" {
		return "/connections"
	}
	return "/connections?q=" + url.QueryEscape(query)
}

func ProjectAssetSectionHref(assetID, section string) string {
	return "/sources/" + url.PathEscape(assetID) + "/" + url.PathEscape(section)
}

func ConnectionAssetSectionHref(assetID, section string) string {
	return "/connections/" + url.PathEscape(assetID) + "/" + url.PathEscape(section)
}

func CanonicalAssetSectionHref(asset project.DevelopAssetView, section string) string {
	switch asset.Type {
	case string(project.AssetTypeDashboard):
		// Dashboard runtime routes and Develop detail routes are deliberately
		// separate: the former renders the report, while the latter renders the
		// governed catalog read model. Use the stable asset ID for detail links.
		if strings.TrimSpace(section) == "" {
			section = "details"
		}
		return "/dashboards/" + url.PathEscape(asset.ID) + "/" + url.PathEscape(section)
	case "connection":
		return ConnectionAssetSectionHref(asset.ID, section)
	case "source":
		return ProjectAssetSectionHref(asset.ID, section)
	case string(project.AssetTypeModel):
		return "/models/" + url.PathEscape(asset.ID) + "/" + url.PathEscape(section)
	case string(project.AssetTypeSemanticModel):
		return "/semantic-models/" + url.PathEscape(asset.ID) + "/" + url.PathEscape(section)
	case string(project.AssetTypeRefreshPipeline):
		return "/pipelines/" + url.PathEscape(asset.ID) + "/" + url.PathEscape(section)
	default:
		return ProjectAssetSectionHref(asset.ID, section)
	}
}

func SourceConnectionID(sourceID string, edges []project.DevelopEdgeView) string {
	return project.SourceConnectionID(sourceID, edges)
}
