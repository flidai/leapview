package http

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	accessapi "github.com/flidai/leapview/internal/access/api"
	httptransport "github.com/flidai/leapview/internal/platform/http/transport"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/workspace"
	"github.com/flidai/leapview/internal/workspace/api"
	"github.com/flidai/leapview/internal/workspace/assetnav"
	workspacedatastar "github.com/flidai/leapview/internal/workspace/datastar"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	"github.com/flidai/leapview/internal/workspace/ui"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	WorkspaceID         func(string) string
	Environment         func(*nethttp.Request) string
	ReadModel           ReadModel
	RefreshState        RefreshStateProvider
	RefreshRunner       AssetRefreshRunner
	Broker              *pagestream.Broker
	CSRFToken           func(*nethttp.Request) string
	CurrentRoleLabel    func(*nethttp.Request) string
	RoleBindingCommands access.RoleBindingCommander
	GrantCommands       access.GrantOperations
	AccessCommands      ui.AccessCommandBindings
	AgentBootstrap      func(*nethttp.Request, string) ui.DataExplorerAgentBootstrap
	AgentCommands       ui.DataExplorerAgentCommandBindings
	Layout              func(*nethttp.Request) webpage.Provider
}

type workspaceAccessSignalPayload struct {
	WorkspaceAccess struct {
		Command ui.WorkspaceAccessCommand `json:"command"`
		Search  string                    `json:"search"`
	} `json:"workspaceAccess"`
	WorkspaceAccessCommand ui.WorkspaceAccessCommand `json:"workspaceAccessCommand"`
}

type workspaceAssetFilterSignalPayload struct {
	WorkspaceAssetType  *string `json:"workspaceAssetType"`
	WorkspaceAssetQuery *string `json:"workspaceAssetQuery"`
}

type entityListSignalPayload struct {
	Query  *string `json:"entityListQuery"`
	Filter *string `json:"entityListFilter"`
}

func (h Handler) AccessSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	signals := workspaceAccessSignalPayload{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	workspaceView := h.workspaceResponse(r, workspaceID)
	response := h.workspaceAccess(r, workspaceView, true, ui.WorkspaceAccessStatus{})
	response.Search = strings.TrimSpace(signals.WorkspaceAccess.Search)
	candidates, err := h.ReadModel.WorkspaceAccessCandidates(r, workspaceID, response.Search, 8)
	if err != nil {
		response.SearchStatus.Error = err.Error()
	} else {
		response.Candidates = candidates
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"workspaceAccess": ui.WorkspaceAccessSignals(response)})
}

func (signals workspaceAccessSignalPayload) command() ui.WorkspaceAccessCommand {
	command := signals.WorkspaceAccess.Command
	if command.Email == "" && command.Role == "" && command.PrincipalID == "" && command.BindingID == "" && command.SubjectType == "" && command.SubjectID == "" {
		command = signals.WorkspaceAccessCommand
	}
	return command
}

func (h Handler) WorkspaceCatalog(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaces, err := h.workspaceList(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	workspaces = workspace.FilterWorkspaceViews(workspaces, query)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.WorkspacesPageForEnvironmentQuery(h.catalogForWorkspacesPage(r, workspaces), workspaces, h.environment(r), query, h.currentRoleLabel(r), h.csrfToken(r), h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) WorkspaceListSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals entityListSignalPayload
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	query := ""
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	workspaces, err := h.workspaceList(r)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	workspaces = workspace.FilterWorkspaceViews(workspaces, query)
	_ = pagestream.PatchResponse(w, r, ui.WorkspacesListResultsPatch(workspaces))
}

func (h Handler) WorkspaceAssetSearch(w nethttp.ResponseWriter, r *nethttp.Request) {
	var signals workspaceAssetFilterSignalPayload
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType, query := "", ""
	if signals.WorkspaceAssetType != nil {
		activeType = strings.TrimSpace(*signals.WorkspaceAssetType)
	}
	if signals.WorkspaceAssetQuery != nil {
		query = strings.TrimSpace(*signals.WorkspaceAssetQuery)
	}
	filtered := workspace.FilterWorkspaceLandingAssets(assets, activeType, query)
	_ = pagestream.PatchResponse(w, r, ui.WorkspaceAssetListResultsPatch(workspaceID, filtered, edges))
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
	activeType, query := "", ""
	if signals.Filter != nil {
		activeType = workspace.NormalizeConnectionAssetType(*signals.Filter)
	}
	if signals.Query != nil {
		query = strings.TrimSpace(*signals.Query)
	}
	filtered := workspace.FilterConnectionAssets(assets, activeType, query)
	_ = pagestream.PatchResponse(w, r, ui.ConnectionsListResultsPatch("platform", filtered, edges))
}

func (h Handler) WorkspaceAssets(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	switch r.URL.Query().Get("type") {
	case "connection":
		nethttp.Redirect(w, r, assetnav.ConnectionsHref(r.URL.Query().Get("q")), nethttp.StatusFound)
		return
	case "source":
		nethttp.Redirect(w, r, assetnav.ConnectionsHrefWithType("source", r.URL.Query().Get("q")), nethttp.StatusFound)
		return
	}
	assets, _, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	filtered := workspace.FilterWorkspaceLandingAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	workspaceView := h.workspaceResponse(r, workspaceID)
	access := h.workspaceAccess(r, workspaceView, h.canManageAccess(r, workspaceID), ui.WorkspaceAccessStatus{})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.WorkspacePageForEnvironment(h.catalogForWorkspace(workspaceID), workspaceView, filtered, r.URL.Query().Get("type"), r.URL.Query().Get("q"), h.environment(r), h.currentRoleLabel(r), access, h.csrfToken(r), h.AccessCommands, h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) WorkspaceAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assetID := chi.URLParam(r, "asset")
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := workspace.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if selected.Type == string(workspace.AssetTypeConnection) {
		nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(assetID, "details"), nethttp.StatusFound)
		return
	}
	if selected.Type == string(workspace.AssetTypeSource) {
		nethttp.Redirect(w, r, assetnav.CanonicalSourceAssetSectionHref(workspaceID, selected.ID, "details", edges), nethttp.StatusFound)
		return
	}
	nethttp.Redirect(w, r, "/workspaces/"+workspaceID+"/assets/"+assetID+"/details", nethttp.StatusFound)
}

func (h Handler) WorkspaceAssetSection(w nethttp.ResponseWriter, r *nethttp.Request) {
	section := chi.URLParam(r, "section")
	redirectToDetails := false
	if section == "definition" {
		section = "details"
		redirectToDetails = true
	}
	if !ui.ValidWorkspaceAssetSection(section) {
		nethttp.NotFound(w, r)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	assetID := chi.URLParam(r, "asset")
	selected, ok := workspace.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if section == "refreshes" && !workspaceAssetRefreshable(selected) {
		nethttp.NotFound(w, r)
		return
	}
	if section == "data" {
		if selected.Type != string(workspace.AssetTypeSemanticModel) && selected.Type != string(workspace.AssetTypeModelTable) && selected.Type != string(workspace.AssetTypeSource) {
			nethttp.NotFound(w, r)
			return
		}
		values := url.Values{}
		values.Set("workspace", workspaceID)
		values.Set("object", assetID)
		nethttp.Redirect(w, r, "/data?"+values.Encode(), nethttp.StatusFound)
		return
	}
	if selected.Type == string(workspace.AssetTypeConnection) {
		nethttp.Redirect(w, r, assetnav.ConnectionAssetSectionHref(assetID, section), nethttp.StatusFound)
		return
	}
	if selected.Type == string(workspace.AssetTypeSource) {
		nethttp.Redirect(w, r, assetnav.CanonicalSourceAssetSectionHref(workspaceID, selected.ID, section, edges), nethttp.StatusFound)
		return
	}
	if redirectToDetails {
		nethttp.Redirect(w, r, "/workspaces/"+workspaceID+"/assets/"+assetID+"/details", nethttp.StatusFound)
		return
	}
	refresh, err := h.assetRefreshState(r.Context(), workspaceID, h.environment(r), selected)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	refresh.CSRFToken = h.csrfToken(r)
	versions, err := h.assetVersionsState(r.Context(), workspaceID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.WorkspaceAssetPageWithRefreshAndVersionsForEnvironment(h.catalogForWorkspace(workspaceID), h.workspaceResponse(r, workspaceID), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), refresh, versions, h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) Connections(w nethttp.ResponseWriter, r *nethttp.Request) {
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType := workspace.NormalizeConnectionAssetType(r.URL.Query().Get("type"))
	filtered := workspace.FilterConnectionAssets(assets, activeType, r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ConnectionsPageForEnvironment(h.catalogForWorkspacesPage(r, nil), "platform", filtered, edges, activeType, r.URL.Query().Get("q"), h.environment(r), h.currentRoleLabel(r), h.csrfToken(r), h.chromeOptions(r)...).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) WorkspaceBootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(r.URL.Query().Get("workspace"))
	if strings.TrimSpace(workspaceID) == "" {
		var signals struct {
			Query  *string `json:"entityListQuery"`
			Filter *string `json:"entityListFilter"`
		}
		if err := pagestream.ReadSignals(r, &signals); err != nil {
			nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if signals.Query != nil {
			query = strings.TrimSpace(*signals.Query)
		}
		workspaces, err := h.workspaceList(r)
		if err != nil {
			nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
			return
		}
		workspaces = workspace.FilterWorkspaceViews(workspaces, query)
		h.patchAndWait(w, r, ui.WorkspacesBootstrapSignalsForEnvironmentQuery(h.catalogForWorkspacesPage(r, workspaces), workspaces, h.environment(r), query, h.currentRoleLabel(r), h.chromeOptions(r)...))
		return
	}
	assets, _, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType := r.URL.Query().Get("type")
	query := r.URL.Query().Get("q")
	var filterSignals workspaceAssetFilterSignalPayload
	if err := pagestream.ReadSignals(r, &filterSignals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	if filterSignals.WorkspaceAssetType != nil {
		activeType = strings.TrimSpace(*filterSignals.WorkspaceAssetType)
	}
	if filterSignals.WorkspaceAssetQuery != nil {
		query = strings.TrimSpace(*filterSignals.WorkspaceAssetQuery)
	}
	filtered := workspace.FilterWorkspaceLandingAssets(assets, activeType, query)
	workspaceView := h.workspaceResponse(r, workspaceID)
	access := h.workspaceAccess(r, workspaceView, h.canManageAccess(r, workspaceID), ui.WorkspaceAccessStatus{})
	h.patchAndWait(w, r, ui.WorkspaceBootstrapSignalsForEnvironment(h.catalogForWorkspace(workspaceID), workspaceView, filtered, activeType, query, h.environment(r), h.currentRoleLabel(r), access, h.chromeOptions(r)...))
}

func (h Handler) ConnectionsBootstrapUpdates(w nethttp.ResponseWriter, r *nethttp.Request) {
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	activeType := workspace.NormalizeConnectionAssetType(r.URL.Query().Get("type"))
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
	if listSignals.Filter != nil {
		activeType = workspace.NormalizeConnectionAssetType(*listSignals.Filter)
	}
	filtered := workspace.FilterConnectionAssets(assets, activeType, query)
	h.patchAndWait(w, r, ui.ConnectionsBootstrapSignalsForEnvironment(h.catalogForWorkspacesPage(r, nil), "platform", filtered, edges, activeType, query, h.environment(r), h.currentRoleLabel(r), h.chromeOptions(r)...))
}

func (h Handler) ConnectionSource(w nethttp.ResponseWriter, r *nethttp.Request) {
	connectionID := chi.URLParam(r, "connection")
	sourceID := chi.URLParam(r, "source")
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	if _, _, ok := connectionSourcePair(assets, edges, connectionID, sourceID); !ok {
		nethttp.NotFound(w, r)
		return
	}
	nethttp.Redirect(w, r, assetnav.ConnectionSourceAssetSectionHref(connectionID, sourceID, "details"), nethttp.StatusFound)
}

func (h Handler) ConnectionSourceSection(w nethttp.ResponseWriter, r *nethttp.Request) {
	section := chi.URLParam(r, "section")
	if section == "definition" {
		nethttp.Redirect(w, r, assetnav.ConnectionSourceAssetSectionHref(chi.URLParam(r, "connection"), chi.URLParam(r, "source"), "details"), nethttp.StatusFound)
		return
	}
	if !ui.ValidWorkspaceAssetSection(section) {
		nethttp.NotFound(w, r)
		return
	}
	if section == "refreshes" {
		nethttp.NotFound(w, r)
		return
	}
	assets, edges, err := h.platformAssetsAndEdges(r)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	connection, source, ok := connectionSourcePair(assets, edges, chi.URLParam(r, "connection"), chi.URLParam(r, "source"))
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if section == "data" {
		values := url.Values{}
		values.Set("workspace", source.WorkspaceID)
		values.Set("object", source.ID)
		nethttp.Redirect(w, r, "/data?"+values.Encode(), nethttp.StatusFound)
		return
	}
	versions, err := h.assetVersionsState(r.Context(), source.WorkspaceID, h.environment(r), source, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ConnectionSourceAssetPageWithVersionsForEnvironment(h.catalogForWorkspacesPage(r, nil), platformAssetWorkspaceView(), connection, source, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
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
	if !ui.ValidWorkspaceAssetSection(section) {
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
	selected, ok := workspace.AssetByID(assets, chi.URLParam(r, "asset"))
	if !ok || selected.Type != string(workspace.AssetTypeConnection) {
		nethttp.NotFound(w, r)
		return
	}
	versions, err := h.assetVersionsState(r.Context(), selected.WorkspaceID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(nethttp.StatusOK)
	if err := ui.ConnectionAssetPageWithVersionsForEnvironment(h.catalogForWorkspacesPage(r, nil), platformAssetWorkspaceView(), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions).Render(w); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
	}
}

func (h Handler) Workspaces(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaces, err := h.workspaceList(r)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiWorkspaceDTOs(workspaces))
}

func (h Handler) Assets(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assets, _, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	filtered := workspace.FilterWorkspaceAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	if r.URL.Query().Get("include") == "all" {
		filtered = workspace.FilterAssets(assets, r.URL.Query().Get("type"), r.URL.Query().Get("q"))
	}
	filtered, err = h.filterReadableAssets(r, workspaceID, filtered)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiAssetSummaryDTOs(filtered))
}

func (h Handler) filterReadableAssets(r *nethttp.Request, workspaceID string, assets []workspace.AssetView) ([]workspace.AssetView, error) {
	out := make([]workspace.AssetView, 0, len(assets))
	for _, asset := range assets {
		object, ok := assetObjectForID(workspaceID, asset.ID)
		if !ok {
			out = append(out, asset)
			continue
		}
		allowed, err := h.ReadModel.CanReadObject(r, object)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, asset)
		}
	}
	return out, nil
}

func (h Handler) filterReadableAssetViewsAndEdges(r *nethttp.Request, workspaceID string, assets []workspace.AssetView, edges []workspace.AssetEdgeView) ([]workspace.AssetView, []workspace.AssetEdgeView, error) {
	filtered, err := h.filterReadableAssets(r, workspaceID, assets)
	if err != nil {
		return nil, nil, err
	}
	readable := make(map[string]struct{}, len(filtered))
	for _, asset := range filtered {
		readable[asset.ID] = struct{}{}
	}
	filteredEdges := make([]workspace.AssetEdgeView, 0, len(edges))
	for _, edge := range edges {
		_, fromReadable := readable[edge.FromAssetID]
		_, toReadable := readable[edge.ToAssetID]
		if fromReadable && toReadable {
			filteredEdges = append(filteredEdges, edge)
		}
	}
	return filtered, filteredEdges, nil
}

func (h Handler) filterReadableAssetGraph(r *nethttp.Request, workspaceID string, graph workspace.AssetGraph) (workspace.AssetGraph, error) {
	readable := make(map[workspace.AssetID]struct{}, len(graph.Assets))
	assets := make([]workspace.Asset, 0, len(graph.Assets))
	for _, asset := range graph.Assets {
		object, securable := assetObjectForID(workspaceID, string(asset.ID))
		allowed := true
		var err error
		if securable {
			allowed, err = h.ReadModel.CanReadObject(r, object)
		}
		if err != nil {
			return workspace.AssetGraph{}, err
		}
		if allowed {
			assets = append(assets, asset)
			readable[asset.ID] = struct{}{}
		}
	}
	edges := make([]workspace.AssetEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		_, fromReadable := readable[edge.FromAssetID]
		_, toReadable := readable[edge.ToAssetID]
		if fromReadable && toReadable {
			edges = append(edges, edge)
		}
	}
	return workspace.AssetGraph{Assets: assets, Edges: edges}, nil
}

func (h Handler) ActiveDeploymentGraph(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	repo, err := h.workspaceRepository()
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	graph := workspace.AssetGraph{}
	if repo != nil {
		var ok bool
		graph, ok, err = repo.ActiveServingStateGraph(r.Context(), workspace.WorkspaceID(workspaceID), h.environment(r))
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		if !ok {
			graph = workspace.AssetGraph{}
		}
	}
	graph, err = h.filterReadableAssetGraph(r, workspaceID, graph)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	response, err := apiWorkspaceAssetGraphDTO(graph)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	writeJSON(w, nethttp.StatusOK, response)
}

func (h Handler) Asset(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assetID := firstNonEmpty(chi.URLParam(r, "assetId"), chi.URLParam(r, "asset"))
	assets, _, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	asset, ok := workspace.AssetByID(assets, assetID)
	if !ok {
		writeJSONError(w, fmt.Errorf("asset %q not found", assetID), nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, apiAssetDTOs([]workspace.AssetView{asset})[0])
}

func (h Handler) AssetEdges(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	_, edges, err = h.filterReadableAssetViewsAndEdges(r, workspaceID, assets, edges)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiAssetEdgeDTOs(edges))
}

func (h Handler) AssetLineage(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assetID := firstNonEmpty(chi.URLParam(r, "assetId"), chi.URLParam(r, "asset"))
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		writeJSONError(w, err, statusForNotFound(err))
		return
	}
	if _, ok := workspace.AssetByID(assets, assetID); !ok {
		writeJSONError(w, fmt.Errorf("asset %q not found", assetID), nethttp.StatusNotFound)
		return
	}
	_, edges, err = h.filterReadableAssetViewsAndEdges(r, workspaceID, assets, edges)
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

func (h Handler) Roles(w nethttp.ResponseWriter, r *nethttp.Request) {
	_, roles, err := h.roleBindingsAndRoles(r, h.workspaceID(chi.URLParam(r, "workspace")))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	_ = writePagedJSON(w, r, apiRoleDTOs(roles))
}

func (h Handler) RoleBindings(w nethttp.ResponseWriter, r *nethttp.Request) {
	repo, err := h.accessRepository()
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	if repo == nil {
		_ = writePagedJSON(w, r, []map[string]any{})
		return
	}
	bindings, err := repo.ListRoleBindings(r.Context(), h.workspaceID(chi.URLParam(r, "workspace")))
	if err != nil {
		writeJSONError(w, err, nethttp.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(bindings))
	for _, binding := range bindings {
		out = append(out, apiRoleBindingDTO(binding))
	}
	_ = writePagedJSON(w, r, out)
}

func (h Handler) AccessUpsert(w nethttp.ResponseWriter, r *nethttp.Request) {
	signals := workspaceAccessSignalPayload{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	command := signals.command()
	status := ui.WorkspaceAccessStatus{Message: "Access updated."}
	repo, err := h.accessRepository()
	if err != nil {
		status = ui.WorkspaceAccessStatus{Error: err.Error()}
	} else if repo == nil {
		status = ui.WorkspaceAccessStatus{Error: errWorkspaceAccessNotConfigured.Error()}
	} else if object, ok := assetAccessObject(r, workspaceID); ok {
		subjectType, subjectID, err := h.resolveAccessSubject(r, repo, command)
		if err != nil {
			status = ui.WorkspaceAccessStatus{Error: err.Error()}
		} else if h.GrantCommands == nil {
			status = ui.WorkspaceAccessStatus{Error: "grant commands are unavailable"}
		} else if _, err := h.GrantCommands.CreateGrant(r.Context(), h.grantInvocation(r), access.GrantInput{
			Object:      object,
			SubjectType: subjectType,
			SubjectID:   subjectID,
			Privilege:   access.Privilege(strings.TrimSpace(firstNonEmpty(command.Privilege, command.Role))),
		}); err != nil {
			status = ui.WorkspaceAccessStatus{Error: err.Error()}
		}
	} else {
		subjectType, subjectID, err := h.resolveAccessSubject(r, repo, command)
		if err != nil {
			status = ui.WorkspaceAccessStatus{Error: err.Error()}
		} else {
			input := access.RoleBindingInput{WorkspaceID: workspaceID, SubjectType: subjectType, SubjectID: subjectID, Role: command.Role}
			if h.RoleBindingCommands == nil {
				err = fmt.Errorf("role binding commands are unavailable")
			} else if strings.TrimSpace(command.BindingID) != "" {
				invocation := h.roleBindingInvocation(r)
				bindings, revisionErr := repo.ListRoleBindings(r.Context(), workspaceID)
				if revisionErr == nil {
					for _, binding := range bindings {
						if binding.ID != command.BindingID {
							continue
						}
						invocation.ConcurrencyToken, revisionErr = access.RoleBindingRevision(binding)
						break
					}
				}
				if revisionErr != nil {
					err = revisionErr
				} else {
					_, err = h.RoleBindingCommands.UpdateRoleBinding(r.Context(), invocation, workspaceID, command.BindingID, input)
				}
			} else {
				_, err = h.RoleBindingCommands.CreateRoleBinding(r.Context(), h.roleBindingInvocation(r), input)
			}
			if err != nil {
				status = ui.WorkspaceAccessStatus{Error: err.Error()}
			}
		}
	}
	h.patchWorkspaceAccess(w, r, workspaceID, status)
}

func (h Handler) resolveAccessSubject(r *nethttp.Request, repo access.WorkspaceAccessService, command ui.WorkspaceAccessCommand) (access.SubjectType, string, error) {
	subjectType := access.SubjectType(strings.TrimSpace(command.SubjectType))
	if subjectType == "" {
		subjectType = access.SubjectPrincipal
	}
	subjectID := strings.TrimSpace(command.SubjectID)
	switch subjectType {
	case access.SubjectPrincipal:
		if subjectID != "" {
			return subjectType, subjectID, nil
		}
		email := strings.TrimSpace(command.Email)
		if email == "" {
			return "", "", fmt.Errorf("email is required")
		}
		principal, err := repo.UpsertPrincipal(r.Context(), access.PrincipalInput{
			ID:    access.PrincipalIDForEmail(email),
			Email: email,
		})
		if err != nil {
			return "", "", err
		}
		return subjectType, principal.ID, nil
	case access.SubjectGroup, access.SubjectServicePrincipal:
		if subjectID == "" {
			return "", "", fmt.Errorf("subject id is required")
		}
		return subjectType, subjectID, nil
	default:
		return "", "", fmt.Errorf("unsupported subject type %q", command.SubjectType)
	}
}

func (h Handler) AccessRemove(w nethttp.ResponseWriter, r *nethttp.Request) {
	signals := workspaceAccessSignalPayload{}
	if err := pagestream.ReadSignals(r, &signals); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusBadRequest)
		return
	}
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	command := signals.command()
	status := ui.WorkspaceAccessStatus{Message: "Access removed."}
	repo, err := h.accessRepository()
	if err != nil {
		status = ui.WorkspaceAccessStatus{Error: err.Error()}
	} else if repo == nil {
		status = ui.WorkspaceAccessStatus{Error: errWorkspaceAccessNotConfigured.Error()}
	} else if _, ok := assetAccessObject(r, workspaceID); ok {
		if h.GrantCommands == nil {
			status = ui.WorkspaceAccessStatus{Error: "grant commands are unavailable"}
		} else if _, err := h.GrantCommands.DeleteGrant(r.Context(), h.grantInvocation(r), workspaceID, command.BindingID); err != nil {
			status = ui.WorkspaceAccessStatus{Error: err.Error()}
		}
	} else if bindingID := strings.TrimSpace(command.BindingID); bindingID != "" {
		if h.RoleBindingCommands == nil {
			status = ui.WorkspaceAccessStatus{Error: "role binding commands are unavailable"}
		} else if _, err := h.RoleBindingCommands.DeleteRoleBinding(r.Context(), h.roleBindingInvocation(r), workspaceID, bindingID); err != nil {
			status = ui.WorkspaceAccessStatus{Error: err.Error()}
		}
	} else if h.RoleBindingCommands == nil {
		status = ui.WorkspaceAccessStatus{Error: "role binding commands are unavailable"}
	} else if bindings, err := repo.ListRoleBindings(r.Context(), workspaceID); err != nil {
		status = ui.WorkspaceAccessStatus{Error: err.Error()}
	} else {
		for _, binding := range bindings {
			if binding.PrincipalID != command.PrincipalID && binding.SubjectID != command.PrincipalID {
				continue
			}
			if _, err := h.RoleBindingCommands.DeleteRoleBinding(r.Context(), h.roleBindingInvocation(r), workspaceID, binding.ID); err != nil {
				status = ui.WorkspaceAccessStatus{Error: err.Error()}
				break
			}
		}
	}
	h.patchWorkspaceAccess(w, r, workspaceID, status)
}

func (h Handler) roleBindingInvocation(r *nethttp.Request) access.RoleBindingInvocation {
	principal, _ := h.ReadModel.currentPrincipal(r)
	requestID := firstNonEmpty(r.Header.Get("X-Request-Id"), r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = httptransport.NewRequestID()
		r.Header.Set("X-Request-ID", requestID)
	}
	return access.RoleBindingInvocation{
		PrincipalID: principal.ID, Surface: access.OperationSurfaceUI,
		RequestID: requestID, IdempotencyKey: requestID,
		CorrelationID:   firstNonEmpty(r.Header.Get("X-Correlation-Id"), r.Header.Get("X-Correlation-ID"), requestID),
		OperationClaims: uicommand.OperationClaims(r),
	}
}

func (h Handler) grantInvocation(r *nethttp.Request) access.GrantInvocation {
	return h.roleBindingInvocation(r)
}

func (h Handler) patchWorkspaceAccess(w nethttp.ResponseWriter, r *nethttp.Request, workspaceID string, status ui.WorkspaceAccessStatus) {
	workspaceView := h.workspaceResponse(r, workspaceID)
	accessResponse := h.workspaceAccess(r, workspaceView, true, status)
	if object, ok := assetAccessObject(r, workspaceID); ok {
		accessResponse = h.objectAccess(r, workspaceView, object, status)
	}
	_ = pagestream.PatchResponse(w, r, pagestream.SignalPatch{"workspaceAccess": ui.WorkspaceAccessSignals(accessResponse)})
}

func (h Handler) objectAccess(r *nethttp.Request, workspaceView workspace.WorkspaceView, object access.ObjectRef, status ui.WorkspaceAccessStatus) ui.WorkspaceAccessResponse {
	response := ui.WorkspaceAccessResponse{
		Workspace:   workspaceView,
		ObjectType:  string(object.Type),
		ObjectID:    object.ObjectID,
		ObjectTitle: object.ObjectID,
		Mode:        "object",
		Roles:       objectPrivilegeRoleViews(),
		CanManage:   true,
		Status:      status,
	}
	repo, err := h.accessRepository()
	if err != nil {
		response.Status.Error = err.Error()
		return response
	}
	if repo == nil {
		response.Status.Error = errWorkspaceAccessNotConfigured.Error()
		return response
	}
	grants, err := repo.ListGrants(r.Context(), object)
	if err != nil {
		response.Status.Error = err.Error()
		return response
	}
	for _, grant := range grants {
		view := workspace.RoleBindingView{
			ID:          grant.ID,
			WorkspaceID: grant.WorkspaceID,
			SubjectType: string(grant.SubjectType),
			SubjectID:   grant.SubjectID,
			Role:        string(grant.Privilege),
			CreatedAt:   grant.CreatedAt,
		}
		if grant.SubjectType == access.SubjectPrincipal {
			view.PrincipalID = grant.SubjectID
			if principal, err := repo.PrincipalByID(r.Context(), grant.SubjectID); err == nil {
				view.Email = principal.Email
				view.DisplayName = principal.DisplayName
			}
		} else if grant.SubjectType == access.SubjectGroup {
			view.GroupID = grant.SubjectID
			view.GroupName = h.groupDisplayName(r, repo, grant.SubjectID)
		} else if grant.SubjectType == access.SubjectServicePrincipal {
			view.PrincipalID = grant.SubjectID
			if principal, err := repo.PrincipalByID(r.Context(), grant.SubjectID); err == nil {
				view.DisplayName = firstNonEmpty(principal.DisplayName, principal.ID)
			}
		}
		response.Bindings = append(response.Bindings, view)
	}
	return response
}

func (h Handler) groupDisplayName(r *nethttp.Request, repo access.WorkspaceAccessService, groupID string) string {
	groups, err := repo.ListAllGroups(r.Context())
	if err != nil {
		return groupID
	}
	for _, group := range groups {
		if group.ID == groupID {
			return firstNonEmpty(group.Name, group.ID)
		}
	}
	return groupID
}

func objectPrivilegeRoleViews() []workspace.RoleView {
	privileges := []access.Privilege{
		access.PrivilegeViewItem,
		access.PrivilegeEditItem,
		access.PrivilegeManageItem,
		access.PrivilegeQueryData,
		access.PrivilegePreviewData,
		access.PrivilegeRefreshData,
		access.PrivilegeUseAgent,
		access.PrivilegeViewAgent,
		access.PrivilegeManageGrants,
	}
	roles := make([]workspace.RoleView, 0, len(privileges))
	for _, privilege := range privileges {
		roles = append(roles, workspace.RoleView{Name: string(privilege), Privileges: []string{string(privilege)}})
	}
	return roles
}

func assetAccessObject(r *nethttp.Request, workspaceID string) (access.ObjectRef, bool) {
	raw := strings.TrimSpace(firstNonEmpty(chi.URLParam(r, "assetId"), chi.URLParam(r, "asset")))
	return assetObjectForID(workspaceID, raw)
}

func assetObjectForID(workspaceID, raw string) (access.ObjectRef, bool) {
	if raw == "" {
		return access.ObjectRef{}, false
	}
	typ, objectID, ok := strings.Cut(raw, ":")
	if !ok || strings.TrimSpace(objectID) == "" {
		return access.ObjectRef{}, false
	}
	switch workspace.AssetType(typ) {
	case workspace.AssetTypeDashboard:
		return access.ItemObjectWithParent(access.SecurableDashboard, workspaceID, objectID, access.WorkspaceObject(workspaceID)), true
	case workspace.AssetTypeSemanticModel:
		return access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, objectID, access.WorkspaceObject(workspaceID)), true
	case workspace.AssetTypeSource:
		return access.ItemObjectWithParent(access.SecurableSource, workspaceID, objectID, access.WorkspaceObject(workspaceID)), true
	case workspace.AssetTypeModelTable:
		return access.ItemObjectWithParent(access.SecurableModelTable, workspaceID, objectID, access.WorkspaceObject(workspaceID)), true
	case workspace.AssetTypeSemanticTable:
		modelID, tableID, ok := strings.Cut(objectID, ".")
		if !ok {
			return access.ItemObject(access.SecurableDataset, workspaceID, objectID), true
		}
		model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, access.WorkspaceObject(workspaceID))
		return access.ItemObjectWithParent(access.SecurableDataset, workspaceID, modelID+"/"+tableID, model), true
	case workspace.AssetTypeField:
		parts := strings.Split(objectID, ".")
		if len(parts) < 3 {
			return access.ItemObject(access.SecurableColumn, workspaceID, objectID), true
		}
		model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, parts[0], access.WorkspaceObject(workspaceID))
		table := access.ItemObjectWithParent(access.SecurableDataset, workspaceID, parts[0]+"/"+parts[1], model)
		return access.ItemObjectWithParent(access.SecurableColumn, workspaceID, parts[0]+"/"+parts[1]+"/"+strings.Join(parts[2:], "."), table), true
	case workspace.AssetTypeMeasure:
		modelID, memberID, ok := strings.Cut(objectID, ".")
		if !ok {
			return access.ItemObject(access.SecurableSemanticField, workspaceID, objectID), true
		}
		model := access.ItemObjectWithParent(access.SecurableSemanticModel, workspaceID, modelID, access.WorkspaceObject(workspaceID))
		return access.ItemObjectWithParent(access.SecurableSemanticField, workspaceID, modelID+"/"+memberID, model), true
	default:
		return access.ObjectRef{}, false
	}
}

func AssetObjectRefs(r *nethttp.Request, workspaceID string) []access.ObjectRef {
	objects := []access.ObjectRef{}
	if object, ok := assetAccessObject(r, workspaceID); ok {
		objects = append(objects, object)
	}
	if strings.TrimSpace(workspaceID) != "" {
		objects = append(objects, access.WorkspaceObject(workspaceID))
	}
	return objects
}

func (h Handler) AssetUpdatesStream(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(firstNonEmpty(chi.URLParam(r, "workspace"), r.URL.Query().Get("workspace")))
	assetID := firstNonEmpty(chi.URLParam(r, "asset"), r.URL.Query().Get("asset"))
	assetWorkspaceID := h.workspaceID(r.URL.Query().Get("assetWorkspace"))
	section := workspacedatastar.WorkspaceAssetUpdateSection(r)
	route := r.URL.Query().Get("route")
	var (
		assets []workspace.AssetView
		edges  []workspace.AssetEdgeView
		err    error
	)
	if route == string(uisignals.RouteConnectionAsset) {
		assets, edges, err = h.platformAssetsAndEdges(r)
	} else {
		assets, edges, err = h.assetsAndEdges(r, workspaceID)
	}
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := workspace.AssetByID(assets, assetID)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	if route == string(uisignals.RouteConnectionAsset) && strings.TrimSpace(assetWorkspaceID) != "" {
		workspaceID = assetWorkspaceID
	} else if strings.TrimSpace(workspaceID) == "" {
		workspaceID = selected.WorkspaceID
	}

	streamID := workspacedatastar.WorkspaceAssetStreamID(workspaceID, assetID, section)
	broker := h.broker()
	var trace *pagestream.TraceStore
	if broker != nil {
		trace = broker.TraceStore()
	}
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, streamID, "workspace.asset.bootstrap"))
	refresh, err := h.assetRefreshState(r.Context(), workspaceID, h.environment(r), selected)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	versions, err := h.assetVersionsState(r.Context(), workspaceID, h.environment(r), selected, section)
	if err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	var patch pagestream.SignalPatch
	if route == string(uisignals.RouteConnectionAsset) {
		if selected.Type == string(workspace.AssetTypeSource) {
			connectionID := assetnav.SourceConnectionID(selected.ID, edges)
			if connection, ok := workspace.AssetByID(assets, connectionID); ok {
				patch = ui.ConnectionSourceAssetBootstrapSignalsForEnvironment(h.catalogForWorkspacesPage(r, nil), platformAssetWorkspaceView(), connection, selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions)
			}
		}
		if patch == nil {
			patch = ui.ConnectionAssetBootstrapSignalsForEnvironment(h.catalogForWorkspacesPage(r, nil), platformAssetWorkspaceView(), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), versions)
		}
	} else {
		patch = ui.WorkspaceAssetBootstrapSignalsForEnvironment(h.catalogForWorkspace(workspaceID), h.workspaceResponse(r, workspaceID), selected, assets, edges, section, h.environment(r), h.currentRoleLabel(r), refresh, versions, h.chromeOptions(r)...)
	}
	if err := updates.Patch(patch); err != nil {
		return
	}
	if workspaceAssetRefreshable(selected) {
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
	updates := pagestream.NewSignalStream(w, r, pagestream.WithStreamTrace(trace, "workspace:"+clientID, "workspace.bootstrap"))
	if err := updates.Patch(patch); err != nil {
		return
	}
	updates.Wait(r.Context())
}

func (h Handler) RefreshAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.refreshAsset(w, r)
}

func (h Handler) refreshAsset(w nethttp.ResponseWriter, r *nethttp.Request) {
	workspaceID := h.workspaceID(chi.URLParam(r, "workspace"))
	assetID := chi.URLParam(r, "asset")
	assets, edges, err := h.assetsAndEdges(r, workspaceID)
	if err != nil {
		nethttp.Error(w, err.Error(), statusForNotFound(err))
		return
	}
	selected, ok := workspace.AssetByID(assets, assetID)
	if !ok || !workspaceAssetRefreshable(selected) {
		nethttp.NotFound(w, r)
		return
	}
	if h.RefreshRunner == nil {
		nethttp.Error(w, "workspace refresh runner is required", nethttp.StatusServiceUnavailable)
		return
	}
	if err := h.RefreshRunner.RefreshAsset(r.Context(), AssetRefreshInput{
		Request:     r,
		WorkspaceID: workspaceID,
		Asset:       selected,
		Assets:      assets,
		Edges:       edges,
	}); err != nil {
		nethttp.Error(w, err.Error(), nethttp.StatusInternalServerError)
		return
	}
	w.WriteHeader(nethttp.StatusNoContent)
}

func (h Handler) workspaceID(value string) string {
	if h.WorkspaceID != nil {
		return h.WorkspaceID(value)
	}
	return value
}

func (h Handler) environment(r *nethttp.Request) string {
	if h.Environment == nil {
		return ""
	}
	return h.Environment(r)
}

func (h Handler) workspaceRepository() (workspace.ReadModel, error) {
	return h.ReadModel.workspaceRepository()
}

func (h Handler) accessRepository() (access.WorkspaceAccessService, error) {
	return h.ReadModel.accessRepository()
}

func (h Handler) assetsAndEdges(r *nethttp.Request, workspaceID string) ([]workspace.AssetView, []workspace.AssetEdgeView, error) {
	return h.ReadModel.WorkspaceAssetsAndEdges(r, workspaceID)
}

func (h Handler) platformAssetsAndEdges(r *nethttp.Request) ([]workspace.AssetView, []workspace.AssetEdgeView, error) {
	return h.ReadModel.PlatformAssetsAndEdges(r)
}

func (h Handler) workspaceList(r *nethttp.Request) ([]workspace.WorkspaceView, error) {
	return h.ReadModel.WorkspaceList(r)
}

func (h Handler) workspaceAssetsAndEdgesForData(ctx context.Context, workspaceID, environment string) ([]workspace.AssetView, []workspace.AssetEdgeView, error) {
	assets, edges, err := h.ReadModel.WorkspaceAssetsAndEdgesForData(ctx, workspaceID, environment)
	if err != nil {
		return nil, nil, err
	}
	if len(assets) == 0 && len(edges) == 0 {
		return nil, nil, fmt.Errorf("workspace %q assets were not found", workspaceID)
	}
	return assets, edges, nil
}

func (h Handler) metricsForWorkspace(workspaceID string) (Metrics, bool) {
	return h.ReadModel.metricsForWorkspace(workspaceID)
}

func (h Handler) roleBindingsAndRoles(r *nethttp.Request, workspaceID string) ([]workspace.RoleBindingView, []workspace.RoleView, error) {
	return h.ReadModel.RoleBindingsAndRoles(r, workspaceID)
}

func (h Handler) catalogForWorkspacesPage(r *nethttp.Request, workspaces []workspace.WorkspaceView) catalog.Catalog {
	return h.ReadModel.CatalogForWorkspacesPage(r, workspaces)
}

func (h Handler) catalogForWorkspace(workspaceID string) catalog.Catalog {
	return h.ReadModel.catalogForWorkspace(workspaceID)
}

func (h Handler) workspaceResponse(r *nethttp.Request, workspaceID string) workspace.WorkspaceView {
	return h.ReadModel.WorkspaceResponse(r, workspaceID)
}

func (h Handler) canManageAccess(r *nethttp.Request, workspaceID string) bool {
	return h.ReadModel.CanManageAccess(r, workspaceID)
}

func (h Handler) workspaceAccess(r *nethttp.Request, view workspace.WorkspaceView, canManage bool, status ui.WorkspaceAccessStatus) ui.WorkspaceAccessResponse {
	return h.ReadModel.WorkspaceAccess(r, view, canManage, status)
}

func (h Handler) assetRefreshState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView) (ui.AssetRefreshState, error) {
	if h.RefreshState == nil || !workspaceAssetRefreshable(asset) {
		return ui.AssetRefreshState{}, nil
	}
	return h.RefreshState.AssetRefreshState(ctx, workspaceID, environment, asset)
}

func (h Handler) assetVersionsState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView, section string) (ui.AssetVersionsState, error) {
	if h.RefreshState == nil {
		return ui.AssetVersionsState{CurrentContentHash: asset.ContentHash}, nil
	}
	return h.RefreshState.AssetVersionsState(ctx, workspaceID, environment, asset, section)
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

var errWorkspaceAccessNotConfigured = errors.New("workspace access store is not configured")

func connectionSourcePair(assets []workspace.AssetView, edges []workspace.AssetEdgeView, connectionID, sourceID string) (workspace.AssetView, workspace.AssetView, bool) {
	connection, ok := workspace.AssetByID(assets, connectionID)
	if !ok || connection.Type != string(workspace.AssetTypeConnection) {
		return workspace.AssetView{}, workspace.AssetView{}, false
	}
	source, ok := workspace.AssetByID(assets, sourceID)
	if !ok || source.Type != string(workspace.AssetTypeSource) || assetnav.SourceConnectionID(source.ID, edges) != connection.ID {
		return workspace.AssetView{}, workspace.AssetView{}, false
	}
	return connection, source, true
}

func platformAssetWorkspaceView() workspace.WorkspaceView {
	return workspace.WorkspaceView{ID: "platform", Title: "Global assets", Description: "Global connection and source assets."}
}

func workspaceAssetRefreshable(asset workspace.AssetView) bool {
	return asset.Type == string(workspace.AssetTypeRefreshPipeline)
}

func (h Handler) workspaceAssetRefreshPatch(r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView, section string) pagestream.SignalPatch {
	refresh, err := h.assetRefreshState(r.Context(), workspaceID, h.environment(r), asset)
	if err != nil {
		refresh = ui.AssetRefreshState{Latest: ui.AssetRefreshRun{Status: "failed"}}
	}
	return pagestream.SignalPatch(workspacedatastar.WorkspaceAssetRefreshSignals(h.workspaceResponse(r, workspaceID), asset, assets, edges, refresh, section))
}

func (h Handler) PublishWorkspaceAssetRefreshPatch(r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView) {
	broker := h.broker()
	if broker == nil {
		return
	}
	for _, section := range workspacedatastar.WorkspaceAssetRefreshSections() {
		broker.Publish(workspacedatastar.WorkspaceAssetStreamID(workspaceID, asset.ID, section), h.workspaceAssetRefreshPatch(r, workspaceID, asset, assets, edges, section))
	}
}

func (h Handler) PublishWorkspaceAssetRefreshPatchForTarget(r *nethttp.Request, workspaceID, targetID string, assets []workspace.AssetView, edges []workspace.AssetEdgeView) {
	for _, asset := range assets {
		if asset.Key == targetID && workspaceAssetRefreshable(asset) {
			h.PublishWorkspaceAssetRefreshPatch(r, workspaceID, asset, assets, edges)
		}
	}
}

func apiWorkspaceDTOs(rows []workspace.WorkspaceView) []api.WorkspaceResponse {
	out := make([]api.WorkspaceResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.WorkspaceResponse{
			ID:                   row.ID,
			Title:                row.Title,
			Description:          row.Description,
			ActiveServingStateID: row.ActiveServingStateID,
			CreatedAt:            row.CreatedAt,
			UpdatedAt:            row.UpdatedAt,
		})
	}
	return out
}

func apiAssetDTOs(rows []workspace.AssetView) []api.AssetResponse {
	out := make([]api.AssetResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetResponse{
			ID:             row.ID,
			SnapshotID:     row.SnapshotID,
			WorkspaceID:    row.WorkspaceID,
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

func apiAssetSummaryDTOs(rows []workspace.AssetView) []api.AssetSummaryResponse {
	out := make([]api.AssetSummaryResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetSummaryResponse{
			ID:             row.ID,
			SnapshotID:     row.SnapshotID,
			WorkspaceID:    row.WorkspaceID,
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

func apiWorkspaceAssetGraphDTO(graph workspace.AssetGraph) (api.WorkspaceAssetGraphResponse, error) {
	assets := make([]api.AssetGraphAssetResponse, 0, len(graph.Assets))
	for _, row := range graph.Assets {
		payload := map[string]any{}
		if row.PayloadJSON != "" {
			if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
				return api.WorkspaceAssetGraphResponse{}, err
			}
		}
		assets = append(assets, api.AssetGraphAssetResponse{
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
			Payload:        payload,
			ContentHash:    row.ContentHash,
		})
	}
	return api.WorkspaceAssetGraphResponse{
		Assets: assets,
		Edges:  apiWorkspaceAssetGraphEdgeDTOs(graph.Edges),
	}, nil
}

func apiWorkspaceAssetGraphEdgeDTOs(rows []workspace.AssetEdge) []api.AssetEdgeResponse {
	out := make([]api.AssetEdgeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetEdgeResponse{
			ID:             string(row.ID),
			WorkspaceID:    string(row.WorkspaceID),
			ServingStateID: string(row.ServingStateID),
			FromAssetID:    string(row.FromAssetID),
			ToAssetID:      string(row.ToAssetID),
			Type:           string(row.Type),
		})
	}
	return out
}

func apiAssetEdgeDTOs(rows []workspace.AssetEdgeView) []api.AssetEdgeResponse {
	out := make([]api.AssetEdgeResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AssetEdgeResponse{
			ID:             row.ID,
			WorkspaceID:    row.WorkspaceID,
			ServingStateID: row.ServingStateID,
			FromAssetID:    row.FromAssetID,
			ToAssetID:      row.ToAssetID,
			Type:           row.Type,
		})
	}
	return out
}

func assetLineageEndpointIDs(edges []workspace.AssetEdgeView, assetID string, upstream bool) []string {
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

func apiRoleDTOs(rows []workspace.RoleView) []accessapi.RoleResponse {
	out := make([]accessapi.RoleResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, accessapi.RoleResponse{Name: row.Name, Privileges: row.Privileges})
	}
	return out
}

func apiRoleBindingDTO(row access.RoleBinding) map[string]any {
	return map[string]any{"id": row.ID, "workspaceId": row.WorkspaceID, "subjectType": string(row.SubjectType), "subjectId": row.SubjectID, "email": row.Email, "displayName": firstNonEmpty(row.DisplayName, row.GroupName), "role": row.Role, "createdAt": row.CreatedAt}
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
			if workspacePageItemKey(item) == lastKey {
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
		nextCursor = encodeKeysetCursor(workspacePageItemKey(items[end-1]))
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

func workspacePageItemKey(value any) string {
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
