package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/access"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	dashboardui "github.com/flidai/leapview/internal/dashboard/ui"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	ProtectWithResources func(access.Capability, func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, http.HandlerFunc) http.HandlerFunc
	// ProtectWithAuthoring authorizes repository-backed draft routes. Unlike
	// graph resources, a newly created dashboard is not present in the active
	// serving generation until publication/deployment.
	ProtectWithAuthoring func(access.Capability, http.HandlerFunc) http.HandlerFunc
}

func (m *Module) MountPublicDocuments(r chi.Router) {
	if m == nil {
		return
	}
	r.Get("/public/dashboards/{publicId}", m.PublicDashboardDocument(dashboardui.PresentationPublic))
	r.Get("/public/dashboards/{publicId}/pages/{page}", m.PublicDashboardDocument(dashboardui.PresentationPublic))
	r.Get("/embed/dashboards/{publicId}", m.PublicDashboardDocument(dashboardui.PresentationEmbed))
	r.Get("/embed/dashboards/{publicId}/pages/{page}", m.PublicDashboardDocument(dashboardui.PresentationEmbed))
}

func (m *Module) MountPublicCommands(r chi.Router) {
	if m == nil {
		return
	}
	r.Post("/public/dashboards/{publicId}/commands/filter", m.PublicDashboardCommand("filter"))
	r.Post("/public/dashboards/{publicId}/commands/filter-options", m.PublicDashboardCommand("filter_options"))
	r.Post("/public/dashboards/{publicId}/commands/navigate", m.PublicDashboardCommand("navigate"))
	r.Post("/public/dashboards/{publicId}/commands/select", m.PublicDashboardCommand("select"))
	r.Post("/public/dashboards/{publicId}/commands/spatial-select", m.PublicDashboardCommand("spatial_select"))
	r.Post("/public/dashboards/{publicId}/commands/clear-selection", m.PublicDashboardCommand("clear_selection"))
	r.Post("/public/dashboards/{publicId}/commands/visual-window", m.PublicDashboardCommand("visual_window"))
}

func (m *Module) MountPublicStream(r chi.Router) {
	if m != nil {
		r.Get("/public/dashboards/{publicId}/updates", m.PublicDashboardUpdates)
		r.Get("/public/dashboards/{publicId}/visuals/{visual}/tiles/{revision}/{z}/{x}/{y}.mvt", m.PublicVisualizationTile)
	}
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil {
		return
	}
	h := m.handler
	// Dashboard delivery is project-wide. The dashboard resource ID is the only
	// route identity; project/environment/generation are selected by the
	// composed serving runtime.
	protectResource := guard.ProtectWithResources
	if protectResource == nil {
		return
	}
	protectAuthoring := guard.ProtectWithAuthoring
	if protectAuthoring == nil {
		protectAuthoring = func(capability access.Capability, next http.HandlerFunc) http.HandlerFunc {
			return protectResource(capability, dashboardhttp.DashboardObjectRefs, next)
		}
	}
	r.Get("/dashboards/{dashboard}", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.Dashboard))
	r.Get("/dashboards/{dashboard}/pages/{page}", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.Page))
	// Draft creation is project-scoped. The shared project authorizer resolves
	// RESOURCE_EDIT through the explicit project role bundle (the same fallback
	// used by the generated project-root API), never as a direct unsupported
	// project-resource grant. Forks bind two distinct RESOURCE_EDIT decisions:
	// target project role bundle, then source dashboard resource.
	r.Get("/dashboards/new", protectResource(access.CapabilityResourceEdit, dashboardhttp.ProjectObjectRefs, h.DashboardDraftCreate))
	r.Post("/dashboards/new", protectResource(access.CapabilityResourceEdit, dashboardhttp.ProjectObjectRefs, h.DashboardDraftCreate))
	forkHandler := protectResource(
		access.CapabilityResourceEdit,
		dashboardhttp.ProjectObjectRefs,
		protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.DashboardDraftFork),
	)
	r.Get("/dashboards/{dashboard}/fork", forkHandler)
	r.Post("/dashboards/{dashboard}/fork", forkHandler)
	// Builder documents and mutations are edit-scoped. The application
	// boundary performs the exact authoring decision again before exposing a
	// draft revision or executing a command.
	r.Get("/dashboards/{dashboard}/edit", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilder))
	r.Get("/dashboards/{dashboard}/preview", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilderPreview))
	r.Get("/dashboards/{dashboard}/export.yaml", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilderExportYAML))
	r.Post("/dashboards/{dashboard}/draft/command", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilderCommand))
	// Builder filter state is a read-side exact-draft preview capability. It
	// shares authoring authorization but has dedicated endpoints and never
	// enters the published dashboard command/session routes.
	r.Post("/dashboards/{dashboard}/draft/filter", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilderFilterCommand))
	r.Post("/dashboards/{dashboard}/draft/filter-options", protectAuthoring(access.CapabilityResourceEdit, h.DashboardBuilderFilterOptions))
	r.Get("/dashboards/{dashboard}/visuals/{visual}/tiles/{revision}/{z}/{x}/{y}.mvt", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, m.VisualizationTile))
	r.Post("/dashboards/{dashboard}/commands/visual-window", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.VisualWindow))
	r.Post("/dashboards/{dashboard}/commands/select", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.Select))
	r.Post("/dashboards/{dashboard}/commands/spatial-select", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.SpatialSelect))
	r.Post("/dashboards/{dashboard}/commands/clear-selection", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.ClearSelection))
	r.Post("/dashboards/{dashboard}/commands/filter", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.FilterCommand))
	r.Post("/dashboards/{dashboard}/commands/filter-options", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.FilterOptions))
	r.Post("/dashboards/{dashboard}/commands/navigate", protectResource(access.CapabilityResourceRead, dashboardhttp.DashboardObjectRefs, h.Navigate))
}
