package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Protect         func(access.Capability, http.HandlerFunc) http.HandlerFunc
	ProtectGlobal   func(access.Capability, http.HandlerFunc) http.HandlerFunc
	ProtectPlatform func(access.Capability, http.HandlerFunc) http.HandlerFunc
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil || m.handler == nil {
		return
	}
	h := m.handler
	r.Get("/chats", guard.ProtectGlobal(access.CapabilityResourceUse, h.Chat))
	r.Get("/chats/new", guard.ProtectGlobal(access.CapabilityResourceUse, h.ChatNew))
	r.Get("/chats/references/search", guard.ProtectGlobal(access.CapabilityResourceRead, h.ChatReferenceSearch))
	r.Get("/chats/restore", guard.ProtectGlobal(access.CapabilityResourceUse, h.ChatRestore))
	r.Get("/chats/{conversation}", guard.ProtectGlobal(access.CapabilityResourceUse, h.ChatConversation))
	r.Post("/chats/turns", guard.ProtectGlobal(access.CapabilityResourceUse, h.ChatTurn))
	r.Patch("/admin/agent/config", guard.ProtectPlatform(access.CapabilityProjectAdmin, h.UpdateAdminConfig))
}

func (m *Module) MountMCP(r chi.Router) {
	if m != nil {
		r.Handle("/mcp", m.MCPHandler())
	}
}
