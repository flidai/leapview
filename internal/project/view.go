package project

import (
	"net/url"
	"strings"
)

type DevelopView struct {
	ID                   string
	Title                string
	Description          string
	ActiveServingStateID string
	CreatedAt            string
	UpdatedAt            string
}

func FilterDevelopViews(projects []DevelopView, query string) []DevelopView {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return projects
	}
	filtered := make([]DevelopView, 0, len(projects))
	for _, project := range projects {
		haystack := strings.ToLower(strings.Join([]string{project.ID, project.Title, project.Description}, " "))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, project)
		}
	}
	return filtered
}

type DevelopAssetView struct {
	ID             string
	SnapshotID     string
	ProjectID      string
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

type DevelopEdgeView struct {
	ID             string
	ProjectID      string
	ServingStateID string
	FromAssetID    string
	ToAssetID      string
	Type           string
}

func DevelopAssetViewFromCatalogRecord(row DevelopAssetRecord) DevelopAssetView {
	return DevelopAssetView{
		ID:             string(row.ID),
		SnapshotID:     string(row.SnapshotID),
		ProjectID:      string(row.ProjectID),
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
		Href:           AssetHrefForAsset(string(row.ProjectID), string(row.Type), row.Key, row.Payload),
	}
}

func DevelopEdgeViewFromCatalogRecord(row DevelopEdgeRecord) DevelopEdgeView {
	return DevelopEdgeView{
		ID:             string(row.ID),
		ProjectID:      string(row.ProjectID),
		ServingStateID: string(row.ServingStateID),
		FromAssetID:    string(row.FromAssetID),
		ToAssetID:      string(row.ToAssetID),
		Type:           string(row.Type),
	}
}

func AssetHref(projectID, assetType, key string) string {
	return AssetHrefForAsset(projectID, assetType, key, nil)
}

func AssetHrefForAsset(projectID, assetType, key string, payload map[string]any) string {
	switch assetType {
	case string(AssetTypeDashboard):
		return "/dashboards/" + url.PathEscape(dashboardRouteID(projectID, key, payload))
	default:
		return ""
	}
}

func dashboardRouteID(projectID, key string, payload map[string]any) string {
	if payload != nil {
		if id, ok := payload["ID"].(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
		if id, ok := payload["id"].(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
	}
	if projectID != "" {
		if routeID, ok := strings.CutPrefix(key, projectID+"."); ok {
			return routeID
		}
	}
	return key
}

func FilterAssets(assets []DevelopAssetView, typ, query string) []DevelopAssetView {
	typ = strings.TrimSpace(typ)
	query = strings.ToLower(strings.TrimSpace(query))
	if typ == "" && query == "" {
		return assets
	}
	out := make([]DevelopAssetView, 0, len(assets))
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

func FilterProjectAssets(assets []DevelopAssetView, typ, query string) []DevelopAssetView {
	typ = strings.TrimSpace(typ)
	query = strings.TrimSpace(query)
	if typ != "" || query != "" {
		return FilterAssets(assets, typ, query)
	}
	return FilterProjectLandingAssets(assets, "", "")
}

// FilterProjectLandingAssets limits the project landing surface to assets
// owned by the project. Sources are first-class assets on the data area;
// connections remain available in the full project asset graph as a
// project-scoped dependency.
func FilterProjectLandingAssets(assets []DevelopAssetView, typ, query string) []DevelopAssetView {
	typ = strings.TrimSpace(typ)
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]DevelopAssetView, 0, len(assets))
	for _, asset := range assets {
		if !IsProjectLandingAsset(asset.Type) {
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

func FilterConnections(assets []DevelopAssetView, query string) []DevelopAssetView {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]DevelopAssetView, 0, len(assets))
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

func AssetByID(assets []DevelopAssetView, id string) (DevelopAssetView, bool) {
	for _, asset := range assets {
		if asset.ID == id {
			return asset, true
		}
	}
	return DevelopAssetView{}, false
}

func IsProjectLandingAsset(typ string) bool {
	switch typ {
	case string(AssetTypeSource), string(AssetTypeModelTable), string(AssetTypeSemanticModel), string(AssetTypeDashboard), string(AssetTypeRefreshPipeline):
		return true
	default:
		return false
	}
}

func SourceConnectionID(sourceID string, edges []DevelopEdgeView) string {
	for _, edge := range edges {
		if edge.Type == string(AssetEdgeUsesConnection) && edge.FromAssetID == sourceID {
			return edge.ToAssetID
		}
	}
	return ""
}
