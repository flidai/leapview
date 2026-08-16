package app

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

const (
	routeLogin            = "login"
	routeDashboard        = "dashboard"
	routeDashboardBuilder = "dashboard_builder"
	routeChat             = "chat"
	routeAdmin            = "admin"
)

func configurePageStream(routes *capabilityRoutes, runtime *runtimeServices, _ *platformServices, _ *httpPolicy) {
	runtime.pageStreams = uitransport.NewPageStream(uitransport.PageStreamConfig{
		Trace: runtime.pageStreamTrace,
		Authorize: func(route, section string, next http.Handler) (http.Handler, bool) {
			switch route {
			case routeLogin:
				return next, true
			case routeDashboard:
				return protectPageStreamResource(
					routes.accessModule, runtime.runtimeHostModule,
					access.CapabilityResourceRead, dashboardPageStreamResource,
					next,
				), true
			case routeDashboardBuilder:
				return protectPageStreamResource(
					routes.accessModule, runtime.runtimeHostModule,
					access.CapabilityResourceEdit, dashboardPageStreamResource,
					next,
				), true
			case routeChat:
				return routes.accessModule.ProtectNamed("", next), true
			case routeAdmin:
				switch strings.TrimSpace(section) {
				case "profile", "security", "api-tokens":
					return routes.accessModule.ProtectNamed("", next), true
				case "general", "service-accounts", "authentication", "storage", "storage-detail", "agent", "system":
					return routes.accessModule.ProtectPlatformNamed("MANAGE_PLATFORM", next), true
				case "workspaces-admin":
					return routes.accessModule.ProtectGlobalNamed("MANAGE_WORKSPACE", next), true
				case "queries", "audit":
					return routes.accessModule.ProtectGlobalNamed("VIEW_AUDIT", next), true
				case "publications":
					// Publication visibility and mutation affordances are filtered by
					// the admin read model. The stream only establishes identity; it
					// must not reintroduce a workspace-wide publication guard.
					return routes.accessModule.ProtectNamed("", next), true
				default:
					return routes.accessModule.ProtectGlobalNamed("MANAGE_GRANTS", next), true
				}
			default:
				return nil, false
			}
		},
		Handlers: map[string]http.Handler{
			routeDashboard:        http.HandlerFunc(routes.dashboardModule.HTTP().Updates),
			routeDashboardBuilder: http.HandlerFunc(routes.dashboardModule.HTTP().DashboardBuilderUpdates),
			routeChat:             http.HandlerFunc(routes.agentModule.HTTP().ChatUpdates),
			routeAdmin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				adminHTTP := routes.adminModule.HTTP()
				if strings.TrimSpace(r.URL.Query().Get("section")) == "queries" {
					adminHTTP.QueryUpdates(w, r)
					return
				}
				adminHTTP.BootstrapUpdates(w, r)
			}),
			routeLogin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = uitransport.PatchOnce(runtime.pageStreamTrace, w, r, routes.accessModule.LoginBootstrapSignals(r))
			}),
		},
	})
}

func dashboardPageStreamResource(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
	dashboardValues, ok := r.URL.Query()["dashboard"]
	if !ok || len(dashboardValues) != 1 {
		return nil
	}
	dashboardID, err := projectgraph.NewResourceID(strings.TrimSpace(dashboardValues[0]))
	if err != nil {
		return nil
	}
	resource, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{resource}
}

func protectPageStreamResource(
	accessModule *accessmodule.Module,
	runtimeHost *runtimehostmodule.Module,
	capability access.Capability,
	resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runtimeHost == nil || resolve == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		// Validate the selector before the project-resource guard can honor a
		// development bypass. A stream without an explicit dashboard identity
		// must never fall back to the metrics default dashboard.
		if len(resolve(r, runtimeHost.ProjectID())) == 0 {
			http.NotFound(w, r)
			return
		}
		protectProjectResources(accessModule, runtimeHost, capability, resolve, next.ServeHTTP).ServeHTTP(w, r)
	})
}
