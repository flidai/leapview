package http

import (
	"context"
	nethttp "net/http"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	"github.com/flidai/leapview/internal/project/ui"
)

type Principal struct {
	ID          string
	Email       string
	DisplayName string
	DevBypass   bool
}

type PrincipalProvider func(*nethttp.Request) (Principal, bool)

type ReadModel struct {
	DevelopCatalogReader func() (DevelopCatalogReader, error)
	MetricsForProject    func(string) (Metrics, bool)
	RootCatalog          func() catalog.Catalog
	Environment          func(*nethttp.Request) string
	CurrentPrincipal     PrincipalProvider
	AuthConfigured       bool
}

func (m ReadModel) CatalogForProjectsPage(_ *nethttp.Request, _ []project.DevelopView) catalog.Catalog {
	return m.rootCatalog()
}

func (m ReadModel) ProjectResponse(_ *nethttp.Request, _ string) project.DevelopView {
	return CatalogProjectView(m.rootCatalog())
}

func (m ReadModel) ProjectViewContext(_ context.Context, _ string) project.DevelopView {
	return CatalogProjectView(m.rootCatalog())
}

func (m ReadModel) ProjectAssetsAndEdges(r *nethttp.Request, projectID string) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	return m.ProjectAssetsAndEdgesForData(r.Context(), projectID, m.environment(r))
}

func (m ReadModel) ProjectAssetsAndEdgesForData(ctx context.Context, projectID, environment string) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	catalog, ok, err := m.activeAssetCatalog(ctx, projectID, environment)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return []project.DevelopAssetView{}, []project.DevelopEdgeView{}, nil
	}
	return AssetCatalogViews(catalog), AssetCatalogEdgeViews(catalog), nil
}

func (m ReadModel) PlatformAssetsAndEdges(r *nethttp.Request) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	projectID := strings.TrimSpace(m.rootCatalog().Project.ID)
	environment := m.environment(r)
	catalog, ok, err := m.activeAssetCatalog(r.Context(), projectID, environment)
	if err != nil || !ok {
		return nil, nil, err
	}
	assetsByID := map[string]project.DevelopAssetView{}
	edgeKeys := map[string]project.DevelopEdgeView{}
	{
		assets := AssetCatalogViews(catalog)
		edges := AssetCatalogEdgeViews(catalog)
		localGlobal := map[string]struct{}{}
		for _, asset := range assets {
			if asset.Type != string(project.AssetTypeConnection) && asset.Type != string(project.AssetTypeSource) {
				continue
			}
			if _, exists := assetsByID[asset.ID]; !exists {
				assetsByID[asset.ID] = asset
			}
			localGlobal[asset.ID] = struct{}{}
		}
		for _, edge := range edges {
			if edge.Type != string(project.AssetEdgeUsesConnection) {
				continue
			}
			if _, ok := localGlobal[edge.FromAssetID]; !ok {
				continue
			}
			if _, ok := localGlobal[edge.ToAssetID]; !ok {
				continue
			}
			key := edge.FromAssetID + "|" + edge.ToAssetID + "|" + edge.Type
			if _, exists := edgeKeys[key]; !exists {
				edgeKeys[key] = edge
			}
		}
	}
	assets := make([]project.DevelopAssetView, 0, len(assetsByID))
	for _, asset := range assetsByID {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	edges := make([]project.DevelopEdgeView, 0, len(edgeKeys))
	for _, edge := range edgeKeys {
		edges = append(edges, edge)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Type != edges[j].Type {
			return edges[i].Type < edges[j].Type
		}
		if edges[i].FromAssetID != edges[j].FromAssetID {
			return edges[i].FromAssetID < edges[j].FromAssetID
		}
		return edges[i].ToAssetID < edges[j].ToAssetID
	})
	return assets, edges, nil
}

func (m ReadModel) activeAssetCatalog(ctx context.Context, projectID, environment string) (project.DevelopCatalog, bool, error) {
	reader, err := m.assetCatalogReader()
	if err != nil || reader == nil {
		return project.DevelopCatalog{}, false, err
	}
	id, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return project.DevelopCatalog{}, false, err
	}
	return reader.ActiveAssetCatalog(ctx, id, environment)
}

func (m ReadModel) assetCatalogReader() (DevelopCatalogReader, error) {
	if m.DevelopCatalogReader == nil {
		return nil, nil
	}
	return m.DevelopCatalogReader()
}

func (m ReadModel) metricsForProject(projectID string) (Metrics, bool) {
	if m.MetricsForProject == nil {
		return nil, false
	}
	return m.MetricsForProject(projectID)
}

func (m ReadModel) catalogForProject(projectID string) catalog.Catalog {
	active := m.rootCatalog()
	if strings.TrimSpace(active.Project.ID) == "" {
		active.Project.ID = strings.TrimSpace(projectID)
	}
	return active
}

func (m ReadModel) rootCatalog() catalog.Catalog {
	if m.RootCatalog == nil {
		return catalog.Catalog{}
	}
	return m.RootCatalog()
}

func (m ReadModel) environment(r *nethttp.Request) string {
	if m.Environment == nil {
		return ""
	}
	return m.Environment(r)
}

func (m ReadModel) currentPrincipal(r *nethttp.Request) (Principal, bool) {
	if m.CurrentPrincipal == nil {
		return Principal{}, false
	}
	return m.CurrentPrincipal(r)
}

func AssetCatalogViews(catalog project.DevelopCatalog) []project.DevelopAssetView {
	assets := make([]project.DevelopAssetView, 0, len(catalog.Assets))
	for _, row := range catalog.Assets {
		assets = append(assets, project.DevelopAssetViewFromCatalogRecord(row))
	}
	return assets
}

func AssetCatalogEdgeViews(catalog project.DevelopCatalog) []project.DevelopEdgeView {
	edges := make([]project.DevelopEdgeView, 0, len(catalog.Edges))
	for _, row := range catalog.Edges {
		edges = append(edges, project.DevelopEdgeViewFromCatalogRecord(row))
	}
	return edges
}

func CatalogProjectView(catalog catalog.Catalog) project.DevelopView {
	return project.DevelopView{
		ID:          catalog.Project.ID,
		Title:       catalog.Project.Title,
		Description: catalog.Project.Description,
	}
}
