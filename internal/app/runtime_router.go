package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apiapigenruntime "github.com/flidai/leapview/internal/app/api/apigenruntime"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	"github.com/flidai/leapview/internal/app/brand"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	platformlifecycle "github.com/flidai/leapview/internal/platform/lifecycle"
	"github.com/flidai/leapview/internal/platform/observability"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/go-chi/chi/v5"
)

type QueryMetrics = dashboardmodule.Metrics

type capabilityRoutes struct {
	accessModule       *accessmodule.Module
	managedDataModule  *manageddatamodule.Module
	deploymentModule   *deploymentmodule.Module
	dashboardModule    *dashboardmodule.Module
	dashboardAuthoring *dashboardmodule.AuthoringApplication
	dashboardAssets    dashboardmodule.Assets
	agentModule        *agentmodule.Module
	releaseModule      *releasemodule.Module
	refreshModule      *refreshmodule.Module
	adminModule        *adminmodule.Module
	product            *adminmodule.ProductService
	dashboardTelemetry dashboardmodule.Telemetry
	projectCatalog     *projectcatalog.Service
	projectBrowser     *projecthttp.BrowserHandler
}

type runtimeServices struct {
	analyticsModule       *analyticsmodule.Module
	metrics               QueryMetrics
	workloads             workloadControl
	broker                *pagestream.Broker
	pageStreamTrace       *pagestream.TraceStore
	pageStreams           *uitransport.PageStream
	persistenceConfigured bool
	platformHealth        platformHealth
	storageRetention      *servingstatemodule.Retention
	queryAuditProvider    adminmodule.QueryAuditReaderProvider
	candidateMetrics      func(runtimehostmodule.Provider, projectgraph.ResourceID) QueryMetrics
	runtimeHostModule     *runtimehostmodule.Module
	projectID             projectgraph.ResourceID
	projectIDResolver     func(context.Context) (projectgraph.ResourceID, error)
}

// resolveProjectID returns the exact project bound to the active serving
// lease. A fresh installation has no project until its first activation, so
// every project-dependent surface must resolve this at operation time rather
// than retaining runtime.projectID from startup composition.
func (r *runtimeServices) resolveProjectID(ctx context.Context) (projectgraph.ResourceID, error) {
	if r == nil {
		return "", errors.New("runtime services are unavailable")
	}
	if r.projectIDResolver != nil {
		projectID, err := r.projectIDResolver(ctx)
		if err != nil {
			return "", err
		}
		if err := projectID.Validate(); err != nil {
			return "", fmt.Errorf("active project identity is invalid: %w", err)
		}
		return projectID, nil
	}
	if r.runtimeHostModule == nil {
		return "", errors.New("active runtime host is unavailable")
	}
	lease, err := r.runtimeHostModule.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		return "", fmt.Errorf("active runtime serving identity is invalid: %w", err)
	}
	return identity.ProjectID, nil
}

type platformServices struct {
	asyncJobs               jobs.Repository
	jobModule               *jobsmodule.Module
	auth                    *accessmodule.Auth
	assets                  staticasset.Resolver
	buildIdentity           buildinfo.Identity
	telemetry               *observability.Telemetry
	health                  *health
	logger                  *slog.Logger
	workers                 *platformlifecycle.Group
	apiProtocol             *apiprotocol.Protocol
	apiGenServers           apiaggregate.Servers
	requireActiveDeployment bool
}

type httpPolicy struct {
	defaultEnvironment   string
	scimBearerToken      string
	metricsBearerToken   string
	allowedHosts         []string
	rateLimits           apihttpmiddleware.RateLimitConfig
	securityHeaders      apihttpmiddleware.SecurityHeadersConfig
	requestBodyLimit     apihttpmiddleware.RequestBodyLimitConfig
	requestLogging       bool
	managedDataTus       http.Handler
	managedDataBootstrap accessmodule.APIGenBootstrapAuthorizer
	desktopDiscovery     http.Handler
}

type persistenceInputs struct {
	agentSettings    agentmodule.Settings
	adminDatabase    *sql.DB
	servingStateRepo servingStateRepository
	accessRepo       access.Repository
	product          *adminmodule.ProductService
	productStatus    adminmodule.ProductStatus
}

type workflowInputs struct {
	managedDataValidation   refreshmodule.CandidateValidationHook
	managedDataResolver     runtimehostmodule.ManagedDataResolver
	refreshPipelineClock    refreshmodule.Clock
	refreshMaterializer     refreshrun.Materializer
	enableRefreshDispatcher bool
	agent                   *agentmodule.Service
	agentConfig             agentmodule.ModelConfig
	reloader                runtimeReloader
	deploymentConfig        deploymentmodule.Config
}

type storageInputs struct {
	instanceID          string
	duckLakeCatalogPath string
	duckLakeDataPath    string
	jobLeaseTimeout     time.Duration
	publicURL           string
}

func newCompositionSurfaces(
	metrics QueryMetrics,
	assets staticasset.Resolver,
	telemetry *observability.Telemetry,
	dashboardTelemetry dashboardmodule.Telemetry,
) (*capabilityRoutes, *runtimeServices, *platformServices, *httpPolicy) {
	logger := slog.Default()
	var trace *pagestream.TraceStore
	if !assets.Production() {
		trace = pagestream.NewTraceStore(pagestream.TraceOptions{
			CapacityPerStream: 512,
			MaxStreams:        32,
			IncludePayloads:   true,
		})
	}
	routes := &capabilityRoutes{dashboardTelemetry: dashboardTelemetry}
	runtime := &runtimeServices{
		metrics: metrics, broker: pagestream.NewBroker(pagestream.WithTraceStore(trace)),
		pageStreamTrace: trace,
	}
	platform := &platformServices{
		telemetry: telemetry, logger: logger, assets: assets,
		buildIdentity: buildinfo.Current(),
	}
	policy := &httpPolicy{
		requestBodyLimit: apihttpmiddleware.DefaultRequestBodyLimitConfig(),
		desktopDiscovery: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}),
	}
	return routes, runtime, platform, policy
}

type dataAssemblyInputs struct {
	Database         *sql.DB
	PlatformHealth   platformHealth
	AdminDatabase    *sql.DB
	ServingStateRepo servingStateRepository
	StorageRetention *servingstatemodule.Retention
	AccessRepo       access.Repository
}

type capabilityAssemblyInputs struct {
	ReleaseModule     *releasemodule.Module
	JobModule         *jobsmodule.Module
	AccessModule      *accessmodule.Module
	Agent             *agentmodule.Service
	ManagedDataModule *manageddatamodule.Module
	AnalyticsModule   *analyticsmodule.Module
	Authoring         *dashboardmodule.AuthoringApplication
	DashboardAssets   dashboardmodule.Assets
	Product           *adminmodule.ProductService
	ProductStatus     adminmodule.ProductStatus
	ProjectCatalog    *projectcatalog.Service
	ProjectGraph      projecthttp.GraphReader
}

type workflowAssemblyInputs struct {
	AgentSettings           agentmodule.Settings
	ManagedDataValidation   refreshmodule.CandidateValidationHook
	ManagedDataResolver     runtimehostmodule.ManagedDataResolver
	AgentConfig             agentmodule.ModelConfig
	Auth                    *accessmodule.Auth
	Reloader                runtimeReloader
	Workload                workloadControl
	DeploymentConfig        deploymentmodule.Config
	RefreshPipelineClock    refreshmodule.Clock
	RefreshMaterializer     refreshrun.Materializer
	EnableRefreshDispatcher bool
	QueryAudit              *analyticsmodule.QueryAuditSurface
}

type runtimeAssemblyInputs struct {
	RuntimeHost *runtimehostmodule.Module
	// DeliveryTargetReader is the durable target-owned active-generation
	// pointer. Sealed production serving must consult it before the legacy
	// serving-state scope table when deciding whether bootstrap is still open.
	DeliveryTargetReader    deliveryTargetReader
	ProjectID               projectgraph.ResourceID
	ProjectIDResolver       func(context.Context) (projectgraph.ResourceID, error)
	ServingSnapshotResolver func(context.Context) (string, error)
	InstanceID              string
	DuckDBDir               string
	DuckLakeCatalogPath     string
	DuckLakeDataPath        string
	DefaultEnvironment      string
	SCIMBearerToken         string
	MetricsBearerToken      string
	AllowedHosts            []string
	Assets                  staticasset.Resolver
	RequireActiveDeployment bool
	SealedServing           bool
	// DeliveryStartup is the production-only control-plane admission and
	// migration check. It keeps a fresh target administrable while readiness
	// fails closed until an operator admits a physical pool and repairs any
	// legacy serving rows.
	DeliveryStartup func(context.Context) error
}

type httpAssemblyInputs struct {
	RateLimits       apihttpmiddleware.RateLimitConfig
	SecurityHeaders  apihttpmiddleware.SecurityHeadersConfig
	RequestBodyLimit apihttpmiddleware.RequestBodyLimitConfig
	RequestLogging   bool
	Logger           *slog.Logger
	JobLeaseTimeout  time.Duration
	ManagedDataTus   http.Handler
	MCPOAuth         MCPOAuthConfig
	PublicURL        string
	DesktopDiscovery desktopdiscovery.Config
}

type MCPOAuthConfig struct {
	PublicURL string
	IssuerURL string
}

type platformHealth interface {
	Ping(context.Context) error
}

type workloadControl interface {
	workloadmodule.Admitter
	Stats() workloadmodule.Stats
	SetObserver(workloadmodule.Observer)
	Close()
}

func buildApplicationSurfaces(
	ctx context.Context,
	metrics QueryMetrics,
	data dataAssemblyInputs,
	capabilities capabilityAssemblyInputs,
	workflow workflowAssemblyInputs,
	runtimeConfig runtimeAssemblyInputs,
	httpConfig httpAssemblyInputs,
) (*capabilityRoutes, *runtimeServices, *platformServices, *httpPolicy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	telemetry := observability.New()
	dashboardTelemetry := dashboardmodule.NewTelemetry(telemetry.Registry())
	if capabilities.AnalyticsModule != nil {
		telemetry.Register(capabilities.AnalyticsModule.Collector())
	}
	controller := workflow.Workload
	ownsController := false
	workloadTelemetry := workloadmodule.NewTelemetryObserver(telemetry.Registry())
	if controller == nil {
		var err error
		controller, err = workloadmodule.Build(ctx, workloadmodule.Config{
			Policy: workloadmodule.DefaultConfig(), Observer: workloadTelemetry,
		})
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("build workload module: %w", err)
		}
		ownsController = true
	} else {
		controller.SetObserver(workloadTelemetry)
	}
	fail := func(err error) (*capabilityRoutes, *runtimeServices, *platformServices, *httpPolicy, error) {
		if ownsController && controller != nil {
			controller.Close()
		}
		return nil, nil, nil, nil, err
	}
	if metrics != nil {
		metrics = dashboardmodule.WithAdmission(metrics, controller)
	}
	var authorizationSnapshot func(context.Context) (accesssnapshot.AuthorizationSnapshot, error)
	if runtimeConfig.RuntimeHost != nil {
		authorizationSnapshot = func(ctx context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
			lease, err := runtimeConfig.RuntimeHost.Acquire(ctx)
			if err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, err
			}
			defer lease.Release()
			authorizedLease, ok := lease.(interface {
				AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
			})
			if !ok {
				return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("active runtime lease does not expose authorization snapshot")
			}
			snapshot := authorizedLease.AuthorizationSnapshot()
			if err := snapshot.ValidateBound(); err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, err
			}
			return snapshot, nil
		}
	}
	canonicalAuditRecorder, _ := data.AccessRepo.(access.CanonicalAuditRecorder)
	if metrics != nil && authorizationSnapshot != nil && capabilities.AccessModule != nil {
		metrics = dashboardmodule.WithQueryAuthorization(metrics, dashboardmodule.QueryAuthorizationConfig{
			SnapshotFromContext: authorizationSnapshot,
			SubjectsFromContext: capabilities.AccessModule.AuthorizationSubjects,
			PrincipalFromContext: func(ctx context.Context) (dashboardmodule.QueryPrincipal, bool) {
				principal, ok := accessmodule.PrincipalFromContext(ctx)
				return dashboardmodule.QueryPrincipal{ID: principal.ID, DevBypass: principal.DevBypass || workflow.Auth == nil}, ok
			},
			CredentialFromContext: accessmodule.APICredentialFromContext,
			AuditRecorder:         canonicalAuditRecorder,
		})
	}
	var queryAuditProvider adminmodule.QueryAuditReaderProvider
	var queryAuditRecorder dashboardmodule.QueryAuditRecorder
	if workflow.QueryAudit != nil {
		queryAuditProvider = adminmodule.QueryAuditReaderProvider(workflow.QueryAudit.Provider())
		queryAuditRecorder = workflow.QueryAudit.Recorder()
	}
	if capabilities.AnalyticsModule != nil {
		if capabilities.AnalyticsModule.QueryAuditReader() != nil {
			queryAuditProvider = adminmodule.QueryAuditReaderProvider(capabilities.AnalyticsModule.QueryAuditProvider())
		}
		if capabilities.AnalyticsModule.QueryAuditRecorder() != nil {
			queryAuditRecorder = capabilities.AnalyticsModule.QueryAuditRecorder()
		}
	}
	if metrics != nil && queryAuditRecorder != nil {
		metrics = dashboardmodule.WithQueryAudit(metrics, queryAuditRecorder, func(ctx context.Context) (string, bool) {
			principal, ok := accessmodule.PrincipalFromContext(ctx)
			return principal.ID, ok
		})
	}
	servingStateRepo := data.ServingStateRepo
	routes, runtime, platform, policy := newCompositionSurfaces(metrics, runtimeConfig.Assets, telemetry, dashboardTelemetry)
	runtime.runtimeHostModule = runtimeConfig.RuntimeHost
	platform.requireActiveDeployment = runtimeConfig.RequireActiveDeployment
	persistence := persistenceInputs{}
	moduleWorkflow := workflowInputs{}
	storage := storageInputs{}
	moduleWorkflow.refreshPipelineClock = workflow.RefreshPipelineClock
	moduleWorkflow.refreshMaterializer = workflow.RefreshMaterializer
	moduleWorkflow.enableRefreshDispatcher = workflow.EnableRefreshDispatcher
	runtime.queryAuditProvider = queryAuditProvider
	runtime.candidateMetrics = func(provider runtimehostmodule.Provider, projectID projectgraph.ResourceID) QueryMetrics {
		if provider == nil || projectID == "" {
			return nil
		}
		var candidate QueryMetrics = dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{Provider: provider, ProjectID: projectID})
		candidate = dashboardmodule.WithAdmission(candidate, controller)
		if authorizationSnapshot != nil && capabilities.AccessModule != nil {
			candidate = dashboardmodule.WithQueryAuthorization(candidate, dashboardmodule.QueryAuthorizationConfig{
				SnapshotFromContext: authorizationSnapshot,
				SubjectsFromContext: capabilities.AccessModule.AuthorizationSubjects,
				PrincipalFromContext: func(ctx context.Context) (dashboardmodule.QueryPrincipal, bool) {
					principal, ok := accessmodule.PrincipalFromContext(ctx)
					return dashboardmodule.QueryPrincipal{ID: principal.ID, DevBypass: principal.DevBypass || workflow.Auth == nil}, ok
				},
				CredentialFromContext: accessmodule.APICredentialFromContext,
				AuditRecorder:         canonicalAuditRecorder,
			})
		}
		if queryAuditRecorder != nil {
			candidate = dashboardmodule.WithQueryAudit(candidate, queryAuditRecorder, func(ctx context.Context) (string, bool) {
				principal, ok := accessmodule.PrincipalFromContext(ctx)
				return principal.ID, ok
			})
		}
		return candidate
	}
	if moduleWorkflow.refreshPipelineClock == nil {
		moduleWorkflow.refreshPipelineClock = refreshmodule.NewRealClock()
	}
	runtime.workloads = controller
	runtime.projectID = runtimeConfig.ProjectID
	runtime.projectIDResolver = runtimeConfig.ProjectIDResolver
	runtime.persistenceConfigured = data.Database != nil
	runtime.platformHealth = data.PlatformHealth
	persistence.agentSettings = workflow.AgentSettings
	persistence.adminDatabase = data.AdminDatabase
	persistence.product = capabilities.Product
	routes.product = capabilities.Product
	persistence.productStatus = capabilities.ProductStatus
	if data.Database != nil {
		platform.jobModule = capabilities.JobModule
		if platform.jobModule == nil {
			var err error
			platform.jobModule, err = jobsmodule.Build(ctx, jobsmodule.Config{
				Database: data.Database, Admission: workloadmodule.JobAdmitter(runtime.workloads),
				LeaseTimeout: httpConfig.JobLeaseTimeout, Logger: httpConfig.Logger,
			})
			if err != nil {
				return fail(fmt.Errorf("build platform jobs module: %w", err))
			}
		}
		platform.asyncJobs = platform.jobModule
		if err := configureAPIProtocol(routes, runtime, platform, policy, ctx, data.Database); err != nil {
			return fail(fmt.Errorf("build API protocol: %w", err))
		}
	}
	if platform.apiProtocol == nil {
		if err := configureAPIProtocol(routes, runtime, platform, policy, ctx, nil); err != nil {
			return fail(fmt.Errorf("build API protocol: %w", err))
		}
	}
	persistence.servingStateRepo = servingStateRepo
	retentionStates, _ := servingStateRepo.(servingstatemodule.RetentionRepository)
	runtime.storageRetention = data.StorageRetention
	if runtime.storageRetention == nil && !runtimeConfig.SealedServing {
		runtime.storageRetention = servingstatemodule.NewRetention(servingstatemodule.RetentionConfig{
			States: retentionStates, Snapshots: capabilities.AnalyticsModule.RetentionSnapshots(),
			Admission: controller, Environment: runtimeConfig.DefaultEnvironment,
			CatalogPath: runtimeConfig.DuckLakeCatalogPath, DataPath: runtimeConfig.DuckLakeDataPath,
			ProtectedSnapshots: func() []int64 {
				if provider, ok := workflow.Reloader.(interface{ LeasedSnapshots() []int64 }); ok {
					return provider.LeasedSnapshots()
				}
				return nil
			},
		})
	}
	moduleWorkflow.managedDataValidation = workflow.ManagedDataValidation
	moduleWorkflow.managedDataResolver = workflow.ManagedDataResolver
	runtime.analyticsModule = capabilities.AnalyticsModule
	routes.dashboardAssets = capabilities.DashboardAssets
	routes.dashboardAuthoring = capabilities.Authoring
	routes.releaseModule = capabilities.ReleaseModule
	persistence.accessRepo = data.AccessRepo
	moduleWorkflow.agent = capabilities.Agent
	moduleWorkflow.agentConfig = workflow.AgentConfig
	platform.auth = workflow.Auth
	routes.accessModule = capabilities.AccessModule
	moduleWorkflow.reloader = workflow.Reloader
	storage.duckLakeCatalogPath = runtimeConfig.DuckLakeCatalogPath
	storage.duckLakeDataPath = runtimeConfig.DuckLakeDataPath
	storage.instanceID = runtimeConfig.InstanceID
	policy.defaultEnvironment = string(servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(runtimeConfig.DefaultEnvironment)))
	storage.publicURL = strings.TrimSuffix(strings.TrimSpace(httpConfig.PublicURL), "/")
	if strings.TrimSpace(httpConfig.DesktopDiscovery.CanonicalOrigin) != "" {
		discovery, err := desktopdiscovery.NewHandler(httpConfig.DesktopDiscovery)
		if err != nil {
			return fail(fmt.Errorf("configure desktop discovery: %w", err))
		}
		policy.desktopDiscovery = discovery
	}
	policy.scimBearerToken = runtimeConfig.SCIMBearerToken
	policy.metricsBearerToken = runtimeConfig.MetricsBearerToken
	policy.allowedHosts = append([]string(nil), runtimeConfig.AllowedHosts...)
	policy.rateLimits = httpConfig.RateLimits
	policy.securityHeaders = httpConfig.SecurityHeaders
	policy.requestBodyLimit = httpConfig.RequestBodyLimit
	if !policy.requestBodyLimit.Enabled && policy.requestBodyLimit.MaxBytes == 0 {
		policy.requestBodyLimit = apihttpmiddleware.DefaultRequestBodyLimitConfig()
	}
	policy.requestLogging = httpConfig.RequestLogging
	routes.managedDataModule = capabilities.ManagedDataModule
	routes.projectCatalog = capabilities.ProjectCatalog
	var projectDefinitionReader projecthttp.ProjectDefinitionReader
	if runtimeConfig.RuntimeHost != nil {
		projectDefinitionReader = projectmodule.NewActiveProjectDefinitionReader(runtimeConfig.RuntimeHost.Provider())
	}
	routes.projectBrowser = &projecthttp.BrowserHandler{
		Graph: capabilities.ProjectGraph, ProjectDefinitionReader: projectDefinitionReader, QueryExecutor: metrics, Catalog: capabilities.ProjectCatalog,
		ResolveProjectID: runtime.resolveProjectID, Environment: runtimeConfig.DefaultEnvironment, Trace: runtime.pageStreamTrace,
		Layout: func(r *http.Request) webpage.Provider {
			return applicationLayout(routes.accessModule, routes.agentModule, routes.product, platform.assets, r)
		},
		CSRFToken: func(r *http.Request) string { return routes.accessModule.CSRFToken(r) },
		CurrentUser: func(r *http.Request) (projecthttp.Principal, bool) {
			principal, ok := routes.accessModule.CurrentPrincipal(r)
			return projecthttp.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
		},
		Authenticate: routes.accessModule.Authenticate,
	}
	if runtime.runtimeHostModule != nil && routes.accessModule != nil {
		authorizationSnapshot := func(ctx context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
			lease, err := runtime.runtimeHostModule.Acquire(ctx)
			if err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, err
			}
			defer lease.Release()
			authorizedLease, ok := lease.(interface {
				AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
			})
			if !ok {
				return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("active runtime lease does not expose authorization snapshot")
			}
			snapshot := authorizedLease.AuthorizationSnapshot()
			if err := snapshot.ValidateBound(); err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, err
			}
			return snapshot, nil
		}
		snapshotAuthorizeConnection := accessmodule.ConnectionAuthorizerFromSnapshot(authorizationSnapshot, routes.accessModule.AuthorizationSubjects)
		authorizeConnection := bootstrapAwareConnectionAuthorization(snapshotAuthorizeConnection, func(ctx context.Context) (bool, error) {
			return hasActiveBootstrapServingState(ctx, runtime.runtimeHostModule, persistence.servingStateRepo, policy.defaultEnvironment, runtimeConfig.DeliveryTargetReader, runtimeConfig.InstanceID, runtimeConfig.ProjectID.String())
		})
		routes.accessModule.SetCurrentEffectiveCapabilities(func(ctx context.Context, principalID string) ([]access.Capability, error) {
			subjects, err := routes.accessModule.AuthorizationSubjects(ctx, principalID)
			if err != nil {
				return nil, err
			}
			snapshot, err := authorizationSnapshot(ctx)
			if err != nil {
				return nil, err
			}
			return snapshot.EffectiveCapabilities(subjects)
		})
		routes.accessModule.SetCurrentProjectID(runtime.resolveProjectID)
		if routes.managedDataModule != nil {
			routes.managedDataModule.SetAuthorizeConnection(manageddatamodule.ConnectionAuthorizer(authorizeConnection))
		}
		if routes.releaseModule != nil {
			routes.releaseModule.SetAuthorizeConnection(snapshotAuthorizeConnection)
		}
	}
	moduleWorkflow.deploymentConfig = workflow.DeploymentConfig
	policy.managedDataTus = httpConfig.ManagedDataTus
	storage.jobLeaseTimeout = httpConfig.JobLeaseTimeout
	if storage.jobLeaseTimeout <= 0 {
		storage.jobLeaseTimeout = 2 * time.Minute
	}
	if httpConfig.Logger != nil {
		platform.logger = httpConfig.Logger
		if runtime.pageStreamTrace != nil {
			runtime.pageStreamTrace.SetLogger(httpConfig.Logger)
		}
	}
	if err := configureRefreshModule(routes, runtime, platform, policy, ctx, data.Database, persistence, moduleWorkflow, storage); err != nil {
		return fail(err)
	}
	if err := configureModules(routes, runtime, platform, policy, runtimeConfig, ctx, data.Database, persistence, moduleWorkflow, storage); err != nil {
		return fail(err)
	}
	if platform.asyncJobs != nil {
		handlers := make([]jobs.Handler, 0, 4)
		if routes.releaseModule != nil {
			handlers = append(handlers, routes.releaseModule.JobHandlers()...)
		}
		if routes.deploymentModule != nil {
			handlers = append(handlers, routes.deploymentModule.JobHandlers()...)
		}
		if routes.managedDataModule != nil && routes.managedDataModule.HasFinalizeJobs() {
			handlers = append(handlers, routes.managedDataModule.JobHandlers(platform.asyncJobs)...)
		}
		if routes.agentModule != nil {
			handlers = append(handlers, routes.agentModule.JobHandlers(platform.asyncJobs)...)
		}
		if err := platform.jobModule.RegisterHandlers(handlers); err != nil {
			return fail(fmt.Errorf("register async job handlers: %w", err))
		}
	}
	return routes, runtime, platform, policy, nil
}

func configureModules(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, runtimeConfig runtimeAssemblyInputs, ctx context.Context, database *sql.DB, persistence persistenceInputs, moduleWorkflow workflowInputs, storage storageInputs) error {
	if routes == nil || runtime == nil || platform == nil || policy == nil {
		return errors.New("runtime router is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var connectionAdministration analyticsmodule.ConnectionBindingAdministration
	if runtime.analyticsModule != nil {
		administration, err := runtime.analyticsModule.NewConnectionAdministration(
			analyticsmodule.ConnectionAdministrationConfig{
				EnsureScope: func(ctx context.Context, scope analyticsmodule.ConnectionBindingScope) error {
					projectID, err := runtime.resolveProjectID(ctx)
					if err != nil {
						return err
					}
					return validateCanonicalConnectionBindingScope(scope, projectID, runtimeConfig.DefaultEnvironment)
				},
				Authorize: func(
					ctx context.Context,
					principalID string,
					permission analyticsmodule.ConnectionAdministrationPermission,
					binding analyticsmodule.ConnectionTargetBinding,
				) error {
					var capability access.Capability
					switch permission {
					case analyticsmodule.PermissionManageConnectionMetadata:
						capability = access.CapabilityResourceManage
					case analyticsmodule.PermissionTestConnection:
						capability = access.CapabilityResourceUse
					case analyticsmodule.PermissionViewConnectionHealth:
						capability = access.CapabilityResourceRead
					default:
						return analyticsmodule.ErrConnectionBindingUnauthorized
					}
					resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
					if err != nil {
						return err
					}
					allowed, err := authorizeProjectResources(
						ctx, routes.accessModule, runtime.runtimeHostModule, principalID,
						binding.Scope.ProjectID, []access.ResourceRef{resource}, capability,
					)
					if err != nil {
						return err
					}
					if !allowed {
						return analyticsmodule.ErrConnectionBindingUnauthorized
					}
					return nil
				},
				Dependencies: connectionBindingDependenciesWithoutConsumers{},
				Now:          time.Now,
				Audit: connectionRotationAuditRecorder{
					record: accessAuditRecorder(routes.accessModule),
				},
				AdministrationAudit: connectionAdministrationAuditRecorder{
					record: accessAuditRecorder(routes.accessModule),
				},
			},
		)
		if err != nil && !errors.Is(err, analyticsmodule.ErrConnectionAdministrationUnavailable) {
			return err
		}
		connectionAdministration = administration
	}
	analyticsAPI := analyticsmodule.AnalyticsAPIGenConfig{
		QueryAudit: analyticsmodule.QueryAuditAPIGenConfig{
			Reader: runtime.queryAuditProvider,
			ProjectID: func(value string) projectgraph.ResourceID {
				return projectgraph.ResourceID(value)
			},
		},
		Connections: analyticsmodule.ConnectionBindingAPIGenConfig{
			Administration: connectionAdministration,
			Environment:    runtimeConfig.DefaultEnvironment,
			CurrentPrincipal: func(r *http.Request) (string, bool) {
				principal, ok := routes.accessModule.CurrentPrincipal(r)
				return principal.ID, ok
			},
		},
	}
	var apiDispatcher *apiGenDispatcher
	if routes.accessModule == nil {
		var err error
		routes.accessModule, err = accessmodule.Build(ctx, accessmodule.Config{
			Database: database, ExistingAuth: platform.auth,
			InstanceID:       storage.instanceID,
			PublicURL:        storage.publicURL,
			CurrentProjectID: runtime.resolveProjectID,
			Presentation:     webpage.Presentation{ProductName: brand.Name, FaviconPath: brand.FaviconPath},
			Assets:           platform.assets,
		})
		if err != nil {
			return fmt.Errorf("build access module: %w", err)
		}
	}
	if routes.deploymentModule == nil {
		config := moduleWorkflow.deploymentConfig
		config.Logger = platform.logger
		config.InstanceID = storage.instanceID
		config.CanonicalOrigin = storage.publicURL
		config.InstanceEnvironment = policy.defaultEnvironment
		config.CurrentPrincipal = func(r *http.Request) (deploymentmodule.Principal, bool) {
			principal, ok := routes.accessModule.CurrentPrincipal(r)
			return deploymentmodule.Principal{ID: principal.ID}, ok
		}
		config.CandidateAudit = func(ctx context.Context, event deploymentmodule.CandidateEvent) error {
			return recordAccessAudit(ctx, routes.accessModule, access.AuditEventInput{
				PrincipalID: event.PrincipalID,
				Action:      event.Action, ResourceKind: "project", ResourceID: event.ProjectID.String(),
				Capability: access.CapabilityProjectAdmin, Status: string(event.Status), MetadataJSON: event.MetadataJSON,
			})
		}
		config.CandidateSourceAudit = candidateSourceAuditRecorder(routes.accessModule)
		config.CandidateSourceBlobAudit = candidateSourceAuditRecorder(routes.accessModule)
		config.Jobs = deploymentmodule.JobConfig{
			Reconcile: func(ctx context.Context) error {
				if routes.refreshModule == nil {
					return nil
				}
				return routes.refreshModule.Reconcile(ctx)
			},
			Events: platform.asyncJobs,
			Logger: platform.logger,
		}
		config.API = deploymentmodule.APIConfig{Releases: routes.releaseModule.DeploymentLinkage(), Jobs: platform.asyncJobs, Workflow: platform.jobModule, Committer: platform.jobModule}
		config.PublicationAuthorization = deploymentmodule.PublicationAuthorizationConfig{
			States: persistence.servingStateRepo, AuthorizeResource: func(ctx context.Context, actor string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
				return authorizeProjectResources(ctx, routes.accessModule, runtime.runtimeHostModule, actor, projectID, []access.ResourceRef{resource}, capability)
			},
			Bypass: func(actor string) bool {
				return (platform.auth == nil || platform.auth.DevBypass()) && actor == accessmodule.LocalDeveloperPrincipal().ID
			},
		}
		var err error
		routes.deploymentModule, err = deploymentmodule.Build(ctx, config)
		if err != nil {
			return fmt.Errorf("build deployment module: %w", err)
		}
	}
	if routes.dashboardModule == nil {
		agentUICommands := routes.agentModule.UICommandBindings()
		var err error
		routes.dashboardModule, err = dashboardmodule.Build(ctx, dashboardmodule.Config{
			Database:    database,
			Authoring:   routes.dashboardAuthoring,
			RecordAudit: accessAuditRecorder(routes.accessModule),
			HTTP: dashboardmodule.HTTPConfig{
				Metrics:          runtime.metrics,
				ProjectID:        runtime.projectID,
				ResolveProjectID: runtime.resolveProjectID,
				Admission:        workloadController(&runtime.workloads), Broker: runtime.broker, Logger: platform.logger,
				Telemetry: routes.dashboardTelemetry,
				CurrentPrincipalID: func(r *http.Request) string {
					principal, ok := accessmodule.PrincipalFromContext(r.Context())
					if !ok {
						return ""
					}
					return principal.ID
				},
				AuthorizeListResource: func(ctx context.Context, principalID string, resource access.ResourceRef, capability access.Capability) (bool, error) {
					projectID, err := runtime.resolveProjectID(ctx)
					if err != nil {
						return false, err
					}
					return authorizeProjectResources(ctx, routes.accessModule, runtime.runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
				},
				CurrentUsagePrincipal: func(r *http.Request) (string, bool) {
					principal, ok := routes.accessModule.CurrentPrincipal(r)
					if !ok || !principal.IsHuman() {
						return "", false
					}
					return principal.ID, true
				},
				CSRFToken: routes.accessModule.CSRFToken,
				Layout: func(r *http.Request) webpage.Provider {
					return applicationLayout(routes.accessModule, routes.agentModule, routes.product, platform.assets, r)
				},
				Environment: func(r *http.Request) string {
					return string(requestServingEnvironment(policy.defaultEnvironment, r))
				},
				DataRefreshedAt: func(ctx context.Context, projectID, environment, modelID string) string {
					if routes.refreshModule == nil {
						return ""
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, projectID, environment, modelID)
					if err != nil || !ok {
						return ""
					}
					return version.RefreshedAt.Format(time.RFC3339)
				},
				QueryFreshness: func(ctx context.Context, projectID, modelID, servingSnapshot string) (dashboardmodule.QueryFreshness, bool) {
					if routes.refreshModule == nil {
						return dashboardmodule.QueryFreshness{}, false
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, projectID, policy.defaultEnvironment, modelID)
					if err != nil || !ok {
						return dashboardmodule.QueryFreshness{}, false
					}
					status := "stale"
					if version.ServingStateID == servingSnapshot {
						status = "current"
					}
					return dashboardmodule.QueryFreshness{
						LastSuccessfulRefreshAt: version.RefreshedAt.UTC().Format(time.RFC3339),
						SnapshotID:              strconv.FormatInt(version.SnapshotID, 10),
						ServingStateID:          version.ServingStateID,
						Source:                  version.Source,
						Status:                  status,
					}, true
				},
				AgentBootstrap: func(r *http.Request, _ string) dashboardmodule.AgentBootstrap {
					if routes.agentModule == nil {
						return dashboardmodule.AgentBootstrap{}
					}
					return dashboardAgentBootstrap(routes.agentModule.DashboardBootstrap(r))
				},
				AgentCommands: dashboardmodule.AgentCommandBindings{
					CreateConversation: agentUICommands.CreateConversation,
					CreateRun:          agentUICommands.CreateRun,
				},
				Presentation: dashboardmodule.Presentation{ProductName: brand.Name, FaviconPath: brand.FaviconPath},
				Assets:       platform.assets,
			},
			Semantic: dashboardmodule.SemanticConfig{
				Metrics:          runtime.metrics,
				ResolveProjectID: runtime.resolveProjectID,
				CurrentPrincipalID: func(r *http.Request) string {
					principal, ok := accessmodule.PrincipalFromContext(r.Context())
					if !ok {
						return ""
					}
					return principal.ID
				},
				AuthorizeListResource: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
					return authorizeProjectResources(ctx, routes.accessModule, runtime.runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
				},
				QueryFreshness: func(ctx context.Context, projectID, modelID, servingSnapshot string) (dashboardmodule.QueryFreshness, bool) {
					if routes.refreshModule == nil {
						return dashboardmodule.QueryFreshness{}, false
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, projectID, policy.defaultEnvironment, modelID)
					if err != nil || !ok {
						return dashboardmodule.QueryFreshness{}, false
					}
					status := "stale"
					if version.ServingStateID == servingSnapshot {
						status = "current"
					}
					return dashboardmodule.QueryFreshness{
						LastSuccessfulRefreshAt: version.RefreshedAt.UTC().Format(time.RFC3339),
						SnapshotID:              strconv.FormatInt(version.SnapshotID, 10),
						ServingStateID:          version.ServingStateID,
						Source:                  version.Source,
						Status:                  status,
					}, true
				},
			},
			PublicTelemetry: dashboardmodule.PublicTelemetry{
				DocumentObserved: routes.dashboardTelemetry.PublicDocumentObserved,
				StreamStarted:    routes.dashboardTelemetry.PublicStreamStarted,
				CommandObserved:  routes.dashboardTelemetry.PublicCommandObserved,
			},
			Logger:    platform.logger,
			Trace:     runtime.pageStreamTrace,
			PublicURL: storage.publicURL,
			CurrentActor: func(r *http.Request) string {
				principal, ok := accessmodule.PrincipalFromContext(r.Context())
				if !ok {
					return ""
				}
				return principal.ID
			},
			RuntimeMetrics:  runtime.metrics,
			ServingSnapshot: runtimeConfig.ServingSnapshotResolver,
		})
		if err != nil {
			return fmt.Errorf("build dashboard module: %w", err)
		}
	}
	if routes.agentModule == nil {
		documentation, err := buildAgentDocumentation()
		if err != nil {
			return err
		}
		routes.agentModule, err = agentmodule.Build(ctx, agentmodule.Config{
			Database: database, Model: moduleWorkflow.agentConfig,
			Service: moduleWorkflow.agent, Jobs: platform.asyncJobs,
			ProductName:        brand.Name,
			BuildVersion:       platform.buildIdentity.Version,
			APIGenOperations:   agentAPIGenOperations(),
			DashboardAuthoring: routes.dashboardAuthoring,
			ResolveResource: func(ctx context.Context, scope agentmodule.Scope, id projectgraph.ResourceID, kind projectgraph.Kind, capability access.Capability) (projectgraph.ResourceID, error) {
				if routes.projectCatalog == nil {
					return "", projectcatalog.ErrUnavailable
				}
				resolved, err := routes.projectCatalog.Resolve(ctx, scope.PrincipalID, projectcatalog.Ref{ID: id, Kind: kind}, capability, scope.DevAuthBypass)
				if err != nil {
					return "", err
				}
				return resolved.Ref.ID, nil
			},
			RunWorkloadClass: string(workloadmodule.BackgroundClass), ProjectID: runtime.projectID,
			ResolveProjectID: runtime.resolveProjectID,
			DashboardMetrics: func(projectID string) (QueryMetrics, bool) {
				requested, err := projectgraph.NewResourceID(projectID)
				active, activeErr := runtime.resolveProjectID(context.Background())
				if err != nil || activeErr != nil || requested != active || runtime.metrics == nil {
					return nil, false
				}
				return runtime.metrics, true
			},
			RecordAudit:   accessAuditRecorder(routes.accessModule),
			Documentation: documentation,
			Catalog:       agentmodule.BuildCatalog(agentmodule.CatalogConfig{ProjectCatalog: routes.projectCatalog}),
			QueryMetadata: func(ctx context.Context, projectID, modelID string) agentmodule.VisualQueryMetadata {
				if runtime.runtimeHostModule == nil {
					return agentmodule.VisualQueryMetadata{}
				}
				lease, err := runtime.runtimeHostModule.Acquire(ctx)
				if err != nil || lease == nil {
					return agentmodule.VisualQueryMetadata{}
				}
				identity := lease.Identity()
				metadata := agentmodule.VisualQueryMetadata{ServingSnapshot: strings.TrimSpace(identity.GenerationID)}
				lease.Release()
				if metadata.ServingSnapshot == "" || identity.ProjectID.String() != strings.TrimSpace(projectID) {
					return agentmodule.VisualQueryMetadata{}
				}
				if routes.refreshModule != nil {
					version, ok, err := routes.refreshModule.DataVersion(ctx, projectID, policy.defaultEnvironment, modelID)
					if err == nil && ok {
						status := "stale"
						if strings.TrimSpace(version.ServingStateID) == metadata.ServingSnapshot {
							status = "current"
						}
						metadata.Freshness = &agentmodule.QueryFreshness{
							LastSuccessfulRefreshAt: version.RefreshedAt.UTC().Format(time.RFC3339),
							SnapshotID:              strconv.FormatInt(version.SnapshotID, 10),
							ServingStateID:          version.ServingStateID,
							Source:                  version.Source,
							Status:                  status,
						}
					}
				}
				return metadata
			},
			EnableSystemPrompt: runtime.persistenceConfigured,
			Logger:             platform.logger,
			MCPProtect:         routes.accessModule.ProtectMCP,
			MCPScope: func(r *http.Request) (agentmodule.Scope, bool) {
				identity, ok := routes.accessModule.MCPIdentity(r)
				if !ok {
					return agentmodule.Scope{}, false
				}
				projectID, err := runtime.resolveProjectID(r.Context())
				if err != nil {
					return agentmodule.Scope{}, false
				}
				scope := agentmodule.Scope{
					ProjectID: projectID.String(), PrincipalID: identity.PrincipalID, DevAuthBypass: identity.DevBypass,
				}
				if identity.Credential.Authoring != nil {
					scope.Credential.ProjectID = identity.Credential.Authoring.Scope.ProjectID.String()
					scope.Credential.Restricted = true
					for _, capability := range identity.Credential.Authoring.Scope.Capabilities {
						scope.Credential.Capabilities = append(scope.Credential.Capabilities, string(capability))
					}
				} else if identity.Credential.Token.ID != "" {
					scope.Credential.Restricted = true
					if identity.Credential.Token.Capabilities != nil {
						scope.Credential.Capabilities = make([]string, 0, len(identity.Credential.Token.Capabilities))
						for _, capability := range identity.Credential.Token.Capabilities {
							scope.Credential.Capabilities = append(scope.Credential.Capabilities, string(capability))
						}
					}
				}
				return scope, true
			},
			DispatchAPIGen: func(scope agentmodule.Scope, operationID string, writer http.ResponseWriter, request *http.Request) bool {
				principal := accessmodule.Principal{ID: scope.PrincipalID, DevBypass: scope.DevAuthBypass}
				if platform.auth == nil && strings.TrimSpace(principal.ID) == "" {
					principal = accessmodule.LocalDeveloperPrincipal()
				}
				ctx := accessmodule.WithPrincipal(request.Context(), principal)
				request = request.WithContext(ctx)
				if apiDispatcher == nil {
					return false
				}
				if routes.accessModule != nil && routes.accessModule.DispatchAPIGenOperation(operationID, writer, request) {
					return true
				}
				if routes.agentModule != nil && routes.agentModule.DispatchAPIGenOperation(operationID, writer, request, platform.logger) {
					return true
				}
				if analyticsmodule.DispatchAPIGenOperation(analyticsAPI, operationID, platform.logger, writer, request) {
					return true
				}
				if routes.releaseModule != nil && projecthttp.DispatchAPIGenOperation(operationID, routes.releaseModule, platform.logger, writer, request) {
					return true
				}
				if routes.refreshModule != nil && routes.refreshModule.DispatchAPIGenOperation(operationID, platform.logger, writer, request) {
					return true
				}
				if routes.deploymentModule != nil && routes.deploymentModule.DispatchAPIGenOperation(operationID, platform.logger, writer, request) {
					return true
				}
				if routes.releaseModule != nil && routes.releaseModule.DispatchAPIGenOperation(operationID, platform.logger, writer, request) {
					return true
				}
				if routes.managedDataModule != nil && routes.managedDataModule.DispatchAPIGenOperation(operationID, routes.releaseModule, platform.logger, writer, request) {
					return true
				}
				if routes.dashboardModule != nil && routes.dashboardModule.DispatchAPIGenOperation(operationID, platform.logger, writer, request) {
					return true
				}
				return apigenapi.DispatchAPIGenOperation(operationID, apiDispatcher, apiprotocol.TransportErrorResponder{Logger: platform.logger}, writer, request)
			},
			QueryContext: func(ctx context.Context, scope agentmodule.Scope) context.Context {
				principal := accessmodule.Principal{ID: scope.PrincipalID, DevBypass: scope.DevAuthBypass}
				if platform.auth == nil && strings.TrimSpace(principal.ID) == "" {
					principal = accessmodule.LocalDeveloperPrincipal()
				}
				ctx = accessmodule.WithPrincipal(ctx, principal)
				if projectID, err := projectgraph.NewResourceID(scope.ProjectID); err == nil {
					ctx = analyticsmodule.WithAgentQueryMetadata(ctx, projectID, scope.PrincipalID)
				}
				return ctx
			},
			HTTP: agentmodule.HTTPConfig{
				Settings: persistence.agentSettings, Broker: runtime.broker,
				PlatformAdmin: func(ctx context.Context, principalID string) (bool, error) {
					capabilities, err := routes.accessModule.CurrentEffectiveCapabilities(ctx, principalID)
					if err != nil {
						return false, err
					}
					for _, capability := range capabilities {
						if capability == access.CapabilityProjectAdmin {
							return true, nil
						}
					}
					return false, nil
				},
				CSRFToken:        routes.accessModule.CSRFToken,
				CurrentRoleLabel: routes.accessModule.CurrentRoleLabel,
				Layout: func(r *http.Request) webpage.Provider {
					return applicationLayout(routes.accessModule, routes.agentModule, routes.product, platform.assets, r)
				},
				CurrentPrincipal: func(r *http.Request) (agentmodule.Principal, bool) {
					if platform.auth == nil {
						return agentmodule.Principal{}, false
					}
					principal, ok := platform.auth.Principal(r)
					return agentmodule.Principal{ID: principal.ID, DevAuthBypass: principal.DevBypass}, ok
				},
				CurrentCredential: func(r *http.Request) (access.APICredential, bool) {
					if platform.auth == nil {
						return access.APICredential{}, false
					}
					return platform.auth.APICredential(r)
				},
			},
		})
		if err != nil {
			return fmt.Errorf("build agent module: %w", err)
		}
	}
	if routes.refreshModule == nil {
		if err := configureRefreshModule(routes, runtime, platform, policy, ctx, nil, persistence, moduleWorkflow, storage); err != nil {
			return err
		}
	}
	if routes.adminModule == nil {
		var accessReader adminmodule.AccessReader
		if reader := routes.accessModule.AdminReader(); reader != nil {
			accessReader = reader
		}
		currentAdminPrincipal := func(r *http.Request) (adminmodule.Principal, bool) {
			principal, ok := routes.accessModule.CurrentPrincipal(r)
			return adminmodule.Principal{
				ID: principal.ID, Email: principal.Email, DisplayName: principal.DisplayName, DevBypass: principal.DevBypass,
			}, ok
		}
		settingsAccess := routes.accessModule.SettingsAdministration()
		localPasswordEnabled := routes.accessModule.Auth() != nil && routes.accessModule.Auth().LocalAuthEnabled()
		productCommands, commandErr := apigencommand.NewExecutor(apiaggregate.GetAPIGenCommandRuntimeContract, nil)
		if commandErr != nil {
			return fmt.Errorf("build product command executor: %w", commandErr)
		}
		var err error
		routes.adminModule, err = adminmodule.Build(ctx, adminmodule.Config{
			Access: accessReader,
			AgentDetails: func(ctx context.Context) (agentmodule.AdminAgentResponse, error) {
				return routes.agentModule.HTTP().AdminDetails(ctx)
			},
			QueryAuditReader: runtime.queryAuditProvider,
			CSRFToken:        routes.accessModule.CSRFToken,
			CurrentPrincipal: currentAdminPrincipal,
			CurrentCredential: func(r *http.Request) (access.APICredential, bool) {
				if platform.auth == nil {
					return access.APICredential{}, false
				}
				return platform.auth.APICredential(r)
			},
			CurrentEffectiveCapabilities: routes.accessModule.CurrentEffectiveCapabilities,
			Publications:                 routes.dashboardModule,
			AgentConfigCommand:           routes.agentModule.UICommandBindings().UpdateConfig,
			PublicationCommands:          routes.dashboardModule.PublicationCommandBindings(),
			AuthConfigured:               platform.auth != nil,
			LocalPasswordEnabled:         localPasswordEnabled,
			AccessConfigured:             accessReader != nil,
			Storage: adminmodule.StorageConfig{
				CatalogPath: storage.duckLakeCatalogPath, DataPath: storage.duckLakeDataPath,
				Environment: policy.defaultEnvironment, ControlPlane: persistence.adminDatabase,
				Analytics: runtime.analyticsModule.AdminResources(), Admitter: workloadController(&runtime.workloads),
			},
			Layout: func(r *http.Request) webpage.Provider {
				return applicationLayout(routes.accessModule, routes.agentModule, routes.product, platform.assets, r)
			},
			EnsureClientID: func(w http.ResponseWriter, r *http.Request) {
				_ = pagestream.EnsureClientID(w, r)
			},
			Broker:  runtime.broker,
			Product: persistence.product, ProductCommands: productCommands, ProductCommandFailure: writeProductCommandFailure, ProductStatus: persistence.productStatus,
			ProductUICommands: productUICommandContract(),
			SettingsAccess:    settingsAccess,
			PersonalAvatar:    routes.accessModule.PersonalAvatar(),
			AuthoringSessions: routes.accessModule.AuthoringSessions(),
			CurrentSession:    routes.accessModule.CurrentSessionID,
		})
		if err != nil {
			return fmt.Errorf("build admin module: %w", err)
		}
	}
	if routes.managedDataModule == nil {
		var err error
		routes.managedDataModule, err = manageddatamodule.Build(ctx, manageddatamodule.Config{
			Disabled:    true,
			Environment: policy.defaultEnvironment, Jobs: platform.asyncJobs,
			CurrentPrincipal: func(r *http.Request) (manageddatamodule.Principal, bool) {
				if platform.auth == nil {
					return manageddatamodule.Principal{}, false
				}
				principal, ok := platform.auth.Principal(r)
				return manageddatamodule.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
			},
		})
		if err != nil {
			return fmt.Errorf("build managed data module: %w", err)
		}
	}
	apiDispatcher = &apiGenDispatcher{
		managedDataModule:  routes.managedDataModule,
		productAPI:         routes.adminModule,
		arrowQueries:       supportsNativeArrow(runtime.metrics),
		defaultEnvironment: policy.defaultEnvironment, managedDataTus: policy.managedDataTus,
		instanceID: storage.instanceID, canonicalOrigin: storage.publicURL, buildIdentity: platform.buildIdentity,
	}
	apiGenAuthorizer, err := routes.accessModule.APIGenAuthorizer(runtime.runtimeHostModule, accessAPIGenOperationContracts(), accessmodule.APIGenResourceResolvers{
		Dashboard: func(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
			id, err := projectgraph.NewResourceID(chi.URLParam(r, "dashboard"))
			if err != nil {
				return nil
			}
			resource, err := access.NewResourceRef(id, projectgraph.KindDashboard)
			if err != nil {
				return nil
			}
			return []access.ResourceRef{resource}
		},
		SemanticModel: func(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
			id, err := projectgraph.NewResourceID(chi.URLParam(r, "model"))
			if err != nil {
				return nil
			}
			resource, err := access.NewResourceRef(id, projectgraph.KindSemanticModel)
			if err != nil {
				return nil
			}
			return []access.ResourceRef{resource}
		},
		Connection: func(r *http.Request, _ projectgraph.ResourceID) []access.ResourceRef {
			id, err := projectgraph.NewResourceID(chi.URLParam(r, "connection"))
			if err != nil {
				return nil
			}
			resource, err := access.NewResourceRef(id, projectgraph.KindConnection)
			if err != nil {
				return nil
			}
			return []access.ResourceRef{resource}
		},
		Project: func(r *http.Request, active projectgraph.ResourceID) []access.ResourceRef {
			requested, err := projectgraph.NewResourceID(chi.URLParam(r, "project"))
			if err != nil || requested != active {
				return nil
			}
			resource, err := access.NewResourceRef(active, projectgraph.KindProject)
			if err != nil {
				return nil
			}
			return []access.ResourceRef{resource}
		},
		Delivery: func(ctx context.Context, r *http.Request, operationID, objectID string, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
			principal, ok := routes.accessModule.CurrentPrincipal(r)
			if !ok {
				return false, nil
			}
			lease, err := runtime.runtimeHostModule.Acquire(ctx)
			if err != nil {
				return false, err
			}
			if lease == nil {
				return false, fmt.Errorf("runtime host returned a nil lease")
			}
			defer lease.Release()
			if lease.Identity().ProjectID != projectID {
				return false, fmt.Errorf("runtime project %q does not match requested project %q", lease.Identity().ProjectID, projectID)
			}
			authorizedLease, ok := lease.(interface {
				AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
			})
			if !ok {
				return false, fmt.Errorf("active runtime lease does not expose authorization snapshot")
			}
			subjects, err := routes.accessModule.AuthorizationSubjects(ctx, principal.ID)
			if err != nil {
				return false, err
			}
			snapshot := authorizedLease.AuthorizationSnapshot()
			if snapshot.Identity() != lease.Identity() {
				return false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
			}
			reader := moduleWorkflow.deploymentConfig.DeliveryReader
			if reader == nil {
				return false, fmt.Errorf("delivery authorization reader is unavailable")
			}
			if operationID == "createDeliveryPlan" || operationID == "getDeliveryOperatorSnapshot" {
				return deliveryRoleAllows(snapshot, subjects, capability), nil
			}
			plan, err := deliveryAuthorizationPlan(ctx, reader, operationID, objectID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return false, nil
				}
				return false, err
			}
			resources, err := deliveryAuthorizationResources(plan)
			if err != nil {
				return false, err
			}
			if len(resources) == 0 {
				// Unknown/new resources require an explicit target-owned role;
				// a grant on an unrelated graph object must never widen scope.
				return deliveryRoleAllows(snapshot, subjects, capability), nil
			}
			return deliverySnapshotAllows(snapshot, subjects, resources, capability)
		},
	})
	if err != nil {
		return fmt.Errorf("build APIGen authorizer: %w", err)
	}
	if bootstrapPolicies := moduleWorkflow.deploymentConfig.BootstrapPolicies; bootstrapPolicies != nil {
		claimReader, ok := bootstrapPolicies.(deploymentmodule.ProjectClaimReader)
		if !ok {
			return fmt.Errorf("bootstrap policy store does not expose the durable project claim")
		}
		bootstrapAuthorizer := func(ctx context.Context, _ *http.Request, operationID string, projectID projectgraph.ResourceID, _ access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
			return bootstrapAPIGenDecision(ctx, runtime.runtimeHostModule, persistence.servingStateRepo, claimReader, policy.defaultEnvironment, operationID, projectID, runtimeConfig.DeliveryTargetReader, runtimeConfig.InstanceID)
		}
		apiGenAuthorizer.SetBootstrapAuthorizer(bootstrapAuthorizer)
		policy.managedDataBootstrap = bootstrapAuthorizer
	}
	if err := apigencommand.ValidateDependencies(apiaggregate.GetAPIGenCommandRuntimeContracts(), map[apigencommand.Dependency]bool{
		apigencommand.DependencyAuthorization: apiGenAuthorizer != nil,
		apigencommand.DependencyIdempotency:   platform.apiProtocol != nil,
		apigencommand.DependencyConcurrency:   true,
		apigencommand.DependencyAudit:         true,
		// The persistence-free developer/test composition does not activate
		// durable async commands. Once persistence is enabled, their generated
		// dependency must be present and startup fails closed if it is not.
		apigencommand.DependencyJobQueue: platform.asyncJobs != nil || !runtime.persistenceConfigured,
	}); err != nil {
		return fmt.Errorf("validate generated command dependencies: %w", err)
	}
	platform.apiProtocol.SetReplayAuthorize(apiGenAuthorizer.AuthorizeReplay)
	appResponder := apiprotocol.TransportErrorResponder{Logger: platform.logger}
	appAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return apigenapi.DispatchAPIGenOperation(operationID, apiDispatcher, appResponder, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build application APIGen transport: %w", err)
	}
	accessAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.accessModule.DispatchAPIGenOperation(operationID, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Access APIGen transport: %w", err)
	}
	agentAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.agentModule.DispatchAPIGenOperation(operationID, w, r, platform.logger)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Agent APIGen transport: %w", err)
	}
	analyticsAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return analyticsmodule.DispatchAPIGenOperation(analyticsAPI, operationID, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Analytics APIGen transport: %w", err)
	}
	projectAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return projecthttp.DispatchAPIGenOperation(operationID, routes.releaseModule, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Project APIGen transport: %w", err)
	}
	refreshAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.refreshModule.DispatchAPIGenOperation(operationID, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Refresh APIGen transport: %w", err)
	}
	deploymentAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.deploymentModule.DispatchAPIGenOperation(operationID, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Deployment APIGen transport: %w", err)
	}
	releaseAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.releaseModule.DispatchAPIGenOperation(operationID, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Release APIGen transport: %w", err)
	}
	managedDataAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.managedDataModule.DispatchAPIGenOperation(operationID, routes.releaseModule, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build ManagedData APIGen transport: %w", err)
	}
	dashboardAPIHandler, err := apiapigenruntime.Build(apiGenAuthorizer, func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		return routes.dashboardModule.DispatchAPIGenOperation(operationID, platform.logger, w, r)
	}, apiaggregate.GetAPIGenCommandRuntimeContract)
	if err != nil {
		return fmt.Errorf("build Dashboard APIGen transport: %w", err)
	}
	platform.apiGenServers = apiaggregate.Servers{
		Access: accessAPIHandler, Agent: agentAPIHandler, Analytics: analyticsAPIHandler,
		Dashboard: dashboardAPIHandler, Deployment: deploymentAPIHandler, LeapViewAPI: appAPIHandler,
		ManagedData: managedDataAPIHandler, Project: projectAPIHandler,
		Refresh: refreshAPIHandler, Release: releaseAPIHandler,
	}
	configurePageStream(routes, runtime, platform, policy)
	platform.health = newHealth(healthConfig{
		Platform: func(ctx context.Context) error {
			if runtime.platformHealth == nil {
				return errors.New("platform store is missing")
			}
			return runtime.platformHealth.Ping(ctx)
		},
		Analytics: func() error {
			if runtime.analyticsModule == nil {
				return nil
			}
			return runtime.analyticsModule.Healthy()
		},
		RuntimeLeaseReady: func(context.Context) error {
			if runtime.runtimeHostModule == nil {
				return nil
			}
			return runtime.runtimeHostModule.LeaseRenewalError()
		},
		Checks: map[string]func(context.Context) error{
			"apiIdempotency": func(context.Context) error {
				return platform.apiProtocol.LeaseRenewalError()
			},
			"mapAssets": func(ctx context.Context) error {
				if routes.dashboardAssets == nil {
					return nil
				}
				return routes.dashboardAssets.Verify(ctx)
			},
			"deliveryStartup": runtimeConfig.DeliveryStartup,
		},
		ActiveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			if runtime.runtimeHostModule == nil {
				return "", errors.New("runtime host is missing")
			}
			projectID := runtime.runtimeHostModule.ProjectID()
			if err := projectID.Validate(); err != nil {
				return "", err
			}
			return projectID, nil
		},
		RuntimeReady: func(ctx context.Context) error {
			if runtime.runtimeHostModule == nil {
				return errors.New("runtime host is missing")
			}
			// ProjectID is configured at process startup, so it does not by
			// itself prove that this exact project/environment has an active
			// serving generation. Check the repository-backed scope first and
			// only acquire a lease once one exists.
			if _, _, err := runtime.runtimeHostModule.ActiveArtifact(ctx); err != nil {
				if errors.Is(err, servingstate.ErrNotFound) {
					return errNoActiveDeployment
				}
				return err
			}
			lease, err := runtime.runtimeHostModule.Acquire(ctx)
			if err != nil {
				return err
			}
			if lease == nil {
				return errors.New("runtime host returned a nil lease")
			}
			lease.Release()
			return nil
		},
		RequireActiveDeployment: platform.requireActiveDeployment,
	})
	platform.workers = platformlifecycle.New(
		platformlifecycle.Component{Start: routes.refreshModule.Start, Stop: routes.refreshModule.Stop},
		platformlifecycle.Component{
			Start: func(ctx context.Context) error { routes.managedDataModule.Start(ctx); return nil },
			Stop:  routes.managedDataModule.Stop,
		},
		platformlifecycle.Component{Start: routes.dashboardModule.Start, Stop: routes.dashboardModule.Stop},
		platformlifecycle.Component{Start: platform.jobModule.Start, Stop: platform.jobModule.Stop},
	)
	return nil
}

func validateCanonicalConnectionBindingScope(
	scope analyticsmodule.ConnectionBindingScope,
	activeProjectID projectgraph.ResourceID,
	configuredEnvironment string,
) error {
	if err := scope.ProjectID.Validate(); err != nil {
		return err
	}
	if scope.ProjectID != activeProjectID {
		return fmt.Errorf("connection binding project %q is not the active project %q", scope.ProjectID, activeProjectID)
	}
	if scope.Environment == "" || scope.Environment != strings.TrimSpace(scope.Environment) {
		return errors.New("connection binding environment is required")
	}
	if scope.Environment != configuredEnvironment {
		return fmt.Errorf("connection binding environment %q is not the configured environment %q", scope.Environment, configuredEnvironment)
	}
	return nil
}

func writeProductCommandFailure(ctx context.Context, w http.ResponseWriter, r *http.Request, operationID string, cause error) {
	if contracts, ok := apiaggregate.GetAPIGenCommandFailureContracts(operationID); ok && apigenfailure.ValidateContracts(contracts) == nil {
		if contract, matched := apigenfailure.Match(contracts, cause); matched {
			apitransport.WriteAPIGenFailure(ctx, w, r, nil, apitransport.APIGenFailure{
				OperationID: operationID, Kind: contract.Kind, StatusCode: contract.StatusCode,
				Code: contract.Code, PublicDetail: contract.PublicDetail, Cause: cause,
			})
			return
		}
	}
	apitransport.WriteAPIGenFailure(ctx, w, r, nil, apitransport.APIGenFailure{
		OperationID: operationID, Kind: "handler", StatusCode: http.StatusInternalServerError,
		Code: "INTERNAL_ERROR", PublicDetail: "The request could not be completed.", Cause: cause,
	})
}

func hasActiveBootstrapServingState(
	ctx context.Context,
	_ *runtimehostmodule.Module,
	states servingStateRepository,
	environment string,
	targets deliveryTargetReader,
	targetID string,
	projectID string,
) (bool, error) {
	// The delivery target pointer is authoritative for sealed serving. Once a
	// target row exists, an active generation there closes bootstrap even when
	// the legacy serving-state scope table has not been updated (or is stale).
	if targets != nil && strings.TrimSpace(targetID) != "" {
		target, err := targets.DeliveryTargetRevision(ctx, targetID)
		if err == nil {
			if target.TargetID != targetID || target.ProjectID != strings.TrimSpace(projectID) || strings.TrimSpace(target.Environment) != strings.TrimSpace(environment) {
				return false, fmt.Errorf("active delivery target scope does not match %q/%q/%q", targetID, projectID, environment)
			}
			return strings.TrimSpace(target.ActiveGenerationID) != "", nil
		}
		if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
			return false, fmt.Errorf("read active delivery target: %w", err)
		}
	}
	if states == nil {
		return false, errors.New("serving-state repository is unavailable")
	}
	scopes, err := states.ListActiveScopes(ctx)
	if err != nil {
		return false, fmt.Errorf("read active serving scopes: %w", err)
	}
	env := servingstatemodule.Environment(strings.TrimSpace(environment))
	activeCount := 0
	for _, scope := range scopes {
		if scope.Environment != env {
			continue
		}
		if err := scope.ProjectID.Validate(); err != nil {
			return false, fmt.Errorf("active serving project identity is invalid: %w", err)
		}
		activeCount++
		if activeCount > 1 {
			return false, fmt.Errorf("active serving scopes contain multiple projects for environment %q", env)
		}
	}
	if activeCount > 0 {
		return true, nil
	}
	// The legacy scope table is only a compatibility fallback when no canonical
	// target row exists. A runtime host may still be warming up (or be nil in a
	// fresh process) while the durable stores have no active generation.
	return false, nil
}

// bootstrapAPIGenDecision is deliberately a read-only seam. It distinguishes
// a typed empty active-generation pointer from a serving-state store failure,
// then evaluates only the durable singleton claim and the explicit candidate
// or managed-data operation allowlist. Credential role/capability evidence is
// enforced by the APIGen wrapper and by deployment's arm/worker revalidator,
// never here.
func bootstrapAPIGenDecision(
	ctx context.Context,
	runtimeHost *runtimehostmodule.Module,
	states servingStateRepository,
	claims deploymentmodule.ProjectClaimReader,
	environment, operationID string,
	projectID projectgraph.ResourceID,
	targets deliveryTargetReader,
	targetID string,
) (accessmodule.APIGenBootstrapDecision, error) {
	active, err := hasActiveBootstrapServingState(ctx, runtimeHost, states, environment, targets, targetID, projectID.String())
	if err != nil {
		return accessmodule.APIGenBootstrapDecision{}, err
	}
	if active {
		return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
	}
	if err := projectID.Validate(); err != nil || projectID.String() != strings.TrimSpace(projectID.String()) {
		return accessmodule.APIGenBootstrapDecision{Handled: true}, nil
	}
	if claims == nil {
		return accessmodule.APIGenBootstrapDecision{}, errors.New("project claim repository is unavailable")
	}
	claim, err := claims.GetProjectClaim(ctx)
	if errors.Is(err, deployment.ErrProjectClaimNotFound) {
		return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: bootstrapOperationAllowedWithoutClaim(operationID)}, nil
	}
	if err != nil {
		return accessmodule.APIGenBootstrapDecision{}, fmt.Errorf("read bootstrap project claim: %w", err)
	}
	if claim.ProjectID != projectID || claim.Environment != servingstatemodule.Environment(strings.TrimSpace(environment)) {
		return accessmodule.APIGenBootstrapDecision{Handled: true}, nil
	}
	return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: bootstrapOperationAllowed(operationID)}, nil
}

func bootstrapOperationAllowed(operationID string) bool {
	switch operationID {
	case "startProjectCandidate", "getProjectCandidate", "replaceProjectCandidateArtifact", "retryProjectCandidate", "cancelProjectCandidate", "publishProjectCandidate", "reviewProjectCandidate", "cancelProjectCandidateByKey", "planProjectCandidateSynchronization", "uploadProjectCandidateSourceBlob", "retainProjectCandidateSource", "commitProjectCandidateSynchronization", "createDeliveryPlan", "buildDeliveryPlan", "publishDeliveryCandidate",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload":
		return true
	case "managedDataTusTransport":
		return true
	default:
		return false
	}
}

func bootstrapOperationAllowedWithoutClaim(operationID string) bool {
	switch operationID {
	case "startProjectCandidate", "planProjectCandidateSynchronization",
		"createManagedDataUploadSession", "getManagedDataUploadSession", "cancelManagedDataUploadSession", "finalizeManagedDataUploadSession",
		"createManagedDataS3MultipartUpload", "signManagedDataS3MultipartPart", "completeManagedDataS3MultipartUpload", "abortManagedDataS3MultipartUpload",
		"managedDataTusTransport":
		return true
	default:
		return false
	}
}

// deliveryRoleAllows is the only project-wide escape hatch in delivery
// authorization. Explicit project role bindings are target-owned policy; a
// direct resource grant must still match every affected graph resource.
func deliveryRoleAllows(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, capability access.Capability) bool {
	for _, binding := range snapshot.RoleBindings() {
		for _, subject := range subjects {
			if binding.Subject != subject {
				continue
			}
			for _, captured := range binding.Capabilities {
				if captured == capability {
					return true
				}
			}
		}
	}
	return false
}

func deliveryAuthorizationPlan(ctx context.Context, reader deployment.DeliveryReader, operationID, objectID string) (deployment.DeliveryPlan, error) {
	if strings.TrimSpace(objectID) == "" {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	loadPlan := func(planID string) (deployment.DeliveryPlan, error) {
		if strings.TrimSpace(planID) == "" {
			return deployment.DeliveryPlan{}, sql.ErrNoRows
		}
		return reader.PlanByID(ctx, planID)
	}
	switch operationID {
	case "buildDeliveryPlan", "getDeliveryPlanPreview":
		return loadPlan(objectID)
	case "publishDeliveryCandidate", "getDeliveryCandidateStatus":
		candidate, err := reader.DeliveryCandidateByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(candidate.PlanID)
	case "rollbackDeliveryGeneration", "getDeliveryGenerationStatus":
		generation, err := reader.DeliveryGenerationByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(generation.PlanID)
	case "getDeliveryBuildStatus":
		attempt, err := reader.DeliveryBuildAttemptByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(attempt.PlanID)
	case "getDeliverySealStatus":
		seal, err := reader.DeliveryCatalogSealByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(seal.PlanID)
	case "getDeliveryPublicationEvidence":
		publication, err := reader.DeliveryPublicationByID(ctx, objectID)
		if err != nil {
			return deployment.DeliveryPlan{}, err
		}
		return loadPlan(publication.PlanID)
	default:
		return deployment.DeliveryPlan{}, fmt.Errorf("unsupported delivery authorization operation %q", operationID)
	}
}

func deliveryAuthorizationResources(plan deployment.DeliveryPlan) ([]access.ResourceRef, error) {
	impact := append([]deployment.DeliveryImpactResource{}, plan.Evidence.GraphImpact.Added...)
	impact = append(impact, plan.Evidence.GraphImpact.Removed...)
	impact = append(impact, plan.Evidence.GraphImpact.DirectlyModified...)
	impact = append(impact, plan.Evidence.GraphImpact.IndirectlyAffected...)
	resources := make([]access.ResourceRef, 0, len(impact))
	seen := make(map[string]struct{}, len(impact))
	for _, item := range impact {
		id, err := projectgraph.NewResourceID(strings.TrimSpace(item.ID))
		if err != nil {
			return nil, err
		}
		kind, err := projectgraph.ParseKind(strings.TrimSpace(item.Kind))
		if err != nil {
			return nil, err
		}
		resource, err := access.NewResourceRef(id, kind)
		if err != nil {
			return nil, err
		}
		key := resource.ID().String() + "\x00" + string(resource.Kind())
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		resources = append(resources, resource)
	}
	return resources, nil
}

func deliverySnapshotAllows(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, resources []access.ResourceRef, capability access.Capability) (bool, error) {
	for _, resource := range resources {
		allowed := false
		for _, subject := range subjects {
			candidate, err := snapshot.Allows(subject, resource, capability)
			if err != nil {
				return false, err
			}
			if candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}
