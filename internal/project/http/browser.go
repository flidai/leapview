// Package browser owns the authenticated, server-bound project browser
// surfaces. It deliberately accepts no project selector from the request:
// the active project and immutable serving generation come from composition.
package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectview "github.com/flidai/leapview/internal/project"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/go-chi/chi/v5"
	g "maragu.dev/gomponents"
)

type GraphReader interface {
	ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
}

type CatalogAuthorizer interface {
	List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error)
	Resolve(context.Context, string, projectcatalog.Ref, access.Capability, bool) (projectcatalog.Result, error)
}

type Principal struct {
	ID        string
	DevBypass bool
}

type BrowserHandler struct {
	Graph            GraphReader
	Catalog          CatalogAuthorizer
	ResolveProjectID func(context.Context) (projectgraph.ResourceID, error)
	Environment      string
	Trace            *pagestream.TraceStore
	Layout           func(*stdhttp.Request) webpage.Provider
	CSRFToken        func(*stdhttp.Request) string
	CurrentUser      func(*stdhttp.Request) (Principal, bool)
	Authenticate     func(stdhttp.Handler) stdhttp.Handler
}

// MountAuthenticated mounts only canonical browser paths. Legacy tenant
// paths are intentionally absent; requests to them remain ordinary 404s.
func (h *BrowserHandler) MountAuthenticated(r chi.Router) {
	if h == nil || r == nil {
		return
	}
	wrap := func(next stdhttp.HandlerFunc) stdhttp.HandlerFunc {
		if h.Authenticate == nil {
			return next
		}
		return h.Authenticate(stdhttp.HandlerFunc(next)).ServeHTTP
	}
	r.Get("/", wrap(h.Insights))
	r.Get("/explore", wrap(h.Explore))
	r.Get("/data", wrap(h.Data))
	r.Get("/data/{asset}/{section}", wrap(h.DataAsset))
	r.Get("/models", wrap(h.Models))
	r.Get("/models/{asset}/{section}", wrap(h.ModelAsset))
	r.Get("/semantic-models", wrap(h.SemanticModels))
	r.Get("/semantic-models/{asset}/{section}", wrap(h.SemanticModelAsset))
	r.Get("/pipelines", wrap(h.Pipelines))
	r.Get("/pipelines/{asset}/{section}", wrap(h.PipelineAsset))
	r.Get("/connections", wrap(h.Connections))
	r.Get("/connections/{asset}/{section}", wrap(h.ConnectionAsset))
	r.Post("/catalog/search", wrap(h.CatalogSearch))
	r.Post("/data/search", wrap(h.DataSearch))
	r.Post("/connections/search", wrap(h.ConnectionsSearch))
	r.Post("/models/search", wrap(h.ModelsSearch))
	r.Post("/semantic-models/search", wrap(h.SemanticModelsSearch))
}

func (h *BrowserHandler) Insights(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindDashboard, projectgraph.KindModel, projectgraph.KindSemanticModel}) {
		return
	}
	catalog := h.navigationCatalog(r)
	writeDocument(w, projectui.CatalogPageForCatalogsWithOptions([]projectnavigation.Catalog{catalog}, projectui.CatalogListOptions{Query: r.URL.Query().Get("q")}, h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) CatalogSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindDashboard, projectgraph.KindModel, projectgraph.KindSemanticModel}) {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("entityListQuery"))
	if query == "" {
		query = strings.TrimSpace(r.FormValue("entityListQuery"))
	}
	patch := projectui.CatalogListPatchForCatalogsQuery([]projectnavigation.Catalog{h.navigationCatalog(r)}, query)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) Explore(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindSemanticModel}) {
		return
	}
	catalog := h.navigationCatalog(r)
	page, explorer := h.dataExplorerSignals(r)
	writeDocument(w, projectui.DataExplorerPage(catalog, page, explorer, h.csrf(r), h.layout(r)))
}

func (h *BrowserHandler) Data(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "data", string(projectview.AssetTypeSource))
}

func (h *BrowserHandler) Models(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "models", string(projectview.AssetTypeModelTable))
}

func (h *BrowserHandler) SemanticModels(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAssets(w, r, "semantic-models", string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) DataAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindSource)
}

func (h *BrowserHandler) ModelAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindModel)
}

func (h *BrowserHandler) SemanticModelAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindSemanticModel)
}

func (h *BrowserHandler) PipelineAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindPipeline)
}

func (h *BrowserHandler) ConnectionAsset(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.assetDocument(w, r, projectgraph.KindConnection)
}

func (h *BrowserHandler) assetDocument(w stdhttp.ResponseWriter, r *stdhttp.Request, kinds ...projectgraph.Kind) {
	if !h.authorizeAny(w, r, kinds) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	asset, found := projectview.AssetByID(assets, chi.URLParam(r, "asset"))
	if !found {
		stdhttp.NotFound(w, r)
		return
	}
	project := projectview.DevelopView{ID: projectID.String(), Title: h.navigationCatalog(r).Project.Title, Description: h.navigationCatalog(r).Project.Description}
	section := chi.URLParam(r, "section")
	if asset.Type == string(projectview.AssetTypeConnection) {
		writeDocument(w, projectui.ConnectionAssetPageWithVersionsForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, section, h.Environment, "", projectui.AssetVersionsState{}))
		return
	}
	writeDocument(w, projectui.ProjectAssetPageWithRefreshAndVersionsForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, section, h.Environment, "", projectui.AssetRefreshState{}, projectui.AssetVersionsState{}, h.layout(r)))
}

func (h *BrowserHandler) projectAssets(w stdhttp.ResponseWriter, r *stdhttp.Request, area, activeType string) {
	kinds := []projectgraph.Kind{projectgraph.KindSource}
	if area == "models" {
		kinds = []projectgraph.Kind{projectgraph.KindModel}
	}
	if area == "semantic-models" {
		kinds = []projectgraph.Kind{projectgraph.KindSemanticModel}
	}
	if !h.authorizeAny(w, r, kinds) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	project := projectview.DevelopView{ID: projectID.String(), Title: h.navigationCatalog(r).Project.Title, Description: h.navigationCatalog(r).Project.Description}
	assets = projectview.FilterProjectLandingAssets(assets, activeType, strings.TrimSpace(r.URL.Query().Get("q")))
	writeDocument(w, projectui.ProjectAreaPage(h.navigationCatalog(r), project, assets, area, activeType, r.URL.Query().Get("q"), h.Environment, "", h.csrf(r), h.layout(r)))
	_ = edges
}

func (h *BrowserHandler) Pipelines(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindPipeline}) {
		return
	}
	state := projectui.PipelineMonitorState{Environment: h.Environment, CSRFToken: h.csrf(r)}
	writeDocument(w, projectui.PipelinesPage(h.navigationCatalog(r), state, r.URL.Query().Get("view"), "", h.layout(r)))
}

func (h *BrowserHandler) Connections(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindConnection}) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	assets = projectview.FilterConnections(assets, strings.TrimSpace(r.URL.Query().Get("q")))
	writeDocument(w, projectui.ConnectionsPage(h.navigationCatalog(r), projectID.String(), assets, edges, r.URL.Query().Get("q"), "", h.layout(r)))
}

func (h *BrowserHandler) DataSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeSource))
}

func (h *BrowserHandler) ModelsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeModelTable))
}

func (h *BrowserHandler) SemanticModelsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	h.projectAreaSearch(w, r, string(projectview.AssetTypeSemanticModel))
}

func (h *BrowserHandler) projectAreaSearch(w stdhttp.ResponseWriter, r *stdhttp.Request, typ string) {
	kind, ok := catalogKindForAssetType(typ)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusForbidden), stdhttp.StatusForbidden)
		return
	}
	if !h.authorizeAny(w, r, []projectgraph.Kind{kind}) {
		return
	}
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("projectAssetQuery"))
	if query == "" {
		query = strings.TrimSpace(r.FormValue("projectAssetQuery"))
	}
	patch := projectui.ProjectAssetListResultsPatch(projectID.String(), projectview.FilterProjectLandingAssets(assets, typ, query), edges)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) ConnectionsSearch(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindConnection}) {
		return
	}
	_, assets, edges, ok := h.assets(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("entityListQuery"))
	if query == "" {
		query = strings.TrimSpace(r.FormValue("entityListQuery"))
	}
	patch := projectui.ConnectionsListResultsPatch(projectview.FilterConnections(assets, query), edges)
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) Updates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
		return
	}
	patch := map[string]any{"status": projectsignals.DashboardStatus{}, "runtime": projectsignals.RouteRuntimeSignal{Kind: projectsignals.RouteKindData}}
	switch uitransport.Route(r) {
	case "catalog":
		patch = projectui.CatalogBootstrapSignals(h.navigationCatalog(r), h.layout(r))
	case "data":
		surface := r.URL.Query().Get("surface")
		if surface == "explore" {
			page, explorer := h.dataExplorerSignals(r)
			patch = projectui.DataExplorerBootstrapSignals(h.navigationCatalog(r), page, explorer, h.layout(r))
		} else if surface == "asset" {
			if assetPatch, ok := h.assetBootstrap(w, r); ok {
				patch = assetPatch
			}
		} else if projectPatch, ok := h.projectBootstrap(w, r); ok {
			patch = projectPatch
		}
	case "connections":
		if connectionPatch, ok := h.connectionsBootstrap(w, r); ok {
			patch = connectionPatch
		}
	case "pipelines":
		if pipelinePatch, ok := h.pipelinesBootstrap(w, r); ok {
			patch = pipelinePatch
		}
	case "asset", "connection_asset":
		if assetPatch, ok := h.assetBootstrap(w, r); ok {
			patch = assetPatch
		}
	}
	_ = uitransport.PatchOnce(h.Trace, w, r, pagestream.SignalPatch(patch))
}

func (h *BrowserHandler) projectBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, _, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	area := strings.TrimSpace(r.URL.Query().Get("area"))
	if area == "" {
		area = "data"
	}
	activeType := projectAreaType(area)
	assets = projectview.FilterProjectLandingAssets(assets, activeType, r.URL.Query().Get("q"))
	catalog := h.navigationCatalog(r)
	project := projectview.DevelopView{ID: projectID.String(), Title: catalog.Project.Title, Description: catalog.Project.Description}
	return projectui.ProjectBootstrapSignalsForArea(catalog, project, assets, area, activeType, r.URL.Query().Get("q"), h.Environment, "", h.layout(r)), true
}

func (h *BrowserHandler) connectionsBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	assets = projectview.FilterConnections(assets, r.URL.Query().Get("q"))
	return projectui.ConnectionsBootstrapSignalsForEnvironment(h.navigationCatalog(r), projectID.String(), assets, edges, r.URL.Query().Get("q"), h.Environment, "", h.layout(r)), true
}

func (h *BrowserHandler) pipelinesBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	_, assets, _, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	pipelines := projectview.FilterProjectLandingAssets(assets, string(projectview.AssetTypeRefreshPipeline), "")
	state := projectui.PipelineMonitorState{Environment: h.Environment, CSRFToken: h.csrf(r), Pipelines: make([]projectui.PipelineMonitorPipeline, 0, len(pipelines))}
	for _, asset := range pipelines {
		state.Pipelines = append(state.Pipelines, projectui.PipelineMonitorPipeline{Asset: asset})
	}
	return projectui.PipelinesBootstrapSignals(h.navigationCatalog(r), state, r.URL.Query().Get("view"), "", h.layout(r)), true
}

func (h *BrowserHandler) assetBootstrap(w stdhttp.ResponseWriter, r *stdhttp.Request) (map[string]any, bool) {
	projectID, assets, edges, ok := h.assets(w, r)
	if !ok {
		return nil, false
	}
	asset, found := projectview.AssetByID(assets, r.URL.Query().Get("asset"))
	if !found {
		stdhttp.NotFound(w, r)
		return nil, false
	}
	project := projectview.DevelopView{ID: projectID.String(), Title: h.navigationCatalog(r).Project.Title, Description: h.navigationCatalog(r).Project.Description}
	if r.URL.Query().Get("surface") == "asset" {
		return projectui.ProjectAssetBootstrapSignalsForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, r.URL.Query().Get("section"), h.Environment, "", projectui.AssetRefreshState{}, projectui.AssetVersionsState{}, h.layout(r)), true
	}
	return projectui.ConnectionAssetBootstrapSignalsForEnvironment(h.navigationCatalog(r), project, asset, assets, edges, r.URL.Query().Get("section"), h.Environment, "", projectui.AssetVersionsState{}), true
}

// ProtectStream applies the same authenticated subject resolution used by the
// document surface to the canonical project SSE stream.
func (h *BrowserHandler) ProtectStream(next stdhttp.Handler) stdhttp.Handler {
	if h == nil || next == nil {
		return stdhttp.NotFoundHandler()
	}
	protected := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindConnection, projectgraph.KindDashboard}) {
			return
		}
		next.ServeHTTP(w, r)
	})
	if h.Authenticate != nil {
		return h.Authenticate(protected)
	}
	return protected
}

func (h *BrowserHandler) assets(w stdhttp.ResponseWriter, r *stdhttp.Request) (projectgraph.ResourceID, []projectview.DevelopAssetView, []projectview.DevelopEdgeView, bool) {
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	if h.Graph == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	graph, ok, err := h.Graph.ActiveServingStateGraph(r.Context(), projectID, h.Environment)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	if h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return "", nil, nil, false
	}
	principal, authenticated := h.CurrentUser(r)
	if !authenticated {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return "", nil, nil, false
	}
	if !principal.DevBypass {
		if h.Catalog == nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return "", nil, nil, false
		}
		allowedPage, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindDashboard, projectgraph.KindPipeline})
		if err != nil {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
			return "", nil, nil, false
		}
		allowed := make(map[projectgraph.ResourceID]struct{}, len(allowedPage.Items))
		for _, item := range allowedPage.Items {
			allowed[item.Ref.ID] = struct{}{}
		}
		filteredAssets := make([]servingstate.Asset, 0, len(graph.Assets))
		for _, asset := range graph.Assets {
			_, mapped := catalogKindForAssetType(asset.Type)
			if !mapped {
				continue
			}
			if _, visible := allowed[asset.ID]; visible {
				filteredAssets = append(filteredAssets, asset)
				continue
			}
		}
		graph.Assets = filteredAssets
		visibleIDs := make(map[projectgraph.ResourceID]struct{}, len(graph.Assets))
		for _, asset := range graph.Assets {
			visibleIDs[asset.ID] = struct{}{}
		}
		filteredEdges := make([]servingstate.AssetEdge, 0, len(graph.Edges))
		for _, edge := range graph.Edges {
			if _, from := visibleIDs[edge.FromAssetID]; !from {
				continue
			}
			if _, to := visibleIDs[edge.ToAssetID]; !to {
				continue
			}
			filteredEdges = append(filteredEdges, edge)
		}
		graph.Edges = filteredEdges
	}
	projectGraph := projectview.DevelopAssetGraph{Assets: make([]projectview.Asset, 0, len(graph.Assets)), Edges: make([]projectview.AssetEdge, 0, len(graph.Edges))}
	for _, asset := range graph.Assets {
		projectGraph.Assets = append(projectGraph.Assets, projectview.Asset{ID: projectview.AssetID(asset.ID), SnapshotID: projectview.AssetSnapshotID(asset.SnapshotID), ProjectID: asset.ProjectID, ServingStateID: projectview.ServingStateID(asset.ServingStateID), Type: projectview.AssetType(asset.Type), Key: asset.Key, ParentID: projectview.AssetID(asset.ParentID), Title: asset.Title, Description: asset.Description, SourceFile: asset.SourceFile, PayloadSchema: asset.PayloadSchema, PayloadJSON: asset.PayloadJSON, ContentHash: asset.ContentHash})
	}
	for _, edge := range graph.Edges {
		projectGraph.Edges = append(projectGraph.Edges, projectview.AssetEdge{ID: projectview.AssetEdgeID(edge.ID), ProjectID: edge.ProjectID, ServingStateID: projectview.ServingStateID(edge.ServingStateID), FromAssetID: projectview.AssetID(edge.FromAssetID), ToAssetID: projectview.AssetID(edge.ToAssetID), Type: projectview.AssetEdgeType(edge.Type)})
	}
	decoded, err := projectview.DecodeDevelopCatalog(projectGraph)
	if err != nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusInternalServerError), stdhttp.StatusInternalServerError)
		return "", nil, nil, false
	}
	assets := make([]projectview.DevelopAssetView, 0, len(decoded.Assets))
	for _, asset := range decoded.Assets {
		assets = append(assets, projectview.DevelopAssetViewFromCatalogRecord(asset))
	}
	edges := make([]projectview.DevelopEdgeView, 0, len(decoded.Edges))
	for _, edge := range decoded.Edges {
		edges = append(edges, projectview.DevelopEdgeViewFromCatalogRecord(edge))
	}
	return projectID, assets, edges, true
}

func (h *BrowserHandler) authorizeAny(w stdhttp.ResponseWriter, r *stdhttp.Request, kinds []projectgraph.Kind) bool {
	if h.CurrentUser == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusUnauthorized), stdhttp.StatusUnauthorized)
		return false
	}
	if principal.DevBypass {
		return true
	}
	if _, err := h.boundProject(r.Context()); err != nil || h.Catalog == nil {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	selector := strings.TrimSpace(r.URL.Query().Get("asset"))
	if selector == "" {
		selector = strings.TrimSpace(r.URL.Query().Get("model"))
	}
	if selector == "" {
		selector = strings.TrimSpace(chi.URLParam(r, "asset"))
	}
	if selector != "" {
		for _, kind := range kinds {
			if _, err := h.Catalog.Resolve(r.Context(), principal.ID, projectcatalog.Ref{ID: projectgraph.ResourceID(selector), Kind: kind}, access.CapabilityResourceRead, principal.DevBypass); err == nil {
				return true
			}
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusForbidden), stdhttp.StatusForbidden)
		return false
	}
	page, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, kinds)
	if err != nil {
		if errors.Is(err, projectcatalog.ErrNotFound) {
			stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusForbidden), stdhttp.StatusForbidden)
			return false
		}
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusServiceUnavailable), stdhttp.StatusServiceUnavailable)
		return false
	}
	if len(page.Items) == 0 {
		stdhttp.Error(w, stdhttp.StatusText(stdhttp.StatusForbidden), stdhttp.StatusForbidden)
		return false
	}
	return true
}

func (h *BrowserHandler) boundProject(ctx context.Context) (projectgraph.ResourceID, error) {
	if h.ResolveProjectID == nil {
		return "", errors.New("active project resolver is unavailable")
	}
	projectID, err := h.ResolveProjectID(ctx)
	if err != nil {
		return "", err
	}
	if err := projectID.Validate(); err != nil {
		return "", fmt.Errorf("active project: %w", err)
	}
	return projectID, nil
}

func (h *BrowserHandler) navigationCatalog(r *stdhttp.Request) projectnavigation.Catalog {
	if h.Catalog == nil || h.CurrentUser == nil {
		return projectnavigation.Catalog{}
	}
	principal, ok := h.CurrentUser(r)
	if !ok {
		return projectnavigation.Catalog{}
	}
	page, err := listCatalogAll(r.Context(), h.Catalog, principal.ID, principal.DevBypass, []projectgraph.Kind{projectgraph.KindProject, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindDashboard})
	if err != nil {
		return projectnavigation.Catalog{}
	}
	projectID, err := h.boundProject(r.Context())
	if err != nil {
		return projectnavigation.Catalog{}
	}
	out := projectnavigation.Catalog{Project: projectnavigation.Project{ID: projectID.String(), Title: projectID.String()}}
	for _, item := range page.Items {
		switch item.Ref.Kind {
		case projectgraph.KindProject:
			out.Project = projectnavigation.Project{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description}
		case projectgraph.KindModel:
			out.Models = append(out.Models, projectnavigation.Model{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		case projectgraph.KindSemanticModel:
			out.SemanticModels = append(out.SemanticModels, projectnavigation.Model{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		case projectgraph.KindDashboard:
			out.Dashboards = append(out.Dashboards, projectnavigation.Dashboard{ID: item.Ref.ID.String(), Title: browserFirstNonEmpty(item.DisplayName, item.Name, item.Ref.ID.String()), Description: item.Description})
		}
	}
	return out
}

func listCatalogAll(ctx context.Context, catalog CatalogAuthorizer, principalID string, devAuthBypass bool, kinds []projectgraph.Kind) (projectcatalog.Page, error) {
	if catalog == nil {
		return projectcatalog.Page{}, projectcatalog.ErrUnavailable
	}
	items := make([]projectcatalog.Result, 0)
	cursor := ""
	seenCursors := map[string]struct{}{}
	for pages := 0; ; pages++ {
		if pages >= 10000 {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination exceeded safety bound")
		}
		page, err := catalog.List(ctx, projectcatalog.ListRequest{PrincipalID: principalID, DevAuthBypass: devAuthBypass, Kinds: kinds, Limit: projectcatalog.MaxLimit, Cursor: cursor})
		if err != nil {
			return projectcatalog.Page{}, err
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			return projectcatalog.Page{Items: items}, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return projectcatalog.Page{}, fmt.Errorf("catalog pagination cursor repeated")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func browserFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func catalogKindForAssetType(typ string) (projectgraph.Kind, bool) {
	switch typ {
	case string(projectview.AssetTypeConnection):
		return projectgraph.KindConnection, true
	case string(projectview.AssetTypeSource):
		return projectgraph.KindSource, true
	case string(projectview.AssetTypeModelTable):
		return projectgraph.KindModel, true
	case string(projectview.AssetTypeSemanticModel):
		return projectgraph.KindSemanticModel, true
	case string(projectview.AssetTypeDashboard):
		return projectgraph.KindDashboard, true
	case string(projectview.AssetTypeRefreshPipeline):
		return projectgraph.KindPipeline, true
	default:
		return "", false
	}
}

func projectAreaType(area string) string {
	switch strings.TrimSpace(area) {
	case "models":
		return string(projectview.AssetTypeModelTable)
	case "semantic-models":
		return string(projectview.AssetTypeSemanticModel)
	default:
		return string(projectview.AssetTypeSource)
	}
}

func (h *BrowserHandler) dataExplorerSignals(r *stdhttp.Request) (projectsignals.DataExplorerPageSignal, projectsignals.DataExplorerSignal) {
	project := h.navigationCatalog(r).Project
	page := projectsignals.DataExplorerPageSignal{Kind: projectsignals.RouteKindData, Title: "Data Explorer", Description: projectsignals.Optional("Explore governed semantic data."), Tabs: []projectsignals.ResourceTabSignal{}, Context: projectsignals.DataExplorerContextSignal{Active: true, Environment: h.Environment, ProjectID: project.ID, ProjectTitle: projectsignals.Optional(project.Title)}}
	exploreCommand := projectsignals.DataExploreCommand{Dimensions: []string{}, Measures: []string{}, Filters: []projectsignals.DataExploreFilterSignal{}, Sort: []projectsignals.DataExploreSortSignal{}, Limit: 100}
	explorer := projectsignals.DataExplorerSignal{Command: projectsignals.DataExplorerCommand{Explore: &exploreCommand, Limit: 100}, Explore: projectsignals.DataExploreSignal{Command: exploreCommand, Models: []projectsignals.DataExploreModelSignal{}, Datasets: []projectsignals.DataExploreDatasetSignal{}, Fields: []projectsignals.DataExploreFieldSignal{}, Result: projectsignals.DataExploreResultSignal{Columns: []projectsignals.DataPreviewColumnSignal{}, Rows: []map[string]any{}, Warnings: []string{}}}, Objects: []projectsignals.DataExplorerObjectSignal{}, Preview: projectsignals.DataPreviewSignal{Blocks: map[string]projectsignals.DataPreviewBlockSignal{}, Columns: []projectsignals.DataPreviewColumnSignal{}, ChunkSize: 100, RowHeight: 32}}
	for _, model := range h.navigationCatalog(r).SemanticModels {
		explorer.Explore.Models = append(explorer.Explore.Models, projectsignals.DataExploreModelSignal{ID: model.ID, Title: model.Title, Description: projectsignals.Optional(model.Description)})
	}
	if len(explorer.Explore.Models) > 0 {
		selected := explorer.Explore.Models[0]
		explorer.Explore.SelectedModel = &selected
		exploreCommand.ModelID = projectsignals.Optional(selected.ID)
		explorer.Command.Explore.ModelID = exploreCommand.ModelID
	}
	return page, explorer
}

func (h *BrowserHandler) layout(r *stdhttp.Request) webpage.Provider {
	if h.Layout == nil {
		return nil
	}
	return h.Layout(r)
}

func (h *BrowserHandler) csrf(r *stdhttp.Request) string {
	if h.CSRFToken == nil {
		return ""
	}
	return h.CSRFToken(r)
}

func writeDocument(w stdhttp.ResponseWriter, node g.Node) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := node.Render(w); err != nil {
		return
	}
}
