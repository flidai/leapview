package app

import (
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
)

const (
	routeLogin           = "login"
	routeCatalog         = "catalog"
	routeDashboard       = "dashboard"
	routeWorkspace       = "workspace"
	routeWorkspaceAsset  = "workspace_asset"
	routeConnections     = "connections"
	routeConnectionAsset = "connection_asset"
	routeData            = "data"
	routeChat            = "chat"
	routeAdmin           = "admin"
)

func configurePageStream(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy) {
	runtime.pageStreams = uitransport.NewPageStream(uitransport.PageStreamConfig{
		Trace: runtime.pageStreamTrace,
		Authorize: func(route, section string, next http.Handler) (http.Handler, bool) {
			switch route {
			case routeLogin:
				return next, true
			case routeCatalog:
				return routes.accessModule.ProtectAnyWorkspaceNamed("VIEW_ITEM", next), true
			case routeWorkspace, routeConnections:
				return routes.accessModule.ProtectAnyWorkspaceNamed("VIEW_ITEM", next), true
			case routeDashboard, routeWorkspaceAsset, routeConnectionAsset, routeData:
				return routes.accessModule.ProtectNamed("VIEW_ITEM", next), true
			case routeChat:
				return routes.accessModule.ProtectAnyWorkspaceNamed("VIEW_AGENT", next), true
			case routeAdmin:
				switch strings.TrimSpace(section) {
				case "profile", "security", "api-tokens":
					return routes.accessModule.ProtectNamed("", next), true
				case "general", "service-accounts", "authentication", "storage", "storage-v2", "storage-v2-detail", "agent", "system":
					return routes.accessModule.ProtectPlatformNamed("MANAGE_PLATFORM", next), true
				case "workspaces-admin":
					return routes.accessModule.ProtectGlobalNamed("MANAGE_WORKSPACE", next), true
				case "queries", "audit":
					return routes.accessModule.ProtectGlobalNamed("VIEW_AUDIT", next), true
				case "publications":
					return routes.accessModule.ProtectAnyWorkspaceNamed("MANAGE_PUBLICATIONS", next), true
				default:
					return routes.accessModule.ProtectGlobalNamed("MANAGE_GRANTS", next), true
				}
			default:
				return nil, false
			}
		},
		Handlers: map[string]http.Handler{
			routeDashboard: http.HandlerFunc(routes.dashboardModule.HTTP().Updates),
			routeChat:      http.HandlerFunc(routes.agentModule.HTTP().ChatUpdates),
			routeData:      http.HandlerFunc(routes.workspaceModule.HTTP().DataExplorerUpdates),
			routeAdmin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				adminHTTP := routes.adminModule.HTTP()
				switch strings.TrimSpace(r.URL.Query().Get("section")) {
				case "queries":
					adminHTTP.QueryUpdates(w, r)
				case "storage":
					adminHTTP.StorageSignalUpdates(w, r)
				default:
					adminHTTP.BootstrapUpdates(w, r)
				}
			}),
			routeWorkspaceAsset: http.HandlerFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				workspaceAssetUpdates(routes.workspaceModule, runtime.pageStreamTrace, w, r)
			})),
			routeConnectionAsset: http.HandlerFunc(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				workspaceAssetUpdates(routes.workspaceModule, runtime.pageStreamTrace, w, r)
			})),
			routeLogin: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = uitransport.PatchOnce(runtime.pageStreamTrace, w, r, routes.accessModule.LoginBootstrapSignals(r))
			}),
			routeCatalog: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				signals, err := routes.workspaceModule.CatalogBootstrapSignals(r, applicationLayout(routes.accessModule, routes.agentModule, routes.product, platform.assets, r))
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				uitransport.PatchAndWait(runtime.pageStreamTrace, w, r, signals)
			}),
			routeWorkspace:   http.HandlerFunc(routes.workspaceModule.HTTP().WorkspaceBootstrapUpdates),
			routeConnections: http.HandlerFunc(routes.workspaceModule.HTTP().ConnectionsBootstrapUpdates),
		},
	})
}

func workspaceAssetUpdates(workspaces *workspacemodule.Module, trace *pagestream.TraceStore, w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.URL.Query().Get("asset")) != "" {
		workspaces.HTTP().AssetUpdatesStream(w, r)
		return
	}
	uitransport.PatchAndWait(trace, w, r, pagestream.SignalPatch{"status": map[string]any{"loading": false, "error": ""}})
}
