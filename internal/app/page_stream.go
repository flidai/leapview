package app

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

const (
	routeLogin            = "login"
	routeCatalog          = "catalog"
	routeData             = "data"
	routeConnections      = "connections"
	routeConnectionAsset  = "connection_asset"
	routePipelines        = "pipelines"
	routeAsset            = "asset"
	routeDashboard        = "dashboard"
	routeDashboardBuilder = "dashboard_builder"
	routeChat             = "chat"
	routeAdmin            = "admin"
)

func configurePageStream(routes *capabilityRoutes, runtime *runtimeServices, _ *platformServices, _ *httpPolicy) {
	authorize := func(route, section string, next http.Handler) (http.Handler, bool) {
		switch route {
		case routeLogin:
			return next, true
		case routeCatalog, routeData, routeConnections, routeConnectionAsset, routePipelines, routeAsset:
			if routes.projectBrowser == nil {
				return nil, false
			}
			return routes.projectBrowser.ProtectStream(next), true
		case routeDashboard:
			return protectPageStreamResource(
				routes.accessModule, runtime.runtimeHostModule,
				access.ActionDashboardRead, dashboardPageStreamResource,
				next,
			), true
		case routeDashboardBuilder:
			// Drafts exist before the active serving graph. Authenticate and reject
			// malformed selectors here; Builder makes the exact durable and typed
			// dashboard decision before emitting the projection.
			return routes.accessModule.Authenticate(validateDashboardBuilderPageStream(next)), true
		case routeChat:
			return routes.accessModule.Authenticate(next), true
		case routeAdmin:
			switch strings.TrimSpace(section) {
			case "", "profile", "security", "api-tokens", "api-token-new", "api-token-edit", "archived-chats":
				return routes.accessModule.Authenticate(next), true
			case "general", "access", "service-accounts", "service-accounts-detail", "service-accounts-new", "authentication", "storage", "storage-detail", "agent", "system", "principals", "principal-detail", "groups", "group-detail", "queries", "audit", "publications":
				return routes.accessModule.RequirePlatformAdmin(next), true
			default:
				return nil, false
			}
		default:
			return nil, false
		}
	}
	handlers := map[string]http.Handler{
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
			uitransport.PatchOnce(w, r, routes.accessModule.LoginBootstrapSignals(r))
		}),
	}
	if routes.projectBrowser != nil {
		handlers[routeCatalog] = http.HandlerFunc(routes.projectBrowser.Updates)
		handlers[routeData] = http.HandlerFunc(routes.projectBrowser.Updates)
		handlers[routeConnections] = http.HandlerFunc(routes.projectBrowser.Updates)
		handlers[routeConnectionAsset] = http.HandlerFunc(routes.projectBrowser.Updates)
		handlers[routePipelines] = http.HandlerFunc(routes.projectBrowser.Updates)
		handlers[routeAsset] = http.HandlerFunc(routes.projectBrowser.Updates)
	}
	runtime.pageStreams = uitransport.NewPageStream(uitransport.PageStreamConfig{Authorize: authorize, Handlers: handlers})
}

func validateDashboardBuilderPageStream(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dashboardID := strings.TrimSpace(dashboardBuilderPageStreamDashboardID(r))
		if err := authoring.ValidateDashboardID(authoring.DashboardID(dashboardID)); err != nil {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func dashboardPageStreamResource(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
	dashboardValues, ok := r.URL.Query()["dashboard"]
	if !ok || len(dashboardValues) != 1 {
		return nil
	}
	dashboardID, err := projectgraph.NewResourceID(dashboardValues[0])
	if err != nil {
		return nil
	}
	resource, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{resource}
}

func dashboardBuilderPageStreamDashboardID(r *http.Request) string {
	values, ok := r.URL.Query()["dashboard"]
	if !ok || len(values) != 1 {
		return ""
	}
	return values[0]
}

func protectPageStreamResource(
	accessModule *accessmodule.Module,
	runtimeHost *runtimehostmodule.Module,
	action access.Action,
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
		protectProjectResourcesWithTypedAction(accessModule, runtimeHost, action, resolve, next.ServeHTTP).ServeHTTP(w, r)
	})
}
