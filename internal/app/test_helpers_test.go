package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
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
	"github.com/flidai/leapview/internal/extension"
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
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// testExactExtensionAdmission is the explicit fixture boundary for app tests
// that open a real DuckDB extension. It resolves reviewed local artifacts once
// and returns their exact immutable paths; production composition uses the
// packaged deployment supply instead.
type testExactExtensionAdmission struct {
	paths map[string]extension.AdmittedExtension
}

var _ extension.Admission = testExactExtensionAdmission{}
var _ extension.Preparation = testExactExtensionAdmission{}

func newTestExactExtensionAdmission(t *testing.T, names ...string) testExactExtensionAdmission {
	t.Helper()
	duckDBVersion, duckDBPlatform, err := runtimeTarget(t.Context())
	if err != nil {
		t.Fatalf("probe test DuckDB runtime: %v", err)
	}
	stagingDir := t.TempDir()
	paths := make(map[string]extension.AdmittedExtension, len(names))
	for _, name := range names {
		sourcePath, sourceErr := findInstalledTestExtension(name, duckDBVersion, duckDBPlatform)
		if sourceErr != nil {
			sourcePath = installTestExtension(t, name, stagingDir)
		}
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read test DuckDB extension %q: %v", name, err)
		}
		digest := sha256.Sum256(contents)
		digestValue := "sha256:" + hex.EncodeToString(digest[:])
		identity := extension.Identity{
			DuckDBVersion: duckDBVersion, ExtensionVersion: "test-fixture", GOOS: runtime.GOOS,
			GOARCH: runtime.GOARCH, Platform: duckDBPlatform, Name: name, Digest: digestValue,
			SupportProfile: "test-fixture",
		}
		canonicalIdentity, err := identity.Canonical()
		if err != nil {
			t.Fatalf("canonicalize test DuckDB extension %q: %v", name, err)
		}
		path := filepath.Join(stagingDir, name+".duckdb_extension")
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("stage test DuckDB extension %q: %v", name, err)
		}
		paths[name] = extension.AdmittedExtension{
			Name: name, Identity: canonicalIdentity, Version: "test-fixture", DuckDBVersion: duckDBVersion,
			ExtensionVersion: "test-fixture", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: duckDBPlatform,
			SupportProfile: "test-fixture", Digest: digestValue, Path: path,
			Origin: "reviewed-local-test-fixture", Provenance: "attest:test-fixture", Signature: "sig:test-fixture",
		}
	}
	return testExactExtensionAdmission{paths: paths}
}

func (a testExactExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	admitted, ok := a.paths[name]
	if !ok {
		return extension.AdmittedExtension{}, fmt.Errorf("test extension %q was not admitted", name)
	}
	return admitted, nil
}

func (a testExactExtensionAdmission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	evidence := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		admitted, err := a.AdmitExtension(ctx, name)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, admitted.Evidence())
	}
	return evidence, nil
}

func findInstalledTestExtension(name, duckDBVersion, duckDBPlatform string, extraRoots ...string) (string, error) {
	filename := name + ".duckdb_extension"
	if name == "sqlite" {
		filename = "sqlite_scanner.duckdb_extension"
	}
	roots := append([]string(nil), extraRoots...)
	if configured := strings.TrimSpace(os.Getenv("DUCKDB_EXTENSION_DIRECTORY")); configured != "" {
		roots = append(roots, configured)
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, filepath.Join(home, ".duckdb", "extensions"))
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidates := make([]string, 0, 4)
		for _, candidate := range []string{
			filepath.Join(root, duckDBVersion, strings.ReplaceAll(duckDBPlatform, "-", "_"), filename),
			filepath.Join(root, duckDBVersion, duckDBPlatform, filename),
			filepath.Join(root, runtime.GOOS+"_"+runtime.GOARCH, filename),
			filepath.Join(root, filename),
		} {
			if candidateInfo, candidateErr := os.Stat(candidate); candidateErr == nil && candidateInfo.Mode().IsRegular() {
				candidates = append(candidates, candidate)
			}
		}
		if len(candidates) == 0 {
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.Type()&os.ModeSymlink != 0 {
					if entry.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if entry.IsDir() || entry.Name() != filename {
					return nil
				}
				pathSlash := filepath.ToSlash(path)
				if !strings.Contains(pathSlash, "/"+duckDBVersion+"/") || (!strings.Contains(pathSlash, "/"+duckDBPlatform+"/") && !strings.Contains(pathSlash, "/"+strings.ReplaceAll(duckDBPlatform, "-", "_")+"/")) {
					return nil
				}
				if entry.Type().IsRegular() {
					candidates = append(candidates, path)
				}
				return nil
			})
		}
		if len(candidates) > 0 {
			sort.Strings(candidates)
			return filepath.Abs(candidates[0])
		}
	}
	return "", fmt.Errorf("reviewed local extension %q is not installed; set DUCKDB_EXTENSION_DIRECTORY", name)
}

func installTestExtension(t *testing.T, name, stagingDir string) string {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB test extension installer: %v", err)
	}
	defer db.Close()
	sqlPath := strings.ReplaceAll(stagingDir, "'", "''")
	if _, err := db.Exec("SET extension_directory = '" + sqlPath + "'"); err != nil {
		t.Fatalf("set test extension directory: %v", err)
	}
	if _, err := db.Exec("INSTALL " + name + " FROM core"); err != nil {
		t.Fatalf("install test extension %q: %v", name, err)
	}
	version, platform, err := runtimeTarget(t.Context())
	if err != nil {
		t.Fatalf("probe installed test DuckDB runtime: %v", err)
	}
	path, err := findInstalledTestExtension(name, version, platform, stagingDir)
	if err != nil {
		t.Fatalf("resolve installed test extension %q: %v", name, err)
	}
	return path
}

func loadTestExtension(t *testing.T, db *sql.DB, admission extension.Admission, name string) {
	t.Helper()
	admitted, err := admission.AdmitExtension(t.Context(), name)
	if err != nil {
		t.Fatalf("admit test DuckDB extension %q: %v", name, err)
	}
	if _, err := db.Exec("LOAD '" + strings.ReplaceAll(admitted.Path, "'", "''") + "'"); err != nil {
		t.Fatalf("load test DuckDB extension %q: %v", name, err)
	}
}

func TestTestExactExtensionAdmissionStagesOwnedArtifactWithCleanCache(t *testing.T) {
	cleanDirectory := t.TempDir()
	t.Setenv("DUCKDB_EXTENSION_DIRECTORY", cleanDirectory)
	admission := newTestExactExtensionAdmission(t, "ducklake")
	admitted, err := admission.AdmitExtension(t.Context(), "ducklake")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(filepath.Clean(admitted.Path), filepath.Clean(cleanDirectory)+string(filepath.Separator)) {
		t.Fatalf("admitted extension escaped test-owned staging setup: %q", admitted.Path)
	}
	if _, err := os.Stat(admitted.Path); err != nil {
		t.Fatalf("staged extension path: %v", err)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loadTestExtension(t, db, admission, "ducklake")
}

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
	RecoveryLifecycle       *refreshmodule.RecoveryLifecycle
	RecoveryInterval        time.Duration
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
	if options.Workload == nil {
		controller, err := workloadmodule.Build(ctx, workloadmodule.Config{Policy: workloadmodule.DefaultConfig()})
		if err != nil {
			return nil, fmt.Errorf("build test workload admission: %w", err)
		}
		options.Workload = controller
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
			RecoveryLifecycle: options.RecoveryLifecycle, RecoveryInterval: options.RecoveryInterval,
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
			AllowDevAuthBypass: true,
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
