package http

import (
	nethttp "net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

// RouteGuard applies the authenticated resource capability policy to a
// browser handler. Project selection is deliberately absent: Handler carries
// the single validated active project from composition.
type RouteGuard func(access.Privilege, nethttp.HandlerFunc) nethttp.HandlerFunc

// MountRoutes registers the canonical browser resource areas. The route
// prefixes are part of the public Develop IA; keep them stable when adding a
// new asset section.
func (h Handler) MountRoutes(r chi.Router, guard RouteGuard) {
	protect := func(privilege access.Privilege, next nethttp.HandlerFunc) nethttp.HandlerFunc {
		if guard == nil {
			return next
		}
		return guard(privilege, next)
	}
	view := func(next nethttp.HandlerFunc) nethttp.HandlerFunc { return protect(access.PrivilegeViewItem, next) }

	// Explore is the governed power-user data explorer. Develop's /data area
	// remains the source/managed-input resource area.
	r.Get("/explore", view(h.DataExplorer))
	r.Post("/explore/command", view(h.DataExplorerCommand))

	r.Get("/data", view(h.ProjectAssets))
	r.Post("/data/search", view(h.ProjectAssetSearch))
	mountAssetArea(r, "/data", view, h)
	mountAssetArea(r, "/models", view, h)
	mountAssetArea(r, "/semantic-models", view, h)
	r.Get("/pipelines", view(h.Pipelines))
	r.Post("/pipelines/command", view(h.PipelineCommand))
	mountAssetArea(r, "/pipelines", view, h)

	r.Get("/connections", view(h.Connections))
	r.Post("/connections/search", view(h.ConnectionsSearch))
	r.Post("/connections/administration/configuration", view(h.ConnectionConfiguration))
	r.Post("/connections/administration/lifecycle", view(h.ConnectionLifecycle))
	r.Get("/connections/{asset}", view(h.ConnectionAsset))
	r.Get("/connections/{asset}/{section}", view(h.ConnectionAssetSection))
}

func mountAssetArea(r chi.Router, prefix string, view func(nethttp.HandlerFunc) nethttp.HandlerFunc, h Handler) {
	r.Get(prefix+"/{asset}", view(h.ProjectAsset))
	r.Get(prefix+"/{asset}/{section}", view(h.ProjectAssetSection))
}
