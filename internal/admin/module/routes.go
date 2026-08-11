package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Protect             func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectGlobal       func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectAnyWorkspace func(access.Privilege, http.HandlerFunc) http.HandlerFunc
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil {
		return
	}
	h := m.handler
	r.Get("/admin", guard.ProtectGlobal(access.PrivilegeManageGrants, h.AdminRoot))
	r.Get("/admin/profile", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Profile))
	r.Get("/admin/principals", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Principals))
	r.Post("/admin/principals/search", guard.ProtectGlobal(access.PrivilegeManageGrants, h.PrincipalsSearch))
	r.Get("/admin/principals/{principal}", guard.ProtectGlobal(access.PrivilegeManageGrants, h.PrincipalDetail))
	r.Get("/admin/groups", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Groups))
	r.Post("/admin/groups/search", guard.ProtectGlobal(access.PrivilegeManageGrants, h.GroupsSearch))
	r.Get("/admin/groups/{group}", guard.ProtectGlobal(access.PrivilegeManageGrants, h.GroupDetail))
	r.Get("/admin/agent", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Agent))
	r.Get("/admin/storage", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Storage))
	r.Post("/admin/storage/select-table", guard.ProtectGlobal(access.PrivilegeManageGrants, h.StorageTableSelect))
	r.Get("/admin/queries", guard.ProtectGlobal(access.PrivilegeViewAudit, h.Queries))
	r.Post("/admin/queries/command", guard.ProtectGlobal(access.PrivilegeViewAudit, h.QueryCommand))
	r.Get("/admin/publications", guard.ProtectAnyWorkspace(access.PrivilegeManagePublications, h.Publications))
	r.Post("/admin/publications/command", guard.ProtectAnyWorkspace(access.PrivilegeManagePublications, h.PublicationCommand))
}
