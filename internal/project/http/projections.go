package http

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

var errAssetNotFound = errors.New("project asset not found")

// pipelineMonitorState is the single read-model builder used by the initial
// document, stream bootstrap, and post-command refresh paths.
func (h *BrowserHandler) pipelineMonitorState(r *http.Request, projectID projectgraph.ResourceID, assets []projectview.DevelopAssetView) (projectui.PipelineMonitorState, error) {
	pipelines := projectview.FilterProjectLandingAssets(assets, string(projectview.AssetTypeRefreshPipeline), "")
	pipelines = append(pipelines, projectview.FilterProjectLandingAssets(assets, "pipeline", "")...)
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
	seenRuns := make(map[string]struct{})
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
		for _, run := range refresh.Runs {
			if run.ID == "" {
				continue
			}
			if _, seen := seenRuns[run.ID]; seen {
				continue
			}
			seenRuns[run.ID] = struct{}{}
			switch run.Status {
			case "running":
				state.Capacity.Running++
			case "queued":
				state.Capacity.Queued++
			case "prepared":
				state.Capacity.Prepared++
			}
		}
	}
	if r.URL.Path == "/runs" || r.URL.Query().Get("view") == "runs" {
		if h.RunMonitor == nil {
			return projectui.PipelineMonitorState{}, errors.New("run monitor is unavailable")
		}
		filter, rangeLabel, page := pipelineRunMonitorFilter(r, time.Now().UTC())
		filter.AllowedPipelineIDs = make([]string, 0, len(pipelines))
		for _, pipeline := range pipelines {
			filter.AllowedPipelineIDs = append(filter.AllowedPipelineIDs, pipeline.ID)
			if filter.Search != "" && (strings.Contains(strings.ToLower(pipeline.Title), strings.ToLower(filter.Search)) || strings.Contains(strings.ToLower(pipeline.Key), strings.ToLower(filter.Search))) {
				filter.PipelineIDs = append(filter.PipelineIDs, pipeline.ID)
			}
		}
		result, err := h.RunMonitor.MonitorRuns(r.Context(), projectID, h.Environment, filter)
		if err != nil {
			return projectui.PipelineMonitorState{}, err
		}
		monitor := &projectui.PipelineRunMonitor{Query: filter.Search, Range: rangeLabel, Status: filter.Status, Trigger: filter.Trigger,
			Page: page, PageSize: 25, Total: result.Total, Failed: result.Failed, Completed: result.Completed, Active: result.Active}
		for _, run := range result.Runs {
			monitor.Runs = append(monitor.Runs, projectui.PipelineMonitorRun{PipelineID: run.PipelineID.String(), Run: projectui.AssetRefreshRun{
				ID: run.ID, Environment: run.Identity.Environment, ModelID: run.SemanticModelID.String(), ServingStateID: run.Identity.GenerationID,
				PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, TriggerType: run.TriggerType,
				ParentRunID: run.ParentRunID, TargetGeneration: run.TargetRevision, Status: run.Status, CreatedAt: run.CreatedAt,
				UpdatedAt: run.UpdatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
			}})
		}
		state.RunMonitor = monitor
	}
	return state, nil
}

func pipelineRunMonitorFilter(r *http.Request, now time.Time) (refreshrun.MonitorFilter, string, int64) {
	query := r.URL.Query()
	rangeLabel := query.Get("range")
	since := now.Add(-24 * time.Hour)
	switch rangeLabel {
	case "7d":
		since = now.Add(-7 * 24 * time.Hour)
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
	case "all":
		since = time.Unix(0, 0).UTC()
	default:
		rangeLabel = "24h"
	}
	status := strings.ToLower(strings.TrimSpace(query.Get("status")))
	switch status {
	case "queued", "running", "prepared", "succeeded", "failed", "cancelled", "superseded", "skipped":
	default:
		status = ""
	}
	trigger := strings.ToLower(strings.TrimSpace(query.Get("trigger")))
	if trigger != "manual" && trigger != "schedule" {
		trigger = ""
	}
	pageValue, err := strconv.ParseInt(query.Get("page"), 10, 64)
	if err != nil || pageValue < 1 {
		pageValue = 1
	}
	const pageSize int64 = 25
	maxPage := int64(math.MaxInt64/pageSize) + 1
	if pageValue > maxPage {
		pageValue = maxPage
	}
	search := strings.TrimSpace(query.Get("q"))
	if runes := []rune(search); len(runes) > 120 {
		search = string(runes[:120])
	}
	page := pageValue
	return refreshrun.MonitorFilter{Since: since, Until: now.Add(time.Second), Search: search, Status: status, Trigger: trigger, Limit: int(pageSize), Offset: (page - 1) * pageSize}, rangeLabel, page
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
	asset, err = h.enrichAssetRuntimeMetadata(r.Context(), asset)
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
	if asset.Type == string(projectview.AssetTypeRefreshPipeline) || asset.Type == "pipeline" {
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
