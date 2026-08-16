package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/project/api"
	"github.com/flidai/leapview/internal/project/assetnav"
	projectdatastar "github.com/flidai/leapview/internal/project/datastar"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	"github.com/flidai/leapview/internal/project/ui"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	Environment              func(*nethttp.Request) string
	ReadModel                ReadModel
	RefreshState             RefreshStateProvider
	RefreshCapacity          func(context.Context) (ui.PipelineMonitorCapacity, error)
	RefreshRunner            AssetRefreshRunner
	Broker                   *pagestream.Broker
	CSRFToken                func(*nethttp.Request) string
	CurrentRoleLabel         func(*nethttp.Request) string
	ConnectionAdministration connectionadmin.Administration
	ConnectionAuthorize      ConnectionPrivilegeAuthorizer
	ConnectionCommands       ui.ConnectionCommandBindings
	ConnectionTargetID       string
	ActiveProjectID          projectgraph.ResourceID
	AgentBootstrap           func(*nethttp.Request, string) ui.DataExplorerAgentBootstrap
	AgentCommands            ui.DataExplorerAgentCommandBindings
	Layout                   func(*nethttp.Request) webpage.Provider
	AuthorizeObject          func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)
}

type projectAssetFilterSignalPayload struct {
	ProjectAssetType  *string `json:"projectAssetType"`
	ProjectAssetQuery *string `json:"projectAssetQuery"`
}

type entityListSignalPayload struct {
	Query  *string `json:"entityListQuery"`
	Filter *string `json:"entityListFilter"`
}

type pipelineCommandSignalPayload struct {
	PipelineCommand uisignals.PipelineCommandSignal `json:"pipelineCommand"`
}

func (h Handler) Pipelines(w nethttp.ResponseWriter, r *nethttp.Request) {
	state, err := h.pipelineMonitorState(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.PipelinesPage(h.catalogForProjectsPage(r, nil), state, r.URL.Query().Get("view"), h.currentRoleLabel(r), h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) PipelinesBootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	clientID := pagestream.EnsureClientID(w, r)
	var trace *pagestream.TraceStore
	if broker := h.broker(); broker != nil {
		trace = broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, "pipelines:"+clientID, "pipelines.bootstrap"))
	view := r.URL.Query().Get("view")
	state, err := h.pipelineMonitorState(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	if err := updates.Patch(ui.PipelinesBootstrapSignals(h.catalogForProjectsPage(r, nil), state, view, h.currentRoleLabel(r), h.chromeOptions(r)...)); err != nil {
		return
	}

	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
			state, err = h.pipelineMonitorState(r)
			if err != nil {
				if patchErr := updates.Patch(pagestream.SignalPatch{"status": map[string]any{"loading": false, "error": err.Error()}}); patchErr != nil {
					return
				}
				continue
			}
			if err := updates.Patch(ui.PipelinesPagePatch(state, view)); err != nil {
				return
			}
		}
	}
}

func (h Handler) PipelineCommand(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals pipelineCommandSignalPayload
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	command := signals.PipelineCommand
	command.Action = strings.ToLower(strings.TrimSpace(command.Action))
	projectID := h.projectID("")
	command.AssetID = strings.TrimSpace(command.AssetID)
	command.PipelineID = strings.TrimSpace(command.PipelineID)
	command.RunID = strings.TrimSpace(command.RunID)
	if projectID == "" || command.AssetID == "" || command.PipelineID == "" {
		nethttp.Error(w, "pipeline command target is required", nethttp.StatusBadRequest)
		return
	}
	if command.Action != "run" && command.Action != "retry" && command.Action != "cancel" {
		nethttp.Error(w, "unsupported pipeline command", nethttp.StatusBadRequest)
		return
	}
	if command.Action != "run" && command.RunID == "" {
		nethttp.Error(w, "pipeline run id is required", nethttp.StatusBadRequest)
		return
	}
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	asset, ok := project.AssetByID(assets, command.AssetID)
	if !ok || asset.Type != string(project.AssetTypeRefreshPipeline) || asset.Key != command.PipelineID {
		nethttp.Error(w, "pipeline was not found", nethttp.StatusNotFound)
		return
	}
	privilege := access.PrivilegeRefreshData
	if command.Action == "cancel" {
		privilege = access.PrivilegeUseProject
	}
	allowed, err := h.canPipelineCommand(r, projectID, privilege)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	if !allowed {
		nethttp.Error(w, "pipeline command is not permitted", nethttp.StatusForbidden)
		return
	}
	if h.RefreshRunner == nil {
		nethttp.Error(w, "pipeline command service is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	input := AssetRefreshInput{Request: r, ProjectID: projectID, Asset: asset, Assets: assets, Edges: edges}
	var message string
	switch command.Action {
	case "run":
		err = h.RefreshRunner.RefreshAsset(r.Context(), input)
		message = "Pipeline run queued."
	case "retry":
		err = h.RefreshRunner.RetryAsset(r.Context(), input, command.RunID)
		message = "Pipeline retry queued."
	case "cancel":
		err = h.RefreshRunner.CancelRefreshRun(r.Context(), PipelineRunCancelInput{Request: r, ProjectID: projectID, PipelineID: command.PipelineID, RunID: command.RunID})
		message = "Pipeline run cancelled."
	}
	status := uisignals.PipelineCommandStatusSignal{Message: message}
	if err != nil {
		status.Message = ""
		status.Error = err.Error()
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"pipelineCommandStatus": status})
}

func (h Handler) canPipelineCommand(r *nethttp.Request, projectID string, privilege access.Privilege) (bool, error) {
	principal, ok := h.ReadModel.currentPrincipal(r)
	if !ok {
		return !h.ReadModel.AuthConfigured, nil
	}
	if principal.DevBypass {
		return true, nil
	}
	if h.AuthorizeObject == nil {
		return false, nil
	}
	return h.AuthorizeObject(r.Context(), principal.ID, privilege, access.ProjectObject(projectID))
}

func (h Handler) pipelineMonitorState(r *nethttp.Request) (ui.PipelineMonitorState, error) {
	projectID := h.projectID("")
	state := ui.PipelineMonitorState{Environment: h.environment(r), CSRFToken: h.csrfToken(r)}
	var err error
	if h.RefreshCapacity != nil {
		state.Capacity, err = h.RefreshCapacity(r.Context())
		if err != nil {
			return ui.PipelineMonitorState{}, err
		}
	}
	assets, _, assetsErr := h.assetsAndEdges(r, projectID)
	if assetsErr != nil {
		return ui.PipelineMonitorState{}, assetsErr
	}
	for _, asset := range assets {
		if asset.Type != "refresh_pipeline" {
			continue
		}
		refresh, refreshErr := h.assetRefreshState(r.Context(), projectID, state.Environment, asset)
		if refreshErr != nil {
			return ui.PipelineMonitorState{}, refreshErr
		}
		canRun, authErr := h.canPipelineCommand(r, projectID, access.PrivilegeRefreshData)
		if authErr != nil {
			return ui.PipelineMonitorState{}, authErr
		}
		canCancel, authErr := h.canPipelineCommand(r, projectID, access.PrivilegeUseProject)
		if authErr != nil {
			return ui.PipelineMonitorState{}, authErr
		}
		if state.RunCommand.OperationID() == "" {
			state.RunCommand = refresh.RunCommand
			state.CancelCommand = refresh.CancelCommand
		}
		state.Pipelines = append(state.Pipelines, ui.PipelineMonitorPipeline{Asset: asset, Refresh: refresh, CanRun: canRun, CanCancel: canCancel})
	}
	return state, nil
}

func (h Handler) ProjectAssetSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals projectAssetFilterSignalPayload
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	projectID := h.projectID("")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType, query := "", ""
	if signals.ProjectAssetType != nil {
		activeType = strings.TrimSpace(*signals.ProjectAssetType)
	}
	if signals.ProjectAssetQuery != nil {
		query = strings.TrimSpace(*signals.ProjectAssetQuery)
	}
	filtered := project.FilterProjectLandingAssets(assets, activeType, query)
	_ = pagestream.PatchResponse(w, r, ui.ProjectAssetListResultsPatch(projectID, filtered, edges))
}

func (h Handler) ConnectionsSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals entityListSignalPayload
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	query := ""
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	filtered := project.FilterConnections(assets, query)
	administration := h.connectionAdministrationView(r, assets, edges, uisignals.ConnectionAdministrationStatusSignal{})
	_ = pagestream.PatchResponse(w, r, ui.ConnectionsListResultsPatchWithAdministration(filtered, edges, administration))
}

func (h Handler) ProjectAssets(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	switch r.URL.Query().Get("type") {
	case "connection":
		nethttp.Redirect(w, r, assetnav.ConnectionsHref(r.URL.Query().Get("q")), nethttp.StatusFound)
		return
	case "source":
		nethttp.Redirect(w, r, assetnav.ConnectionsHref(""), nethttp.StatusFound)
		return
	}
	assets, _, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	filtered := project.FilterProjectLandingAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	projectView := h.projectResponse(r, projectID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ProjectPageForEnvironment(h.catalogForProject(projectID), projectView, filtered, r.URL.Query().Get("type"), r.URL.Query().Get("q"), h.environment(r), h.currentRoleLabel(r), h.csrfToken(r), h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) ProjectAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assetID := chi.URLParam(r, "asset")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := project.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if selected.Type == string(project.AssetTypeConnection) {
		nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(assetID, "details"), nethttp.StatusFound)
		return
	}
	nethttp.Redirect(w, r, assetnav.CanonicalAssetSectionHref(selected, "details"), nethttp.StatusFound)
}

func (h Handler) ProjectAssetSection(w nethttp.ResponseWriter, r *nethttp.Request) {
	section := chi.URLParam(r, "section")
	redirectToDetails := false
	if section == "definition" {
		section = "details"
		redirectToDetails = true
	}
	if !ui.ValidProjectAssetSection(section) {
		nethttp.NotFound(w, r)
		return
	}
	projectID := h.projectID("")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	assetID := chi.URLParam(r, "asset")
	selected, ok := project.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if section == "refreshes" && !projectAssetRefreshable(selected) {
		nethttp.NotFound(w, r)
		return
	}
	if section == "data" {
		if selected.Type != string(project.AssetTypeSemanticModel) && selected.Type != string(project.AssetTypeModelTable) && selected.Type != string(project.AssetTypeSource) {
			nethttp.NotFound(w, r)
			return
		}
		values := url.Values{}
		values.Set("object", assetID)
		nethttp.Redirect(w, r, "/explore?"+values.Encode(), nethttp.StatusFound)
		return
	}
	if selected.Type == string(project.AssetTypeConnection) {
		nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(assetID, section), nethttp.StatusFound)
		return
	}
	if redirectToDetails {
		nethttp.Redirect(w, r, assetnav.CanonicalAssetSectionHref(selected, "details"), nethttp.StatusFound)
		return
	}
	refresh, err := h.assetRefreshState(r.Context(), projectID, h.environment(r), selected)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	refresh.CSRFToken = h.csrfToken(r)
	versions, err := h.assetVersionsState(r.Context(), projectID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ProjectAssetPageWithRefreshAndVersionsForEnvironment(h.catalogForProject(projectID), h.projectResponse(r, projectID), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), refresh, versions, h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) Connections(w nethttp.ResponseWriter, r *nethttp.Request) {
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	filtered := project.FilterConnections(assets, r.URL.Query().Get("q"))
	administration := h.connectionAdministrationView(r, assets, edges, uisignals.ConnectionAdministrationStatusSignal{})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ConnectionsPageWithAdministrationForEnvironment(h.catalogForProjectsPage(r, nil), "platform", filtered, edges, r.URL.Query().Get("q"), h.environment(r), h.currentRoleLabel(r), h.csrfToken(r), administration, h.ConnectionCommands, h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) ProjectBootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	if strings.TrimSpace(projectID) == "" {
		nethttp.Error(w, "active project is unavailable", nethttp.StatusServiceUnavailable)
		return
	}
	assets, _, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType := r.URL.Query().Get("type")
	query := r.URL.Query().Get("q")
	var filterSignals projectAssetFilterSignalPayload
	if err := pagestream.ReadSignals(r, &filterSignals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if filterSignals.ProjectAssetType != nil {
		activeType = strings.TrimSpace(*filterSignals.ProjectAssetType)
	}
	if filterSignals.ProjectAssetQuery != nil {
		query = strings.TrimSpace(*filterSignals.ProjectAssetQuery)
	}
	filtered := project.FilterProjectLandingAssets(assets, activeType, query)
	projectView := h.projectResponse(r, projectID)
	h.patchAndWait(w, r, ui.ProjectBootstrapSignalsForEnvironment(h.catalogForProject(projectID), projectView, filtered, activeType, query, h.environment(r), h.currentRoleLabel(r), h.chromeOptions(r)...))
}

func (h Handler) ConnectionsBootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	query := r.URL.Query().Get("q")
	var listSignals struct {
		Query  *string `json:"entityListQuery"`
		Filter *string `json:"entityListFilter"`
	}
	if err := pagestream.ReadSignals(r, &listSignals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if listSignals.Query != nil {
		query = strings.TrimSpace(*listSignals.Query)
	}
	filtered := project.FilterConnections(assets, query)
	administration := h.connectionAdministrationView(r, assets, edges, uisignals.ConnectionAdministrationStatusSignal{})
	h.patchAndWait(w, r, ui.ConnectionsBootstrapSignalsWithAdministrationForEnvironment(h.catalogForProjectsPage(r, nil), "platform", filtered, edges, query, h.environment(r), h.currentRoleLabel(r), administration, h.chromeOptions(r)...))
}

func (h Handler) ConnectionAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(chi.URLParam(r, "asset"), "details"), nethttp.StatusFound)
}

func (h Handler) ConnectionAssetSection(w nethttp.ResponseWriter, r *nethttp.Request) {
	section := chi.URLParam(r, "section")
	if section == "definition" {
		nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(chi.URLParam(r, "asset"), "details"), nethttp.StatusFound)
		return
	}
	if !ui.ValidProjectAssetSection(section) {
		nethttp.NotFound(w, r)
		return
	}
	if section == "refreshes" || section == "data" {
		nethttp.NotFound(w, r)
		return
	}
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := project.AssetByID(assets, chi.URLParam(r, "asset"))
	if !ok || selected.Type != string(project.AssetTypeConnection) {
		nethttp.NotFound(w, r)
		return
	}
	versions, err := h.assetVersionsState(r.Context(), selected.ProjectID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	administration := h.connectionAdministrationView(r, assets, edges, uisignals.ConnectionAdministrationStatusSignal{})
	if err := ui.ConnectionAssetPageWithAdministrationForEnvironment(h.catalogForProjectsPage(r, nil), platformAssetProjectView(), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions, administration, h.ConnectionCommands, h.csrfToken(r), h.chromeOptions(r)).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) Assets(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assets, _, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	filtered := project.FilterProjectAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	if r.URL.Query().Get("include") == "all" {
		filtered = project.FilterAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	}
	filtered, err = h.filterReadableAssets(r, projectID, filtered)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiAssetSummaryDTOs(filtered))
}

func (h Handler) filterReadableAssets(r *nethttp.Request, projectID string, assets []project.DevelopAssetView) ([]project.DevelopAssetView, error) {
	// The active serving catalog is already authorization-filtered by the
	// composition root. Keep this port for callers while avoiding a second
	// mutable project-access lookup in the HTTP layer.
	return assets, nil
}

func (h Handler) filterReadableAssetViewsAndEdges(r *nethttp.Request, projectID string, assets []project.DevelopAssetView, edges []project.DevelopEdgeView) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	filtered, err := h.filterReadableAssets(r, projectID, assets)
	if err != nil {
		return nil, nil, err
	}
	readable := make(map[string]struct{}, len(filtered))
	for _, asset := range filtered {
		readable[asset.ID] = struct{}{}
	}
	filteredEdges := make([]project.DevelopEdgeView, 0, len(edges))
	for _, edge := range edges {
		_, fromReadable := readable[edge.FromAssetID]
		_, toReadable := readable[edge.ToAssetID]
		if fromReadable && toReadable {
			filteredEdges = append(filteredEdges, edge)
		}
	}
	return filtered, filteredEdges, nil
}

func (h Handler) filterReadableAssetGraph(r *nethttp.Request, projectID string, graph project.DevelopAssetGraph) (project.DevelopAssetGraph, error) {
	return graph, nil
}

func (h Handler) ActiveDeploymentGraph(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	graph, err := developGraphFromViews(assets, edges)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	graph, err = h.filterReadableAssetGraph(r, projectID, graph)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	response, err := apiProjectAssetGraphDTO(graph)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func developGraphFromViews(assets []project.DevelopAssetView, edges []project.DevelopEdgeView) (project.DevelopAssetGraph, error) {
	graph := project.DevelopAssetGraph{
		Assets: make([]project.Asset, 0, len(assets)),
		Edges:  make([]project.AssetEdge, 0, len(edges)),
	}
	for _, view := range assets {
		payload, err := json.Marshal(view.Payload)
		if err != nil {
			return project.DevelopAssetGraph{}, err
		}
		projectID, err := projectgraph.NewResourceID(view.ProjectID)
		if err != nil {
			return project.DevelopAssetGraph{}, err
		}
		graph.Assets = append(graph.Assets, project.Asset{
			ID: project.AssetID(view.ID), SnapshotID: project.AssetSnapshotID(view.SnapshotID), ProjectID: projectID,
			ServingStateID: project.ServingStateID(view.ServingStateID), Type: project.AssetType(view.Type), Key: view.Key,
			ParentID: project.AssetID(view.ParentID), Title: view.Title, Description: view.Description,
			SourceFile: view.SourceFile, PayloadSchema: view.PayloadSchema, PayloadJSON: string(payload), ContentHash: view.ContentHash,
		})
	}
	for _, view := range edges {
		projectID, err := projectgraph.NewResourceID(view.ProjectID)
		if err != nil {
			return project.DevelopAssetGraph{}, err
		}
		graph.Edges = append(graph.Edges, project.AssetEdge{
			ID: project.AssetEdgeID(view.ID), ProjectID: projectID, ServingStateID: project.ServingStateID(view.ServingStateID),
			FromAssetID: project.AssetID(view.FromAssetID), ToAssetID: project.AssetID(view.ToAssetID), Type: project.AssetEdgeType(view.Type),
		})
	}
	return graph, nil
}

func (h Handler) Asset(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assetID := firstNonEmpty(chi.URLParam(r, "assetId"), chi.URLParam(r, "asset"))
	assets, _, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	asset, ok := project.AssetByID(assets, assetID)
	if !ok {
		writeJSONError(w, fmt.Errorf("asset %q not found", assetID), nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, apiAssetDTOs([]project.DevelopAssetView{asset})[0])
}

func (h Handler) AssetEdges(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	_, edges, err = h.filterReadableAssetViewsAndEdges(r, projectID, assets, edges)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiAssetEdgeDTOs(edges))
}

func (h Handler) AssetLineage(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assetID := firstNonEmpty(chi.URLParam(r, "assetId"), chi.URLParam(r, "asset"))
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if _, ok := project.AssetByID(assets, assetID); !ok {
		writeJSONError(w, fmt.Errorf("asset %q not found", assetID), nethttp.StatusNotFound)
		return
	}
	_, edges, err = h.filterReadableAssetViewsAndEdges(r, projectID, assets, edges)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	writeJSON(w, nethttp.StatusOK, api.AssetLineageResponse{
		AssetID:    assetID,
		Upstream:   assetLineageEndpointIDs(edges, assetID, true),
		Downstream: assetLineageEndpointIDs(edges, assetID, false),
	})
}

func (h Handler) AssetUpdatesStream(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assetID := firstNonEmpty(chi.URLParam(r, "asset"), r.URL.Query().Get("asset"))
	section := projectdatastar.ProjectAssetUpdateSection(r)
	route := r.URL.Query().Get("route")
	var (
		assets []project.DevelopAssetView
		edges  []project.DevelopEdgeView
		err    error
	)
	if route == string(uisignals.RouteKindConnectionAsset) {
		assets, edges, err = h.platformAssetsAndEdges(r)
	} else {
		assets, edges, err = h.assetsAndEdges(r, projectID)
	}
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := project.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if strings.TrimSpace(projectID) == "" {
		nethttp.Error(w, "active project is unavailable", nethttp.StatusServiceUnavailable)
		return
	}

	streamID := projectdatastar.ProjectAssetStreamID(projectID, assetID, section)
	broker := h.broker()
	var trace *pagestream.TraceStore
	if broker != nil {
		trace = broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, streamID, "project.asset.bootstrap"))
	refresh, err := h.assetRefreshState(r.Context(), projectID, h.environment(r), selected)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	versions, err := h.assetVersionsState(r.Context(), projectID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	var patch pagestream.SignalPatch
	if route == string(uisignals.RouteKindConnectionAsset) {
		administration := h.connectionAdministrationView(r, assets, edges, uisignals.ConnectionAdministrationStatusSignal{})
		patch = ui.ConnectionAssetBootstrapSignalsWithAdministrationForEnvironment(h.catalogForProjectsPage(r, nil), platformAssetProjectView(), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions, administration, h.chromeOptions(r)...)
	} else {
		patch = ui.ProjectAssetBootstrapSignalsForEnvironment(h.catalogForProject(projectID), h.projectResponse(r, projectID), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), refresh, versions, h.chromeOptions(r)...)
	}
	if err := updates.Patch(patch); err != nil {
		return
	}
	if projectAssetRefreshable(selected) {
		if broker != nil {
			_ = updates.Forward(r.Context(), broker, streamID)
			return
		}
		updates.Wait(r.Context())
		return
	}
	updates.Wait(r.Context())
}

func (h Handler) patchAndWait(w nethttp.ResponseWriter, r *nethttp.Request, patch pagestream.SignalPatch) {
	clientID := pagestream.EnsureClientID(w, r)
	broker := h.broker()
	var trace *pagestream.TraceStore
	if broker != nil {
		trace = broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, "project:"+clientID, "project.bootstrap"))
	if err := updates.Patch(patch); err != nil {
		return
	}
	updates.Wait(r.Context())
}

func (h Handler) RefreshAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.refreshAsset(w, r)
}

func (h Handler) refreshAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	projectID := h.projectID("")
	assetID := chi.URLParam(r, "asset")
	assets, edges, err := h.assetsAndEdges(r, projectID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := project.AssetByID(assets, assetID)
	if !ok || !projectAssetRefreshable(selected) {
		nethttp.NotFound(w, r)
		return
	}
	if h.RefreshRunner == nil {
		nethttp.Error(w, "project refresh runner is required", nethttp.StatusServiceUnavailable)
		return
	}
	if err := h.RefreshRunner.RefreshAsset(r.Context(), AssetRefreshInput{
		Request:   r,
		ProjectID: projectID,
		Asset:     selected,
		Assets:    assets,
		Edges:     edges,
	}); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.WriteHeader(nethttp.StatusNoContent)
}

func (h Handler) projectID(_ string) string {
	if h.ActiveProjectID != "" {
		return string(h.ActiveProjectID)
	}
	// Keep a compatibility fallback for callers that have not yet moved their
	// composition wiring to ActiveProjectID.  No request value is consulted.
	return strings.TrimSpace(h.ReadModel.rootCatalog().Project.ID)
}

func (h Handler) environment(r *nethttp.Request) string {
	if h.Environment == nil {
		return ""
	}
	return h.Environment(r)
}

func (h Handler) assetsAndEdges(r *nethttp.Request, projectID string) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	return h.ReadModel.ProjectAssetsAndEdges(r, projectID)
}

func (h Handler) platformAssetsAndEdges(r *nethttp.Request) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	return h.ReadModel.PlatformAssetsAndEdges(r)
}

func (h Handler) projectAssetsAndEdgesForData(ctx context.Context, projectID, environment string) ([]project.DevelopAssetView, []project.DevelopEdgeView, error) {
	assets, edges, err := h.ReadModel.ProjectAssetsAndEdgesForData(ctx, projectID, environment)
	if err != nil {
		return nil, nil, err
	}
	if len(assets) == 0 && len(edges) == 0 {
		return nil, nil, fmt.Errorf("project %q assets were not found", projectID)
	}
	return assets, edges, nil
}

func (h Handler) metricsForProject(projectID string) (Metrics, bool) {
	return h.ReadModel.metricsForProject(projectID)
}

func (h Handler) catalogForProjectsPage(r *nethttp.Request, projects []project.DevelopView) catalog.Catalog {
	return h.ReadModel.CatalogForProjectsPage(r, projects)
}

func (h Handler) catalogForProject(projectID string) catalog.Catalog {
	return h.ReadModel.catalogForProject(projectID)
}

func (h Handler) projectResponse(r *nethttp.Request, projectID string) project.DevelopView {
	return h.ReadModel.ProjectResponse(r, projectID)
}

func (h Handler) assetRefreshState(ctx context.Context, projectID, environment string, asset project.DevelopAssetView) (ui.AssetRefreshState, error) {
	if h.RefreshState == nil || !projectAssetRefreshable(asset) {
		return ui.AssetRefreshState{}, nil
	}
	return h.RefreshState.AssetRefreshState(ctx, projectID, environment, asset)
}

func (h Handler) assetVersionsState(ctx context.Context, projectID, environment string, asset project.DevelopAssetView, section string) (ui.AssetVersionsState, error) {
	if h.RefreshState == nil {
		return ui.AssetVersionsState{CurrentContentHash: asset.ContentHash}, nil
	}
	return h.RefreshState.AssetVersionsState(ctx, projectID, environment, asset, section)
}

func (h Handler) csrfToken(r *nethttp.Request) string {
	if h.CSRFToken == nil {
		return ""
	}
	return h.CSRFToken(r)
}

func (h Handler) currentRoleLabel(r *nethttp.Request) string {
	if h.CurrentRoleLabel == nil {
		return ""
	}
	return h.CurrentRoleLabel(r)
}

func (h Handler) chromeOptions(r *nethttp.Request) []webpage.Provider {
	if h.Layout == nil {
		return nil
	}
	return []webpage.Provider{h.Layout(r)}
}

func (h Handler) broker() *pagestream.Broker {
	if h.Broker != nil {
		return h.Broker
	}
	return nil
}

func platformAssetProjectView() project.DevelopView {
	return project.DevelopView{ID: "platform", Title: "Global assets", Description: "Global connection and source assets."}
}

func projectAssetRefreshable(asset project.DevelopAssetView) bool {
	return asset.Type == string(project.AssetTypeRefreshPipeline)
}

func (h Handler) projectAssetRefreshPatch(r *nethttp.Request, projectID string, asset project.DevelopAssetView, assets []project.DevelopAssetView, edges []project.DevelopEdgeView, section string) pagestream.SignalPatch {
	refresh, err := h.assetRefreshState(r.Context(), projectID, h.environment(r), asset)
	if err != nil {
		refresh = ui.AssetRefreshState{Latest: ui.AssetRefreshRun{Status: "failed"}}
	}
	return pagestream.SignalPatch(projectdatastar.ProjectAssetRefreshSignals(h.projectResponse(r, projectID), asset, assets, edges, refresh, section))
}

func (h Handler) PublishProjectAssetRefreshPatch(r *nethttp.Request, projectID string, asset project.DevelopAssetView, assets []project.DevelopAssetView, edges []project.DevelopEdgeView) {
	broker := h.broker()
	if broker == nil {
		return
	}
	for _, section := range projectdatastar.ProjectAssetRefreshSections() {
		broker.Publish(projectdatastar.ProjectAssetStreamID(projectID, asset.ID, section), h.projectAssetRefreshPatch(r, projectID, asset, assets, edges, section))
	}
}

func (h Handler) PublishProjectAssetRefreshPatchForTarget(r *nethttp.Request, projectID, targetID string, assets []project.DevelopAssetView, edges []project.DevelopEdgeView) {
	for _, asset := range assets {
		if asset.Key == targetID && projectAssetRefreshable(asset) {
			h.PublishProjectAssetRefreshPatch(r, projectID, asset, assets, edges)
		}
	}
}

func apiAssetDTOs(rows []project.DevelopAssetView) []api.AssetResponse {
	out := make([]api.AssetResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetResponse{
			ID:             row.ID,
			SnapshotID:     row.SnapshotID,
			ProjectID:      row.ProjectID,
			ServingStateID: row.ServingStateID,
			Type:           row.Type,
			Key:            row.Key,
			ParentID:       row.ParentID,
			Title:          row.Title,
			Description:    row.Description,
			SourceFile:     row.SourceFile,
			PayloadSchema:  row.PayloadSchema,
			Payload:        row.Payload,
			Href:           row.Href,
		})
	}
	return out
}

func apiAssetSummaryDTOs(rows []project.DevelopAssetView) []api.AssetSummaryResponse {
	out := make([]api.AssetSummaryResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetSummaryResponse{
			ID:             row.ID,
			SnapshotID:     row.SnapshotID,
			ProjectID:      row.ProjectID,
			ServingStateID: row.ServingStateID,
			Type:           row.Type,
			Key:            row.Key,
			ParentID:       row.ParentID,
			Title:          row.Title,
			Description:    row.Description,
			SourceFile:     row.SourceFile,
			PayloadSchema:  row.PayloadSchema,
			ContentHash:    row.ContentHash,
			Href:           row.Href,
		})
	}
	return out
}

func apiProjectAssetGraphDTO(graph project.DevelopAssetGraph) (api.ProjectAssetGraphResponse, error) {
	assets := make([]api.AssetGraphAssetResponse, 0, len(graph.Assets))
	for _, row := range graph.Assets {
		payload := map[string]any{}
		if row.PayloadJSON != "" {
			if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
				return api.ProjectAssetGraphResponse{}, err
			}
		}
		assets = append(assets, api.AssetGraphAssetResponse{
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
			Payload:        payload,
			ContentHash:    row.ContentHash,
		})
	}
	return api.ProjectAssetGraphResponse{
		Assets: assets,
		Edges:  apiProjectAssetGraphEdgeDTOs(graph.Edges),
	}, nil
}

func apiProjectAssetGraphEdgeDTOs(rows []project.AssetEdge) []api.AssetEdgeResponse {
	out := make([]api.AssetEdgeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetEdgeResponse{
			ID:             string(row.ID),
			ProjectID:      string(row.ProjectID),
			ServingStateID: string(row.ServingStateID),
			FromAssetID:    string(row.FromAssetID),
			ToAssetID:      string(row.ToAssetID),
			Type:           string(row.Type),
		})
	}
	return out
}

func apiAssetEdgeDTOs(rows []project.DevelopEdgeView) []api.AssetEdgeResponse {
	out := make([]api.AssetEdgeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetEdgeResponse{
			ID:             row.ID,
			ProjectID:      row.ProjectID,
			ServingStateID: row.ServingStateID,
			FromAssetID:    row.FromAssetID,
			ToAssetID:      row.ToAssetID,
			Type:           row.Type,
		})
	}
	return out
}

func assetLineageEndpointIDs(edges []project.DevelopEdgeView, assetID string, upstream bool) []string {
	values := map[string]struct{}{}
	for _, edge := range edges {
		if upstream && edge.ToAssetID == assetID {
			values[edge.FromAssetID] = struct{}{}
		}
		if !upstream && edge.FromAssetID == assetID {
			values[edge.ToAssetID] = struct{}{}
		}
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func writePagedJSON[T any](w nethttp.ResponseWriter, r *nethttp.Request, items []T) bool {
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return false
	}
	writeJSON(w, nethttp.StatusOK, pagedResponseWithCursor(page, nextCursor))
	return true
}

type pageResponse struct {
	NextCursor string `json:"nextCursor"`
}

func pagedResponseWithCursor(items any, nextCursor string) map[string]any {
	return map[string]any{"items": items, "page": pageResponse{NextCursor: nextCursor}}
}

func pageSliceForRequest[T any](w nethttp.ResponseWriter, r *nethttp.Request, items []T) ([]T, string, bool) {
	limit, ok := apiLimitForRequest(w, r)
	if !ok {
		return nil, "", false
	}
	lastKey, err := decodeKeysetCursor(r.URL.Query().Get("pageToken"))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return nil, "", false
	}
	start := 0
	if lastKey != "" {
		start = -1
		for index, item := range items {
			if projectPageItemKey(item) == lastKey {
				start = index + 1
				break
			}
		}
		if start < 0 {
			writeJSONError(w, fmt.Errorf("cursor serving snapshot is unavailable"), nethttp.StatusConflict)
			return nil, "", false
		}
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeKeysetCursor(projectPageItemKey(items[end-1]))
	}
	return append(make([]T, 0, end-start), items[start:end]...), nextCursor, true
}

const (
	defaultAPILimit = 50
	maxAPILimit     = 200
)

func apiLimitForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (int, bool) {
	limit, err := parseAPILimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func parseAPILimit(value string) (int, error) {
	if value == "" {
		return defaultAPILimit, nil
	}
	var limit int
	if _, err := fmt.Sscanf(value, "%d", &limit); err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if limit < 1 {
		return 0, fmt.Errorf("limit must be at least 1")
	}
	if limit > maxAPILimit {
		return 0, fmt.Errorf("limit must not exceed %d", maxAPILimit)
	}
	return limit, nil
}

func decodeKeysetCursor(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("invalid page token")
	}
	var cursor struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &cursor) != nil || cursor.Key == "" {
		return "", fmt.Errorf("invalid page token")
	}
	return cursor.Key, nil
}

func encodeKeysetCursor(key string) string {
	payload, _ := json.Marshal(map[string]string{"key": key})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func projectPageItemKey(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func writeJSON(w nethttp.ResponseWriter, status int, value any) {
	httptransport.WriteJSON(w, status, value)
}

func writeJSONError(w nethttp.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/problem+json")
	writeJSON(w, status, map[string]any{
		"type": "https://leapview.dev/problems/http-error", "title": nethttp.StatusText(status),
		"status": status, "detail": err.Error(), "instance": "", "code": fmt.Sprintf("HTTP_%d", status),
		"requestId": w.Header().Get("X-Request-ID"), "errors": []any{},
	})
}

func statusForNotFound(err error) int {
	if err == nil {
		return nethttp.StatusInternalServerError
	}
	return nethttp.StatusNotFound
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
