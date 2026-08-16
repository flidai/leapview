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
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	"github.com/flidai/leapview/internal/platform/jobs"
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
	releasemodule "github.com/flidai/leapview/internal/release/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/go-chi/chi/v5"
)

type QueryMetrics = dashboardmodule.Metrics
type workspaceMetrics = dashboardmodule.WorkspaceMetrics

type capabilityRoutes struct {
	accessModule       *accessmodule.Module
	developModule      *projectmodule.DevelopModule
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
	candidateMetrics      func(runtimehostmodule.Provider, string) QueryMetrics
	runtimeHostModule     *runtimehostmodule.Module
	projectID             projectgraph.ResourceID
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
	defaultEnvironment string
	scimBearerToken    string
	metricsBearerToken string
	allowedHosts       []string
	rateLimits         apihttpmiddleware.RateLimitConfig
	securityHeaders    apihttpmiddleware.SecurityHeadersConfig
	requestBodyLimit   apihttpmiddleware.RequestBodyLimitConfig
	requestLogging     bool
	managedDataTus     http.Handler
	desktopDiscovery   http.Handler
}

type persistenceInputs struct {
	agentSettings    agentmodule.Settings
	adminDatabase    *sql.DB
	servingStateRepo servingStateRepository
	accessRepo       accessmodule.Repository
	product          *adminmodule.ProductService
	productStatus    adminmodule.ProductStatus
}

type workflowInputs struct {
	managedDataValidation refreshmodule.CandidateValidationHook
	managedDataResolver   runtimehostmodule.ManagedDataResolver
	refreshPipelineClock  refreshmodule.Clock
	agent                 *agentmodule.Service
	agentConfig           agentmodule.ModelConfig
	reloader              runtimeReloader
	deploymentConfig      deploymentmodule.Config
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
	AccessRepo       accessmodule.Repository
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
}

type workflowAssemblyInputs struct {
	AgentSettings         agentmodule.Settings
	ManagedDataValidation refreshmodule.CandidateValidationHook
	ManagedDataResolver   runtimehostmodule.ManagedDataResolver
	AgentConfig           agentmodule.ModelConfig
	Auth                  *accessmodule.Auth
	Reloader              runtimeReloader
	Workload              workloadControl
	DeploymentConfig      deploymentmodule.Config
	RefreshPipelineClock  refreshmodule.Clock
	QueryAudit            *analyticsmodule.QueryAuditSurface
}

type runtimeAssemblyInputs struct {
	RuntimeHost             *runtimehostmodule.Module
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
	dataAccessRepo := data.AccessRepo
	var dataAuthorization accessmodule.DataAuthorizationService = dataAccessRepo
	if capabilities.AccessModule != nil {
		dataAuthorization = capabilities.AccessModule.DataAuthorizationService()
	}
	if metrics != nil && dataAuthorization != nil && (data.AccessRepo != nil || workflow.Auth != nil || capabilities.AccessModule != nil) {
		metrics = dashboardmodule.WithQueryAuthorization(metrics, dashboardmodule.QueryAuthorizationConfig{
			Repository: dataAuthorization,
			PrincipalFromContext: func(ctx context.Context) (dashboardmodule.QueryPrincipal, bool) {
				principal, ok := accessmodule.PrincipalFromContext(ctx)
				return dashboardmodule.QueryPrincipal{ID: principal.ID, DevBypass: principal.DevBypass || workflow.Auth == nil}, ok
			},
			CredentialFromContext: accessmodule.APICredentialFromContext,
			TokenAllows:           accessmodule.TokenAllows,
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
	runtime.queryAuditProvider = queryAuditProvider
	runtime.candidateMetrics = func(provider runtimehostmodule.Provider, workspaceID string) QueryMetrics {
		if provider == nil || strings.TrimSpace(workspaceID) == "" {
			return nil
		}
		var candidate QueryMetrics = dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{Provider: provider, ProjectID: workspaceID})
		candidate = dashboardmodule.WithAdmission(candidate, controller)
		if dataAuthorization != nil && (data.AccessRepo != nil || workflow.Auth != nil || capabilities.AccessModule != nil) {
			candidate = dashboardmodule.WithQueryAuthorization(candidate, dashboardmodule.QueryAuthorizationConfig{
				Repository: dataAuthorization,
				PrincipalFromContext: func(ctx context.Context) (dashboardmodule.QueryPrincipal, bool) {
					principal, ok := accessmodule.PrincipalFromContext(ctx)
					return dashboardmodule.QueryPrincipal{ID: principal.ID, DevBypass: principal.DevBypass || workflow.Auth == nil}, ok
				},
				CredentialFromContext: accessmodule.APICredentialFromContext,
				TokenAllows:           accessmodule.TokenAllows,
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
	if runtime.storageRetention == nil {
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
		authorizeConnection := accessmodule.ConnectionAuthorizerFromSnapshot(authorizationSnapshot, routes.accessModule.AuthorizationSubjects)
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
		if routes.managedDataModule != nil {
			routes.managedDataModule.SetAuthorizeConnection(authorizeConnection)
		}
		if routes.releaseModule != nil {
			routes.releaseModule.SetAuthorizeConnection(authorizeConnection)
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
	if err := configureModules(routes, runtime, platform, policy, ctx, data.Database, persistence, moduleWorkflow, storage); err != nil {
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

func configureModules(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, ctx context.Context, database *sql.DB, persistence persistenceInputs, moduleWorkflow workflowInputs, storage storageInputs) error {
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
				EnsureScope: func(context.Context, analyticsmodule.ConnectionBindingScope) error { return nil },
				Authorize: func(
					ctx context.Context,
					principalID string,
					permission analyticsmodule.ConnectionAdministrationPermission,
					binding analyticsmodule.ConnectionTargetBinding,
				) error {
					var privilege accessmodule.Privilege
					switch permission {
					case analyticsmodule.PermissionManageConnectionMetadata:
						privilege = accessmodule.PrivilegeManageConnectionMetadata
					case analyticsmodule.PermissionTestConnection:
						privilege = accessmodule.PrivilegeTestConnection
					case analyticsmodule.PermissionViewConnectionHealth:
						privilege = accessmodule.PrivilegeViewConnectionHealth
					default:
						return analyticsmodule.ErrConnectionBindingUnauthorized
					}
					allowed, err := routes.accessModule.AuthorizeObject(
						ctx,
						principalID,
						privilege,
						accessmodule.WorkspaceObject(binding.Scope.WorkspaceID),
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
					record: routes.accessModule.RecordAudit,
				},
				AdministrationAudit: connectionAdministrationAuditRecorder{
					record: routes.accessModule.RecordAudit,
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
			ProjectID: func(value string) string {
				return value
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
			InstanceID: storage.instanceID, PublicURL: storage.publicURL,
			Presentation: webpage.Presentation{ProductName: brand.Name, FaviconPath: brand.FaviconPath},
			Assets:       platform.assets,
		})
		if err != nil {
			return fmt.Errorf("build access module: %w", err)
		}
	}
	if routes.developModule == nil {
		identity := func(ctx context.Context) (projectgraph.ServingIdentity, error) {
			if runtime.runtimeHostModule == nil {
				return projectgraph.ServingIdentity{}, errors.New("active project runtime is unavailable")
			}
			lease, err := runtime.runtimeHostModule.Acquire(ctx)
			if err != nil {
				return projectgraph.ServingIdentity{}, err
			}
			defer lease.Release()
			return lease.Identity(), nil
		}
		var err error
		routes.developModule, err = projectmodule.BuildDevelop(projectmodule.DevelopConfig{
			Catalog: capabilities.ProjectCatalog, ProjectID: func(*http.Request) (projectgraph.ResourceID, error) { return runtime.projectID, nil },
			Identity: identity, CurrentPrincipal: func(r *http.Request) (string, bool) {
				principal, ok := routes.accessModule.CurrentPrincipal(r)
				return principal.ID, ok
			},
		})
		if err != nil {
			return fmt.Errorf("build project develop module: %w", err)
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
			return routes.accessModule.RecordAudit(ctx, accessmodule.AuditEventInput{
				PrincipalID: event.PrincipalID,
				Action:      event.Action, TargetType: "project_candidate", TargetID: event.CandidateID,
				Privilege: accessmodule.PrivilegeDeploy, Status: string(event.Status), MetadataJSON: event.MetadataJSON,
			})
		}
		config.CandidateSourceBlobAudit = candidateSourceBlobAuditRecorder(routes.accessModule)
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
			States: persistence.servingStateRepo, AuthorizeObject: routes.accessModule.AuthorizeObject,
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
			RecordAudit: routes.accessModule.RecordAudit,
			HTTP: dashboardmodule.HTTPConfig{
				Metrics:          runtime.metrics,
				ProjectID:        runtime.projectID,
				ResolveProjectID: runtimeConfig.ProjectIDResolver,
				Admission:        workloadController(&runtime.workloads), Broker: runtime.broker, Logger: platform.logger,
				Telemetry: routes.dashboardTelemetry,
				CurrentPrincipalID: func(r *http.Request) string {
					principal, ok := accessmodule.PrincipalFromContext(r.Context())
					if !ok {
						return ""
					}
					return principal.ID
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
				DataRefreshedAt: func(ctx context.Context, workspaceID, environment, modelID string) string {
					if routes.refreshModule == nil {
						return ""
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, workspaceID, environment, modelID)
					if err != nil || !ok {
						return ""
					}
					return version.RefreshedAt.Format(time.RFC3339)
				},
				QueryFreshness: func(ctx context.Context, workspaceID, modelID, servingSnapshot string) (dashboardmodule.QueryFreshness, bool) {
					if routes.refreshModule == nil {
						return dashboardmodule.QueryFreshness{}, false
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, workspaceID, policy.defaultEnvironment, modelID)
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
				Metrics:   runtime.metrics,
				ProjectID: runtimeConfig.ProjectID,
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
				QueryFreshness: func(ctx context.Context, workspaceID, modelID, servingSnapshot string) (dashboardmodule.QueryFreshness, bool) {
					if routes.refreshModule == nil {
						return dashboardmodule.QueryFreshness{}, false
					}
					version, ok, err := routes.refreshModule.DataVersion(ctx, workspaceID, policy.defaultEnvironment, modelID)
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
			RunWorkloadClass:   string(workloadmodule.BackgroundClass), GlobalWorkspaceID: workloadmodule.GlobalWorkspace,
			Environment: func(r *http.Request) string {
				return string(requestServingEnvironment(policy.defaultEnvironment, r))
			},
			DashboardMetrics: func(workspaceID string) (QueryMetrics, bool) {
				return metricsForWorkspace(runtime.metrics, workspaceID)
			},
			AuthorizeAnyObject:       routes.accessModule.AuthorizeAnyObject,
			SkipContextAuthorization: platform.auth == nil,
			RecordAudit:              routes.accessModule.RecordAudit,
			Documentation:            documentation,
			Catalog:                  agentmodule.BuildCatalog(agentmodule.CatalogConfig{ProjectCatalog: capabilities.ProjectCatalog}),
			QueryMetadata: func(ctx context.Context, workspaceID, modelID string) agentmodule.VisualQueryMetadata {
				metadata := agentmodule.VisualQueryMetadata{ServingSnapshot: "unversioned"}
				if routes.refreshModule == nil {
					return metadata
				}
				version, ok, err := routes.refreshModule.DataVersion(ctx, workspaceID, policy.defaultEnvironment, modelID)
				if err != nil || !ok {
					return metadata
				}
				status := "stale"
				if version.ServingStateID == metadata.ServingSnapshot {
					status = "current"
				}
				metadata.Freshness = &agentmodule.QueryFreshness{
					LastSuccessfulRefreshAt: version.RefreshedAt.UTC().Format(time.RFC3339),
					SnapshotID:              strconv.FormatInt(version.SnapshotID, 10),
					ServingStateID:          version.ServingStateID,
					Source:                  version.Source,
					Status:                  status,
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
				scope := agentmodule.Scope{
					PrincipalID: identity.PrincipalID, DevAuthBypass: identity.DevBypass,
					Credential: agentmodule.CredentialScope{
						WorkspaceID: identity.Credential.Token.WorkspaceID,
						Restricted:  identity.Restricted,
					},
				}
				for _, privilege := range identity.Credential.Token.Privileges {
					scope.Credential.Privileges = append(scope.Credential.Privileges, string(privilege))
				}
				return scope, true
			},
			DispatchAPIGen: func(scope agentmodule.Scope, operationID string, writer http.ResponseWriter, request *http.Request) bool {
				principal := accessmodule.Principal{ID: scope.PrincipalID, DevBypass: scope.DevAuthBypass}
				if platform.auth == nil && strings.TrimSpace(principal.ID) == "" {
					principal = accessmodule.LocalDeveloperPrincipal()
				}
				ctx := accessmodule.WithPrincipal(request.Context(), principal)
				if scope.Credential.Restricted || scope.Credential.WorkspaceID != "" || len(scope.Credential.Privileges) > 0 {
					ctx = accessmodule.WithAPICredential(ctx, accessmodule.AgentAPICredential(
						scope.PrincipalID, scope.Credential.WorkspaceID, scope.Credential.Privileges,
					))
				}
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
				if scope.Credential.Restricted || scope.Credential.WorkspaceID != "" || len(scope.Credential.Privileges) > 0 {
					ctx = accessmodule.WithAPICredential(ctx, accessmodule.AgentAPICredential(
						scope.PrincipalID, scope.Credential.WorkspaceID, scope.Credential.Privileges,
					))
				}
				return ctx
			},
			HTTP: agentmodule.HTTPConfig{
				Settings: persistence.agentSettings, Broker: runtime.broker,
				PlatformAdmin:    routes.accessModule.IsPlatformAdmin,
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
				CurrentCredential: func(r *http.Request) (accessmodule.APICredential, bool) {
					if platform.auth == nil {
						return accessmodule.APICredential{}, false
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
			CurrentCredential: func(r *http.Request) (accessmodule.APICredential, bool) {
				if platform.auth == nil {
					return accessmodule.APICredential{}, false
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
				return manageddatamodule.Principal{ID: principal.ID}, ok
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
	})
	if err != nil {
		return fmt.Errorf("build APIGen authorizer: %w", err)
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
		},
		ActiveWorkspaces: func(context.Context) ([]string, error) {
			if runtime.projectID == "" {
				return nil, nil
			}
			return []string{runtime.projectID.String()}, nil
		},
		RuntimeReady:            routes.dashboardModule.RuntimeReady,
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

func authorizeListObject(access *accessmodule.Module, authenticationRequired bool, ctx context.Context, principalID string, object accessmodule.ObjectRef) (bool, error) {
	if !authenticationRequired {
		return true, nil
	}
	if strings.TrimSpace(principalID) == "" {
		return false, nil
	}
	return access.AuthorizeObject(ctx, principalID, accessmodule.PrivilegeViewItem, object)
}

func metricsForWorkspace(metrics QueryMetrics, workspaceID string) (QueryMetrics, bool) {
	if workspaceID == "" {
		return nil, false
	}
	if provider, ok := metrics.(workspaceMetrics); ok {
		return provider.MetricsForWorkspace(workspaceID)
	}
	if metrics == nil {
		return nil, false
	}
	catalog := metrics.Catalog()
	if catalog.Workspace.ID == "" || catalog.Workspace.ID == workspaceID {
		return metrics, true
	}
	return nil, false
}
