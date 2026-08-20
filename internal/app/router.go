package app

import (
	"net/http"
	"sort"
	"strings"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
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
	registerAPIGen := func(r chi.Router) {
		apiaggregate.RegisterAPIGenRoutes(r, platform.apiGenServers)
	}
	mountRouterMiddleware(mux, routerMiddlewareDependencies{
		logger: platform.logger, telemetry: platform.telemetry, securityHeaders: policy.securityHeaders,
		allowedHosts: policy.allowedHosts, requestBodyLimit: policy.requestBodyLimit, requestLogging: policy.requestLogging,
	})
	mountPlatformRoutes(mux, platformRouteDependencies{
		dashboardTelemetry: routes.dashboardTelemetry, rateLimits: policy.rateLimits,
		desktopDiscovery: policy.desktopDiscovery, health: platform.health, apiProtocol: platform.apiProtocol,
	})
	mountPublicDashboardRoutes(mux, publicDashboardRouteDependencies{
		dashboard: routes.dashboardModule, dashboardTelemetry: routes.dashboardTelemetry, rateLimits: policy.rateLimits,
	})
	mountDevelopmentRoutes(mux, runtime.pageStreamTrace)
	mux.With(policy.rateLimits.Auth()).Handle("/metrics", platform.telemetry.MetricsHandler(policy.metricsBearerToken, accessmodule.BearerToken))
	mux.With(csrf).Group(routes.accessModule.MountLoginPage)
	mountAuthenticatedRoutes(mux, authenticatedRouteDependencies{
		access: routes.accessModule, projectBrowser: routes.projectBrowser, agent: routes.agentModule,
		admin: routes.adminModule, dashboard: routes.dashboardModule, runtimeHost: runtime.runtimeHostModule,
		pageStreams: runtime.pageStreams, rateLimits: policy.rateLimits, candidates: candidates,
	}, csrf)
	mountAuthenticationRoutes(mux, routes.accessModule, policy.rateLimits, csrf)
	mountAPIRoutes(mux, apiRouteDependencies{
		persistenceConfigured: runtime.persistenceConfigured, auth: platform.auth, agent: routes.agentModule,
		access: routes.accessModule, managedData: routes.managedDataModule, runtimeHost: runtime.runtimeHostModule,
		rateLimits: policy.rateLimits, scimBearerToken: policy.scimBearerToken, managedDataTus: policy.managedDataTus,
		managedDataBootstrap: policy.managedDataBootstrap,
	}, publicProtocol, registerAPIGen)
	mountStaticAndErrorRoutes(mux, staticRouteDependencies{
		dashboardAssets: routes.dashboardAssets, assets: platform.assets, apiProtocol: platform.apiProtocol,
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

// projectHome keeps the Insights entry point independent of project
// selection. The composed runtime owns the single project graph; canonical
// browser surfaces are mounted by project/browser without a request selector.
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
