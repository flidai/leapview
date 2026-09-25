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

// PipelineWaitingIntent is the browser adapter's request-stage read model.
// The app composition root maps refresh-module records into this transport
// shape without importing the project's UI implementation.
type PipelineWaitingIntent struct {
	IntentID      string
	PipelineID    string
	Status        string
	CreatedAt     string
	Reason        string
	QueuePosition int64
	RunID         string
	CancelAllowed bool
}

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
		publicationStatus := "none"
		var publicationAt time.Time
		if h.RunPublicationReader == nil || refresh.Unavailable {
			publicationStatus = "unavailable"
		} else if refresh.LatestSuccessful.ID != "" {
			evidence, found, publicationErr := h.RunPublicationReader.RunPublication(r.Context(), refreshrun.ReadScope{ProjectID: projectID, Environment: h.Environment}, refresh.LatestSuccessful.ID)
			if publicationErr != nil {
				publicationStatus = "unavailable"
			} else if found {
				publicationStatus = "confirmed"
				publicationAt = evidence.CommittedAt
			}
		}
		semanticModelID := projectAssetPayloadResourceID(asset.Payload, "SemanticModel", "semanticModel", "SemanticModelID", "semanticModelId")
		semanticModelTitle := pipelineSemanticModelTitle(semanticModelID.String(), assets)
		state.Pipelines = append(state.Pipelines, projectui.PipelineMonitorPipeline{
			Asset: asset, Refresh: refresh,
			CanRun:             canUse && !refresh.Unavailable && state.RunCommand.OperationID() != "",
			CanCancel:          canUse && !refresh.Unavailable && state.CancelCommand.OperationID() != "",
			SemanticModelTitle: semanticModelTitle,
			PublicationAt:      publicationAt, PublicationStatus: publicationStatus,
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
	state.WaitingIntents, err = h.pipelineWaitingIntents(r, projectID, visiblePipelineIDs(pipelines, ""))
	if err != nil {
		return projectui.PipelineMonitorState{}, err
	}
	if pipelineCollectionView(r) == "runs" {
		if h.RunMonitor == nil {
			return projectui.PipelineMonitorState{}, errors.New("run monitor is unavailable")
		}
		filter, rangeLabel, page := pipelineRunMonitorFilter(r, time.Now().UTC())
		selectedPipeline := strings.TrimSpace(r.URL.Query().Get("pipeline"))
		filter.AllowedPipelineIDs = visiblePipelineIDs(pipelines, selectedPipeline)
		for _, pipeline := range pipelines {
			if filter.Search != "" && (strings.Contains(strings.ToLower(pipeline.Title), strings.ToLower(filter.Search)) || strings.Contains(strings.ToLower(pipeline.Key), strings.ToLower(filter.Search))) {
				filter.PipelineIDs = append(filter.PipelineIDs, pipeline.ID)
			}
		}
		result, err := h.RunMonitor.MonitorRuns(r.Context(), projectID, h.Environment, filter)
		if err != nil {
			return projectui.PipelineMonitorState{}, err
		}
		monitor := &projectui.PipelineRunMonitor{Query: filter.Search, Range: rangeLabel, Pipeline: selectedPipeline, Status: filter.Status, Trigger: filter.Trigger,
			Page: page, PageSize: 25, Total: result.Total, Failed: result.Failed, Completed: result.Completed, Active: result.Active}
		for _, run := range result.Runs {
			monitor.Runs = append(monitor.Runs, projectui.PipelineMonitorRun{PipelineID: run.PipelineID.String(), Run: projectui.AssetRefreshRun{
				ID: run.ID, Environment: run.Identity.Environment, PipelineID: run.PipelineID.String(), ModelID: run.SemanticModelID.String(), ServingStateID: run.Identity.GenerationID,
				PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, TriggerType: run.TriggerType,
				ParentRunID: run.ParentRunID, TargetGeneration: run.TargetRevision, Status: run.Status, CreatedAt: run.CreatedAt,
				UpdatedAt: run.UpdatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
			}})
		}
		state.RunMonitor = monitor
	}
	return state, nil
}

func (h *BrowserHandler) pipelineWaitingIntents(r *http.Request, projectID projectgraph.ResourceID, visibleIDs []string) ([]projectui.PipelineWaitingIntent, error) {
	if h.ReadPipelineIntents == nil || len(visibleIDs) == 0 {
		return nil, nil
	}
	intents, err := h.ReadPipelineIntents(r.Context(), refreshrun.ReadScope{ProjectID: projectID, Environment: h.Environment})
	if err != nil {
		return nil, err
	}
	visible := make(map[string]struct{}, len(visibleIDs))
	for _, id := range visibleIDs {
		visible[id] = struct{}{}
	}
	result := make([]projectui.PipelineWaitingIntent, 0, len(intents))
	for _, intent := range intents {
		if _, allowed := visible[intent.PipelineID]; allowed {
			result = append(result, projectui.PipelineWaitingIntent{
				IntentID: intent.IntentID, PipelineID: intent.PipelineID, Status: intent.Status,
				CreatedAt: intent.CreatedAt, Reason: intent.Reason, QueuePosition: intent.QueuePosition,
				RunID: intent.RunID, CancelAllowed: intent.CancelAllowed,
			})
		}
	}
	return result, nil
}

func pipelineSemanticModelTitle(reference string, assets []projectview.DevelopAssetView) string {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return ""
	}
	for _, asset := range assets {
		if asset.Type == string(projectview.AssetTypeSemanticModel) && strings.TrimSpace(asset.ID) == reference {
			return firstProjectText(asset.Title, asset.Key)
		}
	}
	want := normalizeSemanticModelReference(reference)
	for _, asset := range assets {
		if asset.Type != string(projectview.AssetTypeSemanticModel) {
			continue
		}
		if normalizeSemanticModelReference(asset.ID) == want || normalizeSemanticModelReference(asset.Key) == want {
			return firstProjectText(asset.Title, asset.Key)
		}
	}
	return ""
}

func firstProjectText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func normalizeSemanticModelReference(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"semantic-model:", "semantic_model:", "semantic:", "semantic-model/", "semantic_model/", "semantic/"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

func visiblePipelineIDs(pipelines []projectview.DevelopAssetView, selected string) []string {
	ids := make([]string, 0, len(pipelines))
	for _, pipeline := range pipelines {
		if selected == "" || selected == pipeline.ID {
			ids = append(ids, pipeline.ID)
		}
	}
	return ids
}

func pipelineCollectionView(r *http.Request) string {
	if r != nil && r.URL != nil && (r.URL.Path == "/pipelines/runs" || r.URL.Query().Get("view") == "runs") {
		return "runs"
	}
	return "pipelines"
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
	case "queued", "running", "succeeded", "failed", "cancelled", "superseded", "skipped":
	case "prepared":
		status = "running"
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
