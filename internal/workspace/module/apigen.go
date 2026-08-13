package module

import (
	"log/slog"
	"net/http"

	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
)

type workspaceAPIGenHandler struct{ module *Module }

func (h workspaceAPIGenHandler) Search(w http.ResponseWriter, r *http.Request, params workspacehttp.APIGenSearchParams) {
	h.module.SearchAPI(w, r, params)
}

func (h workspaceAPIGenHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().Workspaces(w, r)
}

func (h workspaceAPIGenHandler) GetWorkspace(w http.ResponseWriter, r *http.Request, workspaceID string) {
	h.module.GetWorkspace(w, r, workspaceID)
}

func (h workspaceAPIGenHandler) GetWorkspaceAdministration(w http.ResponseWriter, r *http.Request, workspaceID string) {
	h.module.GetWorkspaceAdministration(w, r, workspaceID)
}

func (h workspaceAPIGenHandler) GetWorkspaceActiveAssetGraph(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().ActiveDeploymentGraph(w, r)
}

func (h workspaceAPIGenHandler) ListWorkspaceAssetEdges(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().AssetEdges(w, r)
}

func (h workspaceAPIGenHandler) ListWorkspaceAssets(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().Assets(w, r)
}

func (h workspaceAPIGenHandler) GetWorkspaceAsset(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().Asset(w, r)
}

func (h workspaceAPIGenHandler) GetWorkspaceAssetLineage(w http.ResponseWriter, r *http.Request) {
	h.module.HTTP().AssetLineage(w, r)
}

func (h workspaceAPIGenHandler) UpdateDashboardAppearance(w http.ResponseWriter, r *http.Request, workspaceID, dashboardID string) {
	h.module.UpdateDashboardAppearance(w, r, workspaceID, dashboardID, false)
}

func (m *Module) DispatchAPIGenOperation(operationID string, logger *slog.Logger, w http.ResponseWriter, r *http.Request) bool {
	return workspacehttp.DispatchAPIGenOperation(operationID, workspaceAPIGenHandler{module: m}, logger, w, r)
}
