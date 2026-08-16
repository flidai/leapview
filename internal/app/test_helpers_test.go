package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	authoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

// assemblyConfig is deliberately test-only. Focused capability tests use it
// while they are moved beside their owners; production has no general
// dependency bag.
type assemblyConfig struct {
	Database                *sql.DB
	PlatformHealth          platformHealth
	AgentSettings           agentmodule.Settings
	AdminDatabase           *sql.DB
	ServingStateRepo        servingStateRepository
	StorageRetention        *servingstatemodule.Retention
	ManagedDataValidation   refreshmodule.CandidateValidationHook
	ManagedDataResolver     runtimehostmodule.ManagedDataResolver
	ReleaseModule           *releasemodule.Module
	JobModule               *jobsmodule.Module
	AccessRepo              access.Repository
	AccessModule            *accessmodule.Module
	Agent                   *agentmodule.Service
	AgentConfig             agentmodule.ModelConfig
	Auth                    *accessmodule.Auth
	Reloader                runtimeReloader
	DuckDBDir               string
	DuckLakeCatalogPath     string
	DuckLakeDataPath        string
	DefaultEnvironment      string
	SCIMBearerToken         string
	MetricsBearerToken      string
	AllowedHosts            []string
	Assets                  staticasset.Resolver
	RateLimits              apihttpmiddleware.RateLimitConfig
	SecurityHeaders         apihttpmiddleware.SecurityHeadersConfig
	RequestBodyLimit        apihttpmiddleware.RequestBodyLimitConfig
	RequestLogging          bool
	Logger                  *slog.Logger
	Workload                workloadControl
	JobLeaseTimeout         time.Duration
	ManagedDataModule       *manageddatamodule.Module
	DeploymentConfig        deploymentmodule.Config
	ManagedDataTus          http.Handler
	MCPOAuth                MCPOAuthConfig
	PublicURL               string
	DesktopDiscovery        desktopdiscovery.Config
	RefreshPipelineClock    refreshmodule.Clock
	RefreshMaterializer     refreshrun.Materializer
	EnableRefreshDispatcher bool
	RuntimeHost             *runtimehostmodule.Module
	ProjectID               projectgraph.ResourceID
	ProjectIDResolver       func(context.Context) (projectgraph.ResourceID, error)
	ServingSnapshotResolver func(context.Context) (string, error)
	AnalyticsModule         *analyticsmodule.Module
	Authoring               *authoringapplication.Application
	DashboardAssets         dashboardmodule.Assets
	QueryAudit              *analyticsmodule.QueryAuditSurface
	Product                 *adminmodule.ProductService
	ProductStatus           adminmodule.ProductStatus
	ProjectCatalog          *projectcatalog.Service
	ProjectGraph            projecthttp.GraphReader
}

// appTestHarness is the test-only composition adapter used by app-package tests.
// Production composition exposes only the final handler and lifecycle.
type appTestHarness struct {
	routes   capabilityRoutes
	runtime  runtimeServices
	platform platformServices
	policy   httpPolicy
}

func (s *appTestHarness) Routes() http.Handler {
	return Routes(&s.routes, &s.runtime, &s.platform, &s.policy)
}

func (s *appTestHarness) StartBackgroundJobs(ctx context.Context) error {
	if s == nil || s.platform.workers == nil {
		return nil
	}
	return s.platform.workers.Start(ctx)
}

func (s *appTestHarness) StopBackgroundJobs(ctx context.Context) error {
	if s == nil || s.platform.workers == nil {
		return nil
	}
	return s.platform.workers.Stop(ctx)
}

func (s *appTestHarness) workloadController() workloadControl {
	return workloadController(&s.runtime.workloads)
}

func (s *appTestHarness) requestServingEnvironment(r *http.Request) servingstatemodule.Environment {
	return requestServingEnvironment(s.policy.defaultEnvironment, r)
}

func (s *appTestHarness) publicProtocolMiddleware(next http.Handler) http.Handler {
	return publicProtocolMiddleware(s.platform.apiProtocol, next)
}

func assembleRuntime(metrics QueryMetrics, options assemblyConfig) *appTestHarness {
	server, err := assembleRuntimeChecked(context.Background(), metrics, options)
	if err != nil {
		panic(err)
	}
	return server
}

func newAppTestHarness(metrics QueryMetrics) *appTestHarness {
	return assembleRuntime(metrics, assemblyConfig{})
}

func apiGenDispatcherForTest(server *appTestHarness) apiGenDispatcher {
	return apiGenDispatcher{
		managedDataModule:  server.routes.managedDataModule,
		arrowQueries:       supportsNativeArrow(server.runtime.metrics),
		defaultEnvironment: server.policy.defaultEnvironment, managedDataTus: server.policy.managedDataTus,
		instanceID: "lvinst_test", canonicalOrigin: "http://localhost:8080",
		buildIdentity: server.platform.buildIdentity,
	}
}

func assembleRuntimeChecked(ctx context.Context, metrics QueryMetrics, options assemblyConfig) (*appTestHarness, error) {
	instanceID := "lvinst_test"
	// The production composition receives its project identity from the active
	// serving scope. Test fixtures do not open a serving lease, so derive the
	// canonical identity from the metrics catalog (or use the shared fixture
	// project when metrics are intentionally absent).
	if options.ProjectID == "" {
		if metrics != nil {
			options.ProjectID = metrics.Catalog().Project.ID
		}
		if options.ProjectID == "" {
			options.ProjectID = testProjectID
		}
	}
	publicURL := options.PublicURL
	if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	if options.AccessModule == nil {
		var err error
		options.AccessModule, err = accessmodule.Build(ctx, accessmodule.Config{
			Database:     options.Database,
			ExistingAuth: options.Auth, Auth: accessmodule.AuthConfig{Disabled: options.Auth == nil},
			Assets: options.Assets, InstanceID: instanceID, PublicURL: publicURL,
		})
		if err != nil {
			return nil, err
		}
	}
	if options.ProjectCatalog == nil && options.AccessModule != nil && options.RuntimeHost != nil {
		catalog, err := projectcatalog.NewService(
			projectCatalogLeaseProvider{provider: options.RuntimeHost.Provider()},
			projectCatalogSubjectResolver{resolve: options.AccessModule.AuthorizationSubjects},
		)
		if err != nil {
			return nil, err
		}
		options.ProjectCatalog = catalog
	}
	routes, runtime, platform, policy, err := buildApplicationSurfaces(ctx, metrics,
		dataAssemblyInputs{
			Database: options.Database, PlatformHealth: options.PlatformHealth,
			AdminDatabase: options.AdminDatabase, ServingStateRepo: options.ServingStateRepo,
			StorageRetention: options.StorageRetention,
			AccessRepo:       options.AccessRepo,
		},
		capabilityAssemblyInputs{
			ReleaseModule: options.ReleaseModule, JobModule: options.JobModule,
			AccessModule: options.AccessModule, Agent: options.Agent,
			ManagedDataModule: options.ManagedDataModule, AnalyticsModule: options.AnalyticsModule, Authoring: options.Authoring,
			DashboardAssets: options.DashboardAssets, Product: options.Product, ProductStatus: options.ProductStatus,
			ProjectCatalog: options.ProjectCatalog, ProjectGraph: options.ProjectGraph,
		},
		workflowAssemblyInputs{
			AgentSettings: options.AgentSettings, ManagedDataValidation: options.ManagedDataValidation,
			ManagedDataResolver: options.ManagedDataResolver, AgentConfig: options.AgentConfig,
			Auth: options.Auth, Reloader: options.Reloader, Workload: options.Workload,
			DeploymentConfig: options.DeploymentConfig, RefreshPipelineClock: options.RefreshPipelineClock,
			RefreshMaterializer: options.RefreshMaterializer, EnableRefreshDispatcher: options.EnableRefreshDispatcher,
			QueryAudit: options.QueryAudit,
		},
		runtimeAssemblyInputs{
			RuntimeHost: options.RuntimeHost, ProjectID: options.ProjectID,
			ProjectIDResolver: options.ProjectIDResolver, ServingSnapshotResolver: options.ServingSnapshotResolver,
			InstanceID: instanceID,
			DuckDBDir:  options.DuckDBDir, DuckLakeCatalogPath: options.DuckLakeCatalogPath,
			DuckLakeDataPath:   options.DuckLakeDataPath,
			DefaultEnvironment: options.DefaultEnvironment, SCIMBearerToken: options.SCIMBearerToken,
			MetricsBearerToken: options.MetricsBearerToken, AllowedHosts: options.AllowedHosts, Assets: options.Assets,
		},
		httpAssemblyInputs{
			RateLimits: options.RateLimits, SecurityHeaders: options.SecurityHeaders,
			RequestBodyLimit: options.RequestBodyLimit, RequestLogging: options.RequestLogging,
			Logger: options.Logger, JobLeaseTimeout: options.JobLeaseTimeout,
			ManagedDataTus: options.ManagedDataTus, MCPOAuth: options.MCPOAuth,
			PublicURL: publicURL, DesktopDiscovery: options.DesktopDiscovery,
		},
	)
	if err != nil {
		return nil, err
	}
	return &appTestHarness{
		routes: *routes, runtime: *runtime, platform: *platform, policy: *policy,
	}, nil
}

func NewRuntimeMetrics(provider runtimehost.Provider, projectID string) QueryMetrics {
	return dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{Provider: provider, ProjectID: projectgraph.ResourceID(projectID)})
}
