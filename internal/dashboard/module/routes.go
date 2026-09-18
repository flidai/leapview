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
	// ProtectWithResourceAction binds typed credentials to an explicit
	// action/resource pair. The legacy callback remains available for callers
	// that have not migrated a route contract yet.
	ProtectWithResourceAction func(access.Capability, access.Action, func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, http.HandlerFunc) http.HandlerFunc
	// ProtectWithAuthoring authorizes repository-backed draft routes. Unlike
	// graph resources, a newly created dashboard is not present in the active
	// serving generation until publication/deployment.
	ProtectWithAuthoring       func(access.Capability, http.HandlerFunc) http.HandlerFunc
	ProtectWithAuthoringAction func(access.Capability, access.Action, http.HandlerFunc) http.HandlerFunc
	// ProtectWithAuthoringCommand authenticates a body-dependent authoring
	// command. The command handler parses its closed payload union and applies
	// the exact typed and durable action before loading or mutating a draft;
	// unlike the fixed-action callback, this permits publish/archive commands
	// whose action is not known from the route alone.
	ProtectWithAuthoringCommand func(http.HandlerFunc) http.HandlerFunc
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
	protectResourceAction := guard.ProtectWithResourceAction
	if protectResourceAction == nil {
		protectResourceAction = func(capability access.Capability, _ access.Action, resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
			return protectResource(capability, resolve, next)
		}
	}
	protectAuthoring := guard.ProtectWithAuthoring
	if protectAuthoring == nil {
		protectAuthoring = func(capability access.Capability, next http.HandlerFunc) http.HandlerFunc {
			return protectResource(capability, dashboardhttp.DashboardObjectRefs, next)
		}
	}
	protectAuthoringAction := guard.ProtectWithAuthoringAction
	if protectAuthoringAction == nil {
		protectAuthoringAction = func(capability access.Capability, _ access.Action, next http.HandlerFunc) http.HandlerFunc {
			return protectAuthoring(capability, next)
		}
	}
	protectAuthoringCommand := guard.ProtectWithAuthoringCommand
	if protectAuthoringCommand == nil {
		// Compatibility for compositions that have not installed the
		// body-dependent command boundary yet. Production composition supplies
		// an authentication-only wrapper because DashboardBuilderCommand then
		// performs exact typed authorization and the transactional authoring
		// service performs exact durable authorization after parsing the command.
		protectAuthoringCommand = func(next http.HandlerFunc) http.HandlerFunc {
			return protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, next)
		}
	}
	r.Get("/dashboards/{dashboard}", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.Dashboard))
	r.Get("/dashboards/{dashboard}/pages/{page}", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.Page))
	// Draft creation is project-scoped. The shared project authorizer resolves
	// RESOURCE_EDIT through the explicit project role bundle (the same fallback
	// used by the generated project-root API), never as a direct unsupported
	// project-resource grant. Forks bind two distinct RESOURCE_EDIT decisions:
	// target project role bundle, then source dashboard resource.
	r.Get("/dashboards/new", protectResourceAction(access.CapabilityResourceEdit, access.ActionDashboardCreate, dashboardhttp.ProjectObjectRefs, h.DashboardDraftCreate))
	r.Post("/dashboards/new", protectResourceAction(access.CapabilityResourceEdit, access.ActionDashboardCreate, dashboardhttp.ProjectObjectRefs, h.DashboardDraftCreate))
	forkHandler := protectResourceAction(
		access.CapabilityResourceEdit,
		access.ActionDashboardCreate,
		dashboardhttp.ProjectObjectRefs,
		protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.DashboardDraftFork),
	)
	r.Get("/dashboards/{dashboard}/fork", forkHandler)
	r.Post("/dashboards/{dashboard}/fork", forkHandler)
	// Builder documents and mutations are edit-scoped. The application
	// boundary performs the exact authoring decision again before exposing a
	// draft revision or executing a command.
	r.Get("/dashboards/{dashboard}/edit", protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, h.DashboardBuilder))
	r.Post("/dashboards/{dashboard}/archive", protectAuthoringAction(access.CapabilityResourceManage, access.ActionDashboardDelete, h.DashboardArchive))
	r.Get("/dashboards/{dashboard}/preview", protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, h.DashboardBuilderPreview))
	r.Get("/dashboards/{dashboard}/export.yaml", protectAuthoringAction(access.CapabilityResourceRead, access.ActionDashboardRead, h.DashboardBuilderExportYAML))
	r.Post("/dashboards/{dashboard}/draft/command", protectAuthoringCommand(h.DashboardBuilderCommand))
	// Builder filter state is a read-side exact-draft preview capability. It
	// shares authoring authorization but has dedicated endpoints and never
	// enters the published dashboard command/session routes.
	r.Post("/dashboards/{dashboard}/draft/filter", protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, h.DashboardBuilderFilterCommand))
	r.Post("/dashboards/{dashboard}/draft/filter-options", protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, h.DashboardBuilderFilterOptions))
	r.Post("/dashboards/{dashboard}/draft/visual-window", protectAuthoringAction(access.CapabilityResourceEdit, access.ActionDashboardUpdate, h.DashboardBuilderVisualWindow))
	r.Get("/dashboards/{dashboard}/visuals/{visual}/tiles/{revision}/{z}/{x}/{y}.mvt", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, m.VisualizationTile))
	r.Post("/dashboards/{dashboard}/commands/visual-window", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.VisualWindow))
	r.Post("/dashboards/{dashboard}/commands/select", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.Select))
	r.Post("/dashboards/{dashboard}/commands/spatial-select", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.SpatialSelect))
	r.Post("/dashboards/{dashboard}/commands/clear-selection", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.ClearSelection))
	r.Post("/dashboards/{dashboard}/commands/filter", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.FilterCommand))
	r.Post("/dashboards/{dashboard}/commands/filter-options", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.FilterOptions))
	r.Post("/dashboards/{dashboard}/commands/navigate", protectResourceAction(access.CapabilityResourceRead, access.ActionDashboardRead, dashboardhttp.DashboardObjectRefs, h.Navigate))
}
