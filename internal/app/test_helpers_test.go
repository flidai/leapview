package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
)

// assemblyConfig is deliberately test-only. Focused capability tests use it
// while they are moved beside their owners; production has no general
// dependency bag.
type assemblyConfig struct {
	Database              *sql.DB
	PlatformHealth        platformHealth
	AgentSettings         agentmodule.Settings
	AdminDatabase         *sql.DB
	ServingStateRepo      servingStateRepository
	StorageRetention      *servingstatemodule.Retention
	ManagedDataValidation refreshmodule.CandidateValidationHook
	ManagedDataResolver   runtimehostmodule.ManagedDataResolver
	WorkspaceRepo         workspacemodule.Repository
	WorkspaceDirectory    workspacemodule.Directory
	AssetCatalog          workspacemodule.AssetCatalogReader
	ReleaseModule         *releasemodule.Module
	JobModule             *jobsmodule.Module
	AccessRepo            accessmodule.Repository
	AccessModule          *accessmodule.Module
	Agent                 *agentmodule.Service
	AgentConfig           agentmodule.ModelConfig
	Auth                  *accessmodule.Auth
	Reloader              runtimeReloader
	DuckDBDir             string
	DuckLakeCatalogPath   string
	DuckLakeDataPath      string
	WorkspaceID           string
	DefaultEnvironment    string
	SCIMBearerToken       string
	MetricsBearerToken    string
	AllowedHosts          []string
	Assets                staticasset.Resolver
	RateLimits            apihttpmiddleware.RateLimitConfig
	SecurityHeaders       apihttpmiddleware.SecurityHeadersConfig
	RequestBodyLimit      apihttpmiddleware.RequestBodyLimitConfig
	RequestLogging        bool
	Logger                *slog.Logger
	Workload              workloadControl
	JobLeaseTimeout       time.Duration
	ManagedDataModule     *manageddatamodule.Module
	DeploymentConfig      deploymentmodule.Config
	ManagedDataTus        http.Handler
	MCPOAuth              MCPOAuthConfig
	PublicURL             string
	DesktopDiscovery      desktopdiscovery.Config
	RefreshPipelineClock  refreshmodule.Clock
	AnalyticsModule       *analyticsmodule.Module
	DashboardAssets       dashboardmodule.Assets
	QueryAudit            *analyticsmodule.QueryAuditSurface
	Product               *adminmodule.ProductService
	ProductStatus         adminmodule.ProductStatus
}

// registeredTestMetrics makes the test fixture's declared workspace explicit
// while preserving any real multi-workspace routing supplied by the test.
type registeredTestMetrics struct {
	QueryMetrics
	workspaceID string
}

func (m registeredTestMetrics) MetricsForWorkspace(workspaceID string) (QueryMetrics, bool) {
	if provider, ok := m.QueryMetrics.(workspaceMetrics); ok {
		if metrics, found := provider.MetricsForWorkspace(workspaceID); found {
			return metrics, true
		}
	}
	if m.QueryMetrics == nil {
		return nil, false
	}
	if catalogWorkspaceID := m.QueryMetrics.Catalog().Workspace.ID; catalogWorkspaceID != "" && catalogWorkspaceID == workspaceID {
		return m.QueryMetrics, true
	}
	if m.workspaceID != "" && m.workspaceID == workspaceID {
		return m.QueryMetrics, true
	}
	return nil, false
}

// appTestHarness is a test fixture facade for legacy app-package tests.
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

func (s *appTestHarness) metricsForWorkspace(workspaceID string) (QueryMetrics, bool) {
	return metricsForWorkspace(s.runtime.metrics, workspaceID)
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
	if options.WorkspaceID != "" {
		// Tests must register every workspace they address explicitly. This mirrors
		// production's workspace catalog without reintroducing an implicit fallback.
		metrics = registeredTestMetrics{QueryMetrics: metrics, workspaceID: options.WorkspaceID}
	}
	instanceID := "lvinst_test"
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
			WorkspaceIDs: func(ctx context.Context) ([]string, error) {
				workspaceIDs := make([]string, 0, 1)
				if options.WorkspaceID != "" {
					workspaceIDs = append(workspaceIDs, options.WorkspaceID)
				}
				if options.WorkspaceDirectory != nil {
					directoryWorkspaceIDs, err := options.WorkspaceDirectory.WorkspaceIDs(ctx)
					if err != nil {
						return nil, err
					}
					workspaceIDs = append(workspaceIDs, directoryWorkspaceIDs...)
				}
				return workspaceIDs, nil
			},
		})
		if err != nil {
			return nil, err
		}
	}
	routes, runtime, platform, policy, err := buildApplicationSurfaces(ctx, metrics,
		dataAssemblyInputs{
			Database: options.Database, PlatformHealth: options.PlatformHealth,
			AdminDatabase: options.AdminDatabase, ServingStateRepo: options.ServingStateRepo,
			StorageRetention: options.StorageRetention, WorkspaceReadModel: options.WorkspaceRepo,
			WorkspaceDirectory: options.WorkspaceDirectory, AssetCatalog: options.AssetCatalog,
			AccessRepo: options.AccessRepo,
		},
		capabilityAssemblyInputs{
			ReleaseModule: options.ReleaseModule, JobModule: options.JobModule,
			AccessModule: options.AccessModule, Agent: options.Agent,
			ManagedDataModule: options.ManagedDataModule, AnalyticsModule: options.AnalyticsModule,
			DashboardAssets: options.DashboardAssets, Product: options.Product, ProductStatus: options.ProductStatus,
		},
		workflowAssemblyInputs{
			AgentSettings: options.AgentSettings, ManagedDataValidation: options.ManagedDataValidation,
			ManagedDataResolver: options.ManagedDataResolver, AgentConfig: options.AgentConfig,
			Auth: options.Auth, Reloader: options.Reloader, Workload: options.Workload,
			DeploymentConfig: options.DeploymentConfig, RefreshPipelineClock: options.RefreshPipelineClock,
			QueryAudit: options.QueryAudit,
		},
		runtimeAssemblyInputs{
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

func NewRuntimeMetrics(provider runtimehost.Provider, workspaceID string) QueryMetrics {
	return dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{Provider: provider, WorkspaceID: workspaceID})
}

func NewDynamicRuntimeMetrics(factory func(string) runtimehost.Provider) QueryMetrics {
	return dashboardmodule.NewDynamicRuntimeMetrics(dashboardmodule.DynamicRuntimeMetricsOptions{ProviderFactory: factory})
}

func NewMultiWorkspaceMetrics(workspaces map[string]QueryMetrics) QueryMetrics {
	return dashboardmodule.NewMultiWorkspaceMetrics(workspaces)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
