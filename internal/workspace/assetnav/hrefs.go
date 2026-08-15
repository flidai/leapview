package assetnav

import (
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/workspace"
)

func ConnectionsHref(query string) string {
	if query = strings.TrimSpace(query); query == "" {
		return "/connections"
	}
	return "/connections?q=" + url.QueryEscape(query)
}

func WorkspaceAssetSectionHref(workspaceID, assetID, section string) string {
	return "/workspaces/" + workspaceID + "/assets/" + assetID + "/" + section
}

func ConnectionAssetSectionHref(assetID, section string) string {
	return "/connections/" + assetID + "/" + section
}

func ConnectionSourceAssetSectionHref(connectionID, sourceID, section string) string {
	return "/connections/" + connectionID + "/sources/" + sourceID + "/" + section
}

func CanonicalAssetSectionHref(workspaceID string, asset workspace.AssetView, section string, edges []workspace.AssetEdgeView) string {
	switch asset.Type {
	case "connection":
		return ConnectionAssetSectionHref(asset.ID, section)
	case "source":
		return CanonicalSourceAssetSectionHref(workspaceID, asset.ID, section, edges)
	default:
		return WorkspaceAssetSectionHref(workspaceID, asset.ID, section)
	}
}

func CanonicalSourceAssetSectionHref(workspaceID, sourceID, section string, edges []workspace.AssetEdgeView) string {
	if connectionID := SourceConnectionID(sourceID, edges); connectionID != "" {
		return ConnectionSourceAssetSectionHref(connectionID, sourceID, section)
	}
	return WorkspaceAssetSectionHref(workspaceID, sourceID, section)
}

func SourceConnectionID(sourceID string, edges []workspace.AssetEdgeView) string {
	return workspace.SourceConnectionID(sourceID, edges)
}
