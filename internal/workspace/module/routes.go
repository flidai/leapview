package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
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
	protectAnyWorkspace := guard.ProtectAnyWorkspace
	if protectAnyWorkspace == nil {
		protectAnyWorkspace = guard.Protect
	}
	r.Get("/pipelines", protectAnyWorkspace(access.PrivilegeViewItem, h.Pipelines))
	r.Post("/pipelines/command", protectAnyWorkspace(access.PrivilegeViewItem, h.PipelineCommand))
	r.Get("/data", protectAnyWorkspace(access.PrivilegeViewItem, h.DataExplorer))
	r.Post("/data/command", protectAnyWorkspace(access.PrivilegeViewItem, h.DataExplorerCommand))
	r.Post("/catalog/search", protectAnyWorkspace(access.PrivilegeViewItem, m.CatalogSearch))
	// The project graph is the sole browser resource namespace. Workspace
	// collection/object routes are intentionally gone; Develop entry points
	// resolve the singleton runtime and address stable resource IDs directly.
	r.Get("/models", protectAnyWorkspace(access.PrivilegeViewItem, h.WorkspaceCatalog))
	r.Get("/semantic-models", protectAnyWorkspace(access.PrivilegeViewItem, h.WorkspaceCatalog))
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
