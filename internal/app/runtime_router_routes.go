package app

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/observability"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/go-chi/chi/v5"
)

type routerMiddlewareDependencies struct {
	logger           *slog.Logger
	telemetry        *observability.Telemetry
	securityHeaders  apihttpmiddleware.SecurityHeadersConfig
	allowedHosts     []string
	requestBodyLimit apihttpmiddleware.RequestBodyLimitConfig
	requestLogging   bool
}

type platformRouteDependencies struct {
	dashboardTelemetry dashboardmodule.Telemetry
	rateLimits         apihttpmiddleware.RateLimitConfig
	desktopDiscovery   http.Handler
	health             *health
	apiProtocol        *apiprotocol.Protocol
}

type publicDashboardRouteDependencies struct {
	dashboard          *dashboardmodule.Module
	dashboardTelemetry dashboardmodule.Telemetry
	rateLimits         apihttpmiddleware.RateLimitConfig
}

type authenticatedRouteDependencies struct {
	access         *accessmodule.Module
	projectBrowser *projecthttp.BrowserHandler
	agent          *agentmodule.Module
	admin          *adminmodule.Module
	dashboard      *dashboardmodule.Module
	runtimeHost    *runtimehostmodule.Module
	pageStreams    *uitransport.PageStream
	rateLimits     apihttpmiddleware.RateLimitConfig
	candidates     candidateRouteDependencies
}

type apiRouteDependencies struct {
	persistenceConfigured bool
	auth                  *accessmodule.Auth
	agent                 *agentmodule.Module
	access                *accessmodule.Module
	managedData           *manageddatamodule.Module
	runtimeHost           *runtimehostmodule.Module
	rateLimits            apihttpmiddleware.RateLimitConfig
	scimBearerToken       string
	managedDataTus        http.Handler
	managedDataBootstrap  accessmodule.APIGenBootstrapAuthorizer
}

type staticRouteDependencies struct {
	dashboardAssets dashboardmodule.Assets
	assets          staticasset.Resolver
	apiProtocol     *apiprotocol.Protocol
}

// mountRouterMiddleware owns process-wide transport middleware. Keeping this
// separate from capability route mounting makes the order of security
// middleware explicit and prevents feature modules from silently changing it.
func mountRouterMiddleware(mux *chi.Mux, dependencies routerMiddlewareDependencies) {
	if dependencies.requestLogging {
		mux.Use(apihttpmiddleware.RequestLogger(dependencies.logger))
	}
	mux.Use(dependencies.telemetry.Middleware)
	mux.Use(apihttpmiddleware.PanicRecovery(dependencies.logger))
	mux.Use(apihttpmiddleware.SecurityHeadersMiddleware(dependencies.securityHeaders))
	mux.Use(apihttpmiddleware.AllowedHosts(dependencies.allowedHosts))
	mux.Use(apihttpmiddleware.RequestBodyLimit(dependencies.requestBodyLimit))
}

func mountPlatformRoutes(mux *chi.Mux, dependencies platformRouteDependencies) {
	mux.Get("/favicon.ico", favicon)
	mux.Get("/healthz", dependencies.health.Healthz)
	mux.Get("/readyz", dependencies.health.Readyz)
	mux.With(dependencies.rateLimits.PublicPage(func() {
		dependencies.dashboardTelemetry.PublicRateLimitObserved("desktop-discovery")
	})).Get(desktopdiscovery.WellKnownPath, dependencies.desktopDiscovery.ServeHTTP)
	mux.Get("/api/openapi.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAPIDescription(dependencies.apiProtocol, w, r)
	}))
	mux.Get("/api/docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { publicDocs(dependencies.apiProtocol, w, r) }))
}

func mountPublicDashboardRoutes(mux *chi.Mux, dependencies publicDashboardRouteDependencies) {
	mux.Group(func(r chi.Router) {
		r.Use(dependencies.rateLimits.PublicPage(func() { dependencies.dashboardTelemetry.PublicRateLimitObserved("page") }))
		dependencies.dashboard.MountPublicDocuments(r)
	})
	mux.Group(func(r chi.Router) {
		r.Use(dependencies.rateLimits.PublicCommand(func() { dependencies.dashboardTelemetry.PublicRateLimitObserved("command") }))
		dependencies.dashboard.MountPublicCommands(r)
	})
	dependencies.dashboard.MountPublicStream(mux.With(dependencies.rateLimits.PublicStream(func() { dependencies.dashboardTelemetry.PublicRateLimitObserved("stream") })))
}

func mountDevelopmentRoutes(mux *chi.Mux, trace *pagestream.TraceStore) {
	if trace == nil {
		return
	}
	traceHandler := uitransport.TraceHandler{Store: trace}
	mux.Get("/__dev/pagestream/traces", traceHandler.Traces)
	mux.Get("/__dev/pagestream/signals", traceHandler.Signals)
}

func mountAuthenticatedRoutes(mux *chi.Mux, dependencies authenticatedRouteDependencies, csrf func(http.Handler) http.Handler) {
	mux.Group(func(r chi.Router) {
		r.Use(csrf)
		r.With(dependencies.rateLimits.Updates()).Get("/updates", dependencies.pageStreams.ServeHTTP)
		if dependencies.projectBrowser != nil {
			dependencies.projectBrowser.MountAuthenticated(r)
		} else {
			r.Get("/", dependencies.access.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			})).ServeHTTP)
		}
		candidateProjectGuard := func(next http.HandlerFunc) http.HandlerFunc {
			return protectProjectResources(dependencies.access, dependencies.runtimeHost, access.CapabilityProjectAdmin, activeProjectResource, next)
		}
		candidateReviewGuard := func(next http.HandlerFunc) http.HandlerFunc {
			return protectProjectResources(dependencies.access, dependencies.runtimeHost, access.CapabilityResourceEdit, activeProjectResource, next)
		}
		r.Get("/candidates/{candidate}", candidateProjectGuard(func(w http.ResponseWriter, request *http.Request) {
			candidatePreview(dependencies.candidates, w, request)
		}))
		r.Get("/candidates/{candidate}/review", candidateReviewGuard(func(w http.ResponseWriter, request *http.Request) {
			candidateReview(dependencies.candidates, w, request)
		}))
		r.Get("/candidates/{candidate}/dashboards/{dashboard}", candidateProjectGuard(func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardDocument(dependencies.candidates, w, request)
		}))
		r.Get("/candidates/{candidate}/dashboards/{dashboard}/pages/{page}", candidateProjectGuard(func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardDocument(dependencies.candidates, w, request)
		}))
		r.With(dependencies.rateLimits.Updates()).Get("/candidates/{candidate}/updates", candidateProjectGuard(func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardUpdates(dependencies.candidates, w, request)
		}))
		r.Post("/candidates/{candidate}/commands/{command}", candidateProjectGuard(func(w http.ResponseWriter, request *http.Request) {
			candidateDashboardCommand(dependencies.candidates, w, request)
		}))
		dependencies.agent.MountAuthenticated(r, agentmodule.RouteGuard{Authenticate: dependencies.access.Authenticate, RequirePlatformAdmin: dependencies.access.RequirePlatformAdmin})
		dependencies.admin.MountAuthenticated(r, adminmodule.RouteGuard{Authenticate: dependencies.access.Authenticate, RequirePlatformAdmin: dependencies.access.RequirePlatformAdmin})
		dependencies.dashboard.MountAuthenticated(r, dashboardmodule.RouteGuard{ProtectWithResources: func(capability access.Capability, resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef, next http.HandlerFunc) http.HandlerFunc {
			return protectProjectResources(dependencies.access, dependencies.runtimeHost, capability, resolve, next)
		}})
		dependencies.access.MountAuthenticatedBrowser(r)
	})
}

func mountAuthenticationRoutes(mux *chi.Mux, accessModule *accessmodule.Module, rateLimits apihttpmiddleware.RateLimitConfig, csrf func(http.Handler) http.Handler) {
	mux.Group(func(r chi.Router) {
		r.Use(rateLimits.Auth())
		r.Use(csrf)
		accessModule.MountLocalLogin(r)
	})
	mux.Group(func(r chi.Router) {
		r.Use(rateLimits.Auth())
		accessModule.MountOAuthEndpoints(r)
	})
	accessModule.MountOAuthMetadata(mux)
}

func mountAPIRoutes(mux *chi.Mux, dependencies apiRouteDependencies, publicProtocol func(http.Handler) http.Handler, registerAPIGen func(chi.Router)) {
	if !dependencies.persistenceConfigured {
		return
	}
	if dependencies.auth != nil {
		dependencies.agent.MountMCP(mux.With(dependencies.rateLimits.API()))
	}
	if strings.TrimSpace(dependencies.scimBearerToken) != "" {
		_ = dependencies.access.MountSCIM(mux.With(dependencies.rateLimits.API()), dependencies.scimBearerToken)
	}
	mux.Group(func(r chi.Router) {
		r.Use(dependencies.rateLimits.API())
		r.Use(publicProtocol)
		dependencies.managedData.MountTus(r, dependencies.managedDataTus, func(next http.Handler) http.Handler {
			return protectManagedDataTransportWithBootstrap(dependencies.access, dependencies.runtimeHost, dependencies.managedData, dependencies.managedDataBootstrap, next)
		})
		registerAPIGen(r)
	})
}

func mountStaticAndErrorRoutes(mux *chi.Mux, dependencies staticRouteDependencies) {
	if dependencies.dashboardAssets != nil {
		mux.Handle("/map-assets/*", dependencies.dashboardAssets.Handler())
	}
	mux.Handle("/static/*", staticAssetCache(dependencies.assets, http.StripPrefix("/static/", http.FileServer(http.Dir("static")))))
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
			if dependencies.apiProtocol.Authenticate(w, r) {
				apitransport.WriteProblem(w, r, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "The requested method is not supported for this API route", nil)
			}
			return
		}
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	})
}
