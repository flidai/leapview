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
	return "/data/" + url.PathEscape(assetID) + "/" + url.PathEscape(section)
}

func ConnectionAssetSectionHref(assetID, section string) string {
	return "/connections/" + url.PathEscape(assetID) + "/" + url.PathEscape(section)
}

func CanonicalAssetSectionHref(asset project.DevelopAssetView, section string) string {
	switch asset.Type {
	case string(project.AssetTypeDashboard):
		if asset.Href != "" {
			return asset.Href
		}
		return "/dashboards/" + url.PathEscape(asset.Key)
	case "connection":
		return ConnectionAssetSectionHref(asset.ID, section)
	case "source":
		return ProjectAssetSectionHref(asset.ID, section)
	case string(project.AssetTypeModelTable):
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
