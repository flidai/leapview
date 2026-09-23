package http

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectui "github.com/flidai/leapview/internal/project/ui"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/go-chi/chi/v5"
)

// PipelineDetail renders the purpose-built pipeline detail route. The
// pipeline asset, compiled dependencies, and authored configuration all come
// from the same active serving-generation projection.
func (h *BrowserHandler) PipelineDetail(w http.ResponseWriter, r *http.Request) {
	if !h.authorizeAny(w, r, []projectgraph.Kind{projectgraph.KindPipeline}) {
		return
	}
	section := strings.TrimSpace(chi.URLParam(r, "section"))
	if section == "" {
		section = projectui.PipelineDetailOverview
	}
	if !pipelineDetailSectionValid(section) {
		http.NotFound(w, r)
		return
	}
	assetID, decodeErr := url.PathUnescape(chi.URLParam(r, "asset"))
	if decodeErr != nil {
		http.NotFound(w, r)
		return
	}
	nav, state, err := h.pipelineDetailPageState(r, assetID, section)
	if err != nil {
		writePipelineDetailError(w, r, err)
		return
	}
	html := projectui.PipelineDetailPage(nav, state, "", h.layout(r))
	writeDocument(w, html)
}

// pipelineDetailBootstrap is used by the /updates route after its normal
// resource authorization. It shares the exact projection builder with the
// document handler so its first signal patch cannot drift from page HTML.
func (h *BrowserHandler) pipelineDetailBootstrap(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	if section == "" {
		section = projectui.PipelineDetailOverview
	}
	if !pipelineDetailSectionValid(section) {
		http.NotFound(w, r)
		return nil, false
	}
	nav, state, err := h.pipelineDetailPageState(r, strings.TrimSpace(r.URL.Query().Get("asset")), section)
	if err != nil {
		writePipelineDetailError(w, r, err)
		return nil, false
	}
	return projectui.PipelineDetailBootstrapSignals(nav, state, "", h.layout(r)), true
}

func (h *BrowserHandler) pipelineDetailPageState(r *http.Request, assetID, section string) (projectnavigation.Catalog, projectui.PipelineDetailState, error) {
	projectID, assets, edges, err := h.loadAssets(r)
	if err != nil {
		return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, err
	}
	assets, err = h.projectAssetReadModels(r.Context(), assets)
	if err != nil {
		return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, err
	}
	asset, found := projectview.AssetByID(assets, strings.TrimSpace(assetID))
	if !found || (asset.Type != string(projectview.AssetTypeRefreshPipeline) && asset.Type != "pipeline") {
		return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, errAssetNotFound
	}
	asset, err = h.enrichAssetRuntimeMetadata(r.Context(), asset)
	if err != nil {
		return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, err
	}
	for index := range assets {
		if assets[index].ID == asset.ID {
			assets[index] = asset
			break
		}
	}
	refresh := projectui.AssetRefreshState{}
	refresh, refreshErr := h.assetRefreshState(r.Context(), projectID, asset)
	if refreshErr != nil {
		refresh = projectui.AssetRefreshState{Unavailable: true}
	}
	nav := h.navigationCatalog(r)
	projectTitle := strings.TrimSpace(nav.Project.Title)
	if projectTitle == "" {
		projectTitle = projectID.String()
	}
	project := projectview.DevelopView{ID: projectID.String(), Title: projectTitle, Description: nav.Project.Description}
	state := projectui.PipelineDetailState{
		Project: project, Asset: asset, Assets: assets, Edges: edges, Refresh: refresh,
		Environment: h.Environment, ActiveTab: section, CSRFToken: h.csrf(r), RunCommand: h.PipelineRunCommand,
		CanRun: h.PipelineRunCommand.OperationID() != "" && h.pipelineMutationAllowed(r, asset.ID) && !refresh.Unavailable &&
			refresh.Latest.Status != refreshrun.RunStatusQueued && refresh.Latest.Status != refreshrun.RunStatusRunning && refresh.Latest.Status != refreshrun.RunStatusPrepared,
	}
	if section == projectui.PipelineDetailRuns {
		if h.RunMonitor == nil {
			return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, errors.New("run monitor is unavailable")
		}
		filter, rangeLabel, page := pipelineRunMonitorFilter(r, time.Now().UTC())
		filter.PipelineIDs = []string{asset.ID}
		filter.AllowedPipelineIDs = []string{asset.ID}
		result, monitorErr := h.RunMonitor.MonitorRuns(r.Context(), projectID, h.Environment, filter)
		if monitorErr != nil {
			return projectnavigation.Catalog{}, projectui.PipelineDetailState{}, monitorErr
		}
		state.RunMonitor = &projectui.PipelineRunMonitor{
			Query: filter.Search, Range: rangeLabel, Pipeline: asset.ID, Status: filter.Status, Trigger: filter.Trigger,
			Page: page, PageSize: int32(filter.Limit), Total: result.Total,
		}
		state.MonitorRuns = make([]projectui.PipelineMonitorRun, 0, len(result.Runs))
		for _, run := range result.Runs {
			state.MonitorRuns = append(state.MonitorRuns, projectui.PipelineMonitorRun{
				PipelineID: run.PipelineID.String(),
				Run: projectui.AssetRefreshRun{
					ID: run.ID, Environment: run.Identity.Environment, ModelID: run.SemanticModelID.String(), ServingStateID: run.Identity.GenerationID,
					PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, TriggerType: run.TriggerType,
					ParentRunID: run.ParentRunID, TargetGeneration: run.TargetRevision, Status: run.Status, CreatedAt: run.CreatedAt,
					UpdatedAt: run.UpdatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
				},
			})
		}
	}
	if refresh.LatestSuccessful.ID != "" {
		if h.RunPublicationReader == nil {
			state.PublicationUnavailable = true
		} else {
			evidence, found, publicationErr := h.RunPublicationReader.RunPublication(r.Context(), refreshrun.ReadScope{ProjectID: projectID, Environment: h.Environment}, refresh.LatestSuccessful.ID)
			if publicationErr != nil {
				state.PublicationUnavailable = true
			} else if found {
				state.PublicationRunID = refresh.LatestSuccessful.ID
				state.PublicationServingStateID = evidence.ResultGenerationID
				state.PublicationSnapshotID = evidence.SnapshotID
				state.PublicationAt = evidence.CommittedAt
			}
		}
	}
	return nav, state, nil
}

func pipelineDetailSectionValid(section string) bool {
	switch section {
	case projectui.PipelineDetailOverview, projectui.PipelineDetailRuns, projectui.PipelineDetailDefinition:
		return true
	default:
		return false
	}
}

func writePipelineDetailError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errAssetNotFound) {
		http.NotFound(w, r)
		return
	}
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}
