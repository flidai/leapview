package http

import (
	"errors"
	"net/http"

	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
)

var errAssetNotFound = errors.New("project asset not found")

// pipelineMonitorState is the single read-model builder used by the initial
// document, stream bootstrap, and post-command refresh paths.
func (h *BrowserHandler) pipelineMonitorState(r *http.Request, projectID projectgraph.ResourceID, assets []projectview.DevelopAssetView) (projectui.PipelineMonitorState, error) {
	pipelines := projectview.FilterProjectLandingAssets(assets, string(projectview.AssetTypeRefreshPipeline), "")
	pipelines, err := h.projectAssetReadModels(r.Context(), pipelines)
	if err != nil {
		return projectui.PipelineMonitorState{}, err
	}
	state := projectui.PipelineMonitorState{
		Environment:   h.Environment,
		CSRFToken:     h.csrf(r),
		Pipelines:     make([]projectui.PipelineMonitorPipeline, 0, len(pipelines)),
		RunCommand:    h.PipelineRunCommand,
		CancelCommand: h.PipelineCancelCommand,
	}
	for _, asset := range pipelines {
		refresh, refreshErr := h.assetRefreshState(r.Context(), projectID, asset)
		if refreshErr != nil {
			refresh.Unavailable = true
		}
		canUse := h.pipelineMutationAllowed(r, asset.ID)
		state.Pipelines = append(state.Pipelines, projectui.PipelineMonitorPipeline{
			Asset: asset, Refresh: refresh,
			CanRun:    canUse && !refresh.Unavailable && state.RunCommand.OperationID() != "",
			CanCancel: canUse && !refresh.Unavailable && state.CancelCommand.OperationID() != "",
		})
	}
	return state, nil
}

type assetPageProjection struct {
	Catalog  projectnavigation.Catalog
	Project  projectview.DevelopView
	Asset    projectview.DevelopAssetView
	Assets   []projectview.DevelopAssetView
	Edges    []projectview.DevelopEdgeView
	Section  string
	Refresh  projectui.AssetRefreshState
	Versions projectui.AssetVersionsState
}

// assetPageState centralizes the enriched asset projection shared by HTML,
// bootstrap, and mutation responses. Callers retain route/type validation.
func (h *BrowserHandler) assetPageState(r *http.Request, projectID projectgraph.ResourceID, assets []projectview.DevelopAssetView, edges []projectview.DevelopEdgeView, assetID, section string) (assetPageProjection, error) {
	assets, err := h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		return assetPageProjection{}, err
	}
	asset, found := projectview.AssetByID(assets, assetID)
	if !found {
		return assetPageProjection{}, errAssetNotFound
	}
	asset, err = h.enrichModelPhysicalMetadata(r.Context(), asset)
	if err != nil {
		return assetPageProjection{}, err
	}
	versions, err := h.assetVersionsState(r.Context(), projectID, asset, section)
	if err != nil {
		return assetPageProjection{}, err
	}
	refresh := projectui.AssetRefreshState{}
	if section != "definition" {
		refresh, err = h.assetRefreshState(r.Context(), projectID, asset)
		if err != nil {
			refresh = projectui.AssetRefreshState{Unavailable: true}
		}
	}
	if asset.Type == string(projectview.AssetTypeRefreshPipeline) {
		refresh.CanRun = h.pipelineMutationAllowed(r, asset.ID)
	}
	refresh.CSRFToken = h.csrf(r)
	catalog := h.navigationCatalog(r)
	return assetPageProjection{
		Catalog: catalog,
		Project: projectview.DevelopView{
			ID: projectID.String(), Title: catalog.Project.Title, Description: catalog.Project.Description,
		},
		Asset: asset, Assets: assets, Edges: edges, Section: section,
		Refresh: refresh, Versions: versions,
	}, nil
}
