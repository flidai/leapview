package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Protect             func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectAnyWorkspace func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectWithObjects  func(access.Privilege, func(*http.Request, string) []access.ObjectRef, http.HandlerFunc) http.HandlerFunc
	AssetObjectRefs     func(*http.Request, string) []access.ObjectRef
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil {
		return
	}
	h := m.handler
	assetObjectRefs := guard.AssetObjectRefs
	if assetObjectRefs == nil {
		assetObjectRefs = workspacehttp.AssetObjectRefs
	}
	protectAnyWorkspace := guard.ProtectAnyWorkspace
	if protectAnyWorkspace == nil {
		protectAnyWorkspace = guard.Protect
	}
	r.Get("/pipelines", protectAnyWorkspace(access.PrivilegeViewItem, h.Pipelines))
	r.Post("/pipelines/command", protectAnyWorkspace(access.PrivilegeViewItem, h.PipelineCommand))
	r.Get("/data", protectAnyWorkspace(access.PrivilegeViewItem, h.DataExplorer))
	r.Post("/data/command", protectAnyWorkspace(access.PrivilegeViewItem, h.DataExplorerCommand))
	r.Post("/catalog/search", protectAnyWorkspace(access.PrivilegeViewItem, m.CatalogSearch))
	r.Get("/workspaces", protectAnyWorkspace(access.PrivilegeViewItem, h.WorkspaceCatalog))
	r.Post("/workspaces/search", protectAnyWorkspace(access.PrivilegeViewItem, h.WorkspaceListSearch))
	r.Get("/workspaces/{workspace}", guard.Protect(access.PrivilegeViewItem, h.WorkspaceAssets))
	r.Post("/workspaces/{workspace}/search", guard.Protect(access.PrivilegeViewItem, h.WorkspaceAssetSearch))
	r.Post("/workspaces/{workspace}/catalog/appearance", guard.Protect(access.PrivilegeManageWorkspace, func(w http.ResponseWriter, request *http.Request) {
		m.UpdateDashboardAppearanceFromUI(w, request)
	}))
	r.Get("/workspaces/{workspace}/assets/{asset}", guard.ProtectWithObjects(access.PrivilegeViewItem, assetObjectRefs, h.WorkspaceAsset))
	r.Get("/workspaces/{workspace}/assets/{asset}/{section}", guard.ProtectWithObjects(access.PrivilegeViewItem, assetObjectRefs, h.WorkspaceAssetSection))
	r.Post("/workspaces/{workspace}/assets/{asset}/refresh", guard.ProtectWithObjects(access.PrivilegeRefreshData, assetObjectRefs, h.RefreshAsset))
	r.Get("/workspaces/{workspace}/data", guard.Protect(access.PrivilegeViewItem, h.WorkspaceDataExplorerRedirect))
	if h.RoleBindingCommands != nil {
		r.Post("/workspaces/{workspace}/access/upsert", guard.Protect(m.roleBindingUpsert, h.AccessUpsert))
		r.Post("/workspaces/{workspace}/access/remove", guard.Protect(m.roleBindingDelete, h.AccessRemove))
	}
	r.Get("/workspaces/{workspace}/access/search", guard.Protect(access.PrivilegeManageGrants, h.AccessSearch))
	if h.GrantCommands != nil {
		r.Post("/workspaces/{workspace}/assets/{asset}/access/upsert", guard.ProtectWithObjects(m.grantUpsert, workspacehttp.AssetObjectRefs, h.AccessUpsert))
		r.Post("/workspaces/{workspace}/assets/{asset}/access/remove", guard.ProtectWithObjects(m.grantDelete, workspacehttp.AssetObjectRefs, h.AccessRemove))
	}
	r.Get("/connections", protectAnyWorkspace(access.PrivilegeViewItem, h.Connections))
	r.Post("/connections/search", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionsSearch))
	if h.ConnectionAdministration != nil {
		r.Post("/connections/administration/configuration", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionConfiguration))
		r.Post("/connections/administration/lifecycle", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionLifecycle))
	}
	r.Get("/connections/{connection}/sources/{source}", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionSource))
	r.Get("/connections/{connection}/sources/{source}/{section}", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionSourceSection))
	r.Get("/connections/{asset}", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionAsset))
	r.Get("/connections/{asset}/{section}", protectAnyWorkspace(access.PrivilegeViewItem, h.ConnectionAssetSection))
}
