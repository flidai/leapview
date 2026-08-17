package module

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Authenticate         func(http.Handler) http.Handler
	RequirePlatformAdmin func(http.Handler) http.Handler
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil || m.handler == nil {
		return
	}
	h := m.handler
	authenticated := func(next http.HandlerFunc) http.HandlerFunc {
		if guard.Authenticate == nil {
			return unavailable
		}
		return guard.Authenticate(http.HandlerFunc(next)).ServeHTTP
	}
	platformAdmin := func(next http.HandlerFunc) http.HandlerFunc {
		if guard.RequirePlatformAdmin == nil {
			return unavailable
		}
		return guard.RequirePlatformAdmin(http.HandlerFunc(next)).ServeHTTP
	}
	r.Get("/chats", authenticated(h.Chat))
	r.Get("/chats/new", authenticated(h.ChatNew))
	r.Get("/chats/references/search", authenticated(h.ChatReferenceSearch))
	r.Get("/chats/restore", authenticated(h.ChatRestore))
	r.Get("/chats/{conversation}", authenticated(h.ChatConversation))
	r.Post("/chats/turns", authenticated(h.ChatTurn))
	r.Patch("/admin/agent/config", platformAdmin(h.UpdateAdminConfig))
}

func unavailable(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}

func (m *Module) MountMCP(r chi.Router) {
	if m != nil {
		r.Handle("/mcp", m.MCPHandler())
	}
}
