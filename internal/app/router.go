package app

import (
	"net/http"
	"sort"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
	"github.com/go-chi/chi/v5"
)

func Routes(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy) http.Handler {
	mux := chi.NewRouter()
	candidates := candidateRouteDependencies{
		access: routes.accessModule, agent: routes.agentModule, product: routes.product, assets: platform.assets,
		dashboards: routes.dashboardModule, deployments: routes.deploymentModule,
		runtimeHost: runtime.runtimeHostModule, candidateMetrics: runtime.candidateMetrics,
	}
	csrf := func(next http.Handler) http.Handler {
		return csrfMiddleware(routes.accessModule, next)
	}
	publicProtocol := func(next http.Handler) http.Handler {
		return publicProtocolMiddleware(platform.apiProtocol, next)
	}
	if policy.requestLogging {
		mux.Use(apihttpmiddleware.RequestLogger(platform.logger))
	}
	mux.Use(platform.telemetry.Middleware)
	mux.Use(apihttpmiddleware.PanicRecovery(platform.logger))
	mux.Use(apihttpmiddleware.SecurityHeadersMiddleware(policy.securityHeaders))
	mux.Use(apihttpmiddleware.AllowedHosts(policy.allowedHosts))
	mux.Use(apihttpmiddleware.RequestBodyLimit(policy.requestBodyLimit))
	mux.Get("/favicon.ico", favicon)
	mux.Get("/healthz", platform.health.Healthz)
	mux.Get("/readyz", platform.health.Readyz)
	mux.With(policy.rateLimits.PublicPage(func() {
		routes.dashboardTelemetry.PublicRateLimitObserved("desktop-discovery")
	})).Get(desktopdiscovery.WellKnownPath, policy.desktopDiscovery.ServeHTTP)
	mux.Get("/api/openapi.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAPIDescription(platform.apiProtocol, w, r)
	}))
	mux.Get("/api/docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { publicDocs(platform.apiProtocol, w, r) }))
	mux.Group(func(r chi.Router) {
		r.Use(policy.rateLimits.PublicPage(func() { routes.dashboardTelemetry.PublicRateLimitObserved("page") }))
		routes.dashboardModule.MountPublicDocuments(r)
	})
	mux.Group(func(r chi.Router) {
		r.Use(policy.rateLimits.PublicCommand(func() { routes.dashboardTelemetry.PublicRateLimitObserved("command") }))
		routes.dashboardModule.MountPublicCommands(r)
	})
	routes.dashboardModule.MountPublicStream(mux.With(policy.rateLimits.PublicStream(func() { routes.dashboardTelemetry.PublicRateLimitObserved("stream") })))
	if runtime.pageStreamTrace != nil {
		traceHandler := uitransport.TraceHandler{Store: runtime.pageStreamTrace}
		mux.Get("/__dev/pagestream/traces", traceHandler.Traces)
		mux.Get("/__dev/pagestream/signals", traceHandler.Signals)
	}
	mux.With(policy.rateLimits.Auth()).Handle("/metrics", platform.telemetry.MetricsHandler(policy.metricsBearerToken, accessmodule.BearerToken))
	mux.With(csrf).Group(routes.accessModule.MountLoginPage)
	mux.Group(func(r chi.Router) {
		r.Use(csrf)
		r.With(policy.rateLimits.Updates()).Get("/updates", runtime.pageStreams.ServeHTTP)
		r.Get("/", routes.accessModule.ProtectAnyWorkspace(accessmodule.PrivilegeViewItem, projectHome(runtime)))
		r.Get("/candidates/{candidate}", routes.accessModule.Protect(accessmodule.PrivilegeAuthorProject, func(w http.ResponseWriter, request *http.Request) {
			candidatePreview(candidates, w, request)
		}))
		r.Get("/candidates/{candidate}/dashboards/{dashboard}", routes.accessModule.Protect(accessmodule.PrivilegeAuthorProject, func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardDocument(candidates, w, request)
		}))
		r.Get("/candidates/{candidate}/dashboards/{dashboard}/pages/{page}", routes.accessModule.Protect(accessmodule.PrivilegeAuthorProject, func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardDocument(candidates, w, request)
		}))
		r.With(policy.rateLimits.Updates()).Get("/candidates/{candidate}/updates", routes.accessModule.Protect(accessmodule.PrivilegeAuthorProject, func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardUpdates(candidates, w, request)
		}))
		r.Post("/candidates/{candidate}/commands/{command}", routes.accessModule.Protect(accessmodule.PrivilegeAuthorProject, func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardCommand(candidates, w, request)
		}))
		routes.workspaceModule.MountAuthenticated(r, workspacemodule.RouteGuard{
			Protect: routes.accessModule.Protect, ProtectAnyWorkspace: routes.accessModule.ProtectAnyWorkspace,
			ProtectWithObjects: routes.accessModule.ProtectWithObjects, AssetObjectRefs: routes.workspaceModule.AssetObjectRefs,
		})
		routes.agentModule.MountAuthenticated(r, agentmodule.RouteGuard{
			Protect: routes.accessModule.Protect, ProtectGlobal: routes.accessModule.ProtectGlobal,
			ProtectPlatform: routes.accessModule.ProtectPlatform,
		})
		r.Get("/chat", redirectLegacyChat)
		r.Get("/chat/updates", http.NotFound)
		r.Get("/chat/*", redirectLegacyChat)
		r.Post("/chat/turns", redirectLegacyChat)
		routes.adminModule.MountAuthenticated(r, adminmodule.RouteGuard{
			Protect: routes.accessModule.Protect, ProtectGlobal: routes.accessModule.ProtectGlobal,
			ProtectPlatform:     routes.accessModule.ProtectPlatform,
			ProtectAnyWorkspace: routes.accessModule.ProtectAnyWorkspace,
		})
		routes.dashboardModule.MountAuthenticated(r, dashboardmodule.RouteGuard{
			Protect: routes.accessModule.Protect, ProtectWithObjects: routes.accessModule.ProtectWithObjects,
		})
		routes.accessModule.MountAuthenticatedBrowser(r)
	})
	mux.Group(func(r chi.Router) {
		r.Use(policy.rateLimits.Auth())
		r.Use(csrf)
		routes.accessModule.MountLocalLogin(r)
	})
	mux.Group(func(r chi.Router) {
		r.Use(policy.rateLimits.Auth())
		routes.accessModule.MountOAuthEndpoints(r)
	})
	routes.accessModule.MountOAuthMetadata(mux)
	if runtime.persistenceConfigured {
		if platform.auth != nil {
			routes.agentModule.MountMCP(mux.With(policy.rateLimits.API()))
		}
		if strings.TrimSpace(policy.scimBearerToken) != "" {
			_ = routes.accessModule.MountSCIM(mux.With(policy.rateLimits.API()), policy.scimBearerToken)
		}
		mux.Group(func(r chi.Router) {
			r.Use(policy.rateLimits.API())
			r.Use(publicProtocol)
			routes.managedDataModule.MountTus(r, policy.managedDataTus, routes.accessModule.ProtectIngestData)
			apiaggregate.RegisterAPIGenRoutes(r, platform.apiGenServers)
		})
	}
	if routes.dashboardAssets != nil {
		mux.Handle("/map-assets/*", routes.dashboardAssets.Handler())
	}
	mux.Handle("/static/*", staticAssetCache(platform.assets, http.StripPrefix("/static/", http.FileServer(http.Dir("static")))))
	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if isPublicAPIPath(r.URL.Path) {
			apiprotocol.PrepareRequest(w, r)
			apitransport.WriteProblem(w, r, http.StatusNotFound, "API_ROUTE_NOT_FOUND", "The requested API route does not exist", nil)
			return
		}
		http.NotFound(w, r)
	})
	registeredMethods := registeredRouteMethods(mux)
	mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		setAllowedMethods(w.Header(), mux, registeredMethods, r.URL.Path)
		if isPublicAPIPath(r.URL.Path) {
			if platform.apiProtocol.Authenticate(w, r) {
				apitransport.WriteProblem(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The requested method is not supported for this API route", nil)
			}
			return
		}
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	})

	return mux
}

func registeredRouteMethods(routes chi.Routes) []string {
	registered := make(map[string]struct{})
	_ = chi.Walk(routes, func(method, _ string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != "*" {
			registered[method] = struct{}{}
		}
		return nil
	})
	methods := make([]string, 0, len(registered))
	for method := range registered {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

func setAllowedMethods(header http.Header, routes chi.Routes, methods []string, path string) {
	for _, method := range methods {
		if routes.Match(chi.NewRouteContext(), method, path) {
			header.Add("Allow", method)
		}
	}
}

func isPublicAPIPath(path string) bool {
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/") || path == "/upload-protocols" || strings.HasPrefix(path, "/upload-protocols/")
}

func redirectLegacyChat(w http.ResponseWriter, r *http.Request) {
	target := "/chats" + strings.TrimPrefix(r.URL.Path, "/chat")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

// projectHome keeps the Insights entry point independent of workspace
// selection. The composed dashboard runtime owns the single project graph;
// redirecting to its default dashboard preserves the existing shell and
// Datastar bootstrap flow without inventing a workspace identifier.
func projectHome(runtime *runtimeServices) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtime == nil || runtime.metrics == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		dashboardID := strings.TrimSpace(runtime.metrics.DefaultDashboardID())
		if dashboardID == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/dashboards/"+dashboardID, http.StatusFound)
	}
}

func protectGlobalAgent(access *accessmodule.Module, privilege accessmodule.Privilege, next http.Handler) http.Handler {
	return access.ProtectGlobal(privilege, next.ServeHTTP)
}

func protectAnyWorkspace(access *accessmodule.Module, privilege accessmodule.Privilege, next http.Handler) http.Handler {
	return access.ProtectAnyWorkspace(privilege, next.ServeHTTP)
}

func protect(access *accessmodule.Module, privilege accessmodule.Privilege, next http.Handler) http.Handler {
	return access.ProtectHandler(privilege, next)
}

func protectGlobal(access *accessmodule.Module, privilege accessmodule.Privilege, next http.Handler) http.Handler {
	return access.ProtectGlobal(privilege, next.ServeHTTP)
}

func protectWithObjects(access *accessmodule.Module, privilege accessmodule.Privilege, objectResolver accessmodule.ObjectResolver, next http.Handler) http.Handler {
	return access.ProtectHandlerWithObjects(privilege, objectResolver, next)
}

func csrfMiddleware(access *accessmodule.Module, next http.Handler) http.Handler {
	return access.CSRFMiddleware(next)
}

func staticAssetCache(assets staticasset.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := assets.Version()
		switch {
		case version != "dev" && r.URL.Query().Get("v") == version:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case immutableStaticPath(r.URL.Path):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		case fontStaticPath(r.URL.Path):
			w.Header().Set("Cache-Control", "public, max-age=86400")
		default:
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func immutableStaticPath(path string) bool {
	return strings.HasPrefix(path, "/static/chunks/")
}

func fontStaticPath(path string) bool {
	return strings.HasPrefix(path, "/static/files/") && strings.HasSuffix(path, ".woff2")
}

func favicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="6" fill="#0969da"/><path d="M8 22h16v3H8zm1-5h4v4H9zm5-7h4v11h-4zm5 4h4v7h-4z" fill="#fff"/></svg>`))
}
