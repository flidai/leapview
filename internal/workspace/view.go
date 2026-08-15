package workspace

import (
	"net/url"
	"strings"
)

type WorkspaceView struct {
	ID                   string
	Title                string
	Description          string
	ActiveServingStateID string
	CreatedAt            string
	UpdatedAt            string
}

func FilterWorkspaceViews(workspaces []WorkspaceView, query string) []WorkspaceView {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return workspaces
	}
	filtered := make([]WorkspaceView, 0, len(workspaces))
	for _, workspace := range workspaces {
		haystack := strings.ToLower(strings.Join([]string{workspace.ID, workspace.Title, workspace.Description}, " "))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, workspace)
		}
	}
	return filtered
}

type AssetView struct {
	ID             string
	SnapshotID     string
	WorkspaceID    string
	ServingStateID string
	Type           string
	Key            string
	ParentID       string
	Title          string
	Description    string
	SourceFile     string
	PayloadSchema  string
	Payload        map[string]any
	ContentHash    string
	Href           string
}

type AssetEdgeView struct {
	ID             string
	WorkspaceID    string
	ServingStateID string
	FromAssetID    string
	ToAssetID      string
	Type           string
}

type RoleView struct {
	Name       string
	Privileges []string
}

type RoleBindingView struct {
	ID          string
	WorkspaceID string
	SubjectType string
	SubjectID   string
	PrincipalID string
	GroupID     string
	Email       string
	DisplayName string
	GroupName   string
	Role        string
	CreatedAt   string
}

func WorkspaceViewFromSummary(row Summary) WorkspaceView {
	activeServingStateID := ""
	if row.ActiveServingStateID != "" {
		activeServingStateID = string(row.ActiveServingStateID)
	}
	return WorkspaceView{
		ID:                   string(row.ID),
		Title:                row.Title,
		Description:          row.Description,
		ActiveServingStateID: activeServingStateID,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

func AssetViewFromCatalogRecord(row AssetRecord) AssetView {
	return AssetView{
		ID:             string(row.ID),
		SnapshotID:     string(row.SnapshotID),
		WorkspaceID:    string(row.WorkspaceID),
		ServingStateID: string(row.ServingStateID),
		Type:           string(row.Type),
		Key:            row.Key,
		ParentID:       string(row.ParentID),
		Title:          row.Title,
		Description:    row.Description,
		SourceFile:     row.SourceFile,
		PayloadSchema:  row.PayloadSchema,
		Payload:        row.Payload,
		ContentHash:    row.ContentHash,
		Href:           AssetHrefForAsset(string(row.WorkspaceID), string(row.Type), row.Key, row.Payload),
	}
}

func AssetEdgeViewFromCatalogRecord(row AssetEdgeRecord) AssetEdgeView {
	return AssetEdgeView{
		ID:             string(row.ID),
		WorkspaceID:    string(row.WorkspaceID),
		ServingStateID: string(row.ServingStateID),
		FromAssetID:    string(row.FromAssetID),
		ToAssetID:      string(row.ToAssetID),
		Type:           string(row.Type),
	}
}

func AssetHref(workspaceID, assetType, key string) string {
	return AssetHrefForAsset(workspaceID, assetType, key, nil)
}

func AssetHrefForAsset(workspaceID, assetType, key string, payload map[string]any) string {
	switch assetType {
	case string(AssetTypeDashboard):
		return "/workspaces/" + url.PathEscape(workspaceID) + "/dashboards/" + url.PathEscape(dashboardRouteID(workspaceID, key, payload))
	default:
		return ""
	}
}

func dashboardRouteID(workspaceID, key string, payload map[string]any) string {
	if payload != nil {
		if id, ok := payload["ID"].(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
		if id, ok := payload["id"].(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
	}
	if workspaceID != "" {
		if routeID, ok := strings.CutPrefix(key, workspaceID+"."); ok {
			return routeID
		}
	}
	return key
}

func FilterAssets(assets []AssetView, typ, query string) []AssetView {
	typ = strings.TrimSpace(typ)
	query = strings.ToLower(strings.TrimSpace(query))
	if typ == "" && query == "" {
		return assets
	}
	out := make([]AssetView, 0, len(assets))
	for _, asset := range assets {
		if typ != "" && asset.Type != typ {
			continue
		}
		haystack := strings.ToLower(asset.Type + " " + asset.Key + " " + asset.Title + " " + asset.Description)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		out = append(out, asset)
	}
	return out
}

func FilterWorkspaceAssets(assets []AssetView, typ, query string) []AssetView {
	typ = strings.TrimSpace(typ)
	query = strings.TrimSpace(query)
	if typ != "" || query != "" {
		return FilterAssets(assets, typ, query)
	}
	return FilterWorkspaceLandingAssets(assets, "", "")
}

// FilterWorkspaceLandingAssets limits the workspace landing surface to assets
// owned by the workspace. Connections and sources remain available in the full
// workspace asset graph as project-scoped dependencies.
func FilterWorkspaceLandingAssets(assets []AssetView, typ, query string) []AssetView {
	typ = strings.TrimSpace(typ)
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]AssetView, 0, len(assets))
	for _, asset := range assets {
		if !IsWorkspaceLandingAsset(asset.Type) {
			continue
		}
		if typ != "" && asset.Type != typ {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(asset.Type + " " + asset.Key + " " + asset.Title + " " + asset.Description)
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		out = append(out, asset)
	}
	return out
}

func FilterConnections(assets []AssetView, query string) []AssetView {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]AssetView, 0, len(assets))
	for _, asset := range assets {
		if asset.Type != string(AssetTypeConnection) {
			continue
		}
		haystack := strings.ToLower(asset.Type + " " + asset.Key + " " + asset.Title + " " + asset.Description)
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		out = append(out, asset)
	}
	return out
}

func AssetByID(assets []AssetView, id string) (AssetView, bool) {
	for _, asset := range assets {
		if asset.ID == id {
			return asset, true
		}
	}
	return AssetView{}, false
}

func IsWorkspaceLandingAsset(typ string) bool {
	switch typ {
	case string(AssetTypeModelTable), string(AssetTypeSemanticModel), string(AssetTypeDashboard), string(AssetTypeRefreshPipeline):
		return true
	default:
		return false
	}
}

func SourceConnectionID(sourceID string, edges []AssetEdgeView) string {
	for _, edge := range edges {
		if edge.Type == string(AssetEdgeUsesConnection) && edge.FromAssetID == sourceID {
			return edge.ToAssetID
		}
	}
	return ""
}
