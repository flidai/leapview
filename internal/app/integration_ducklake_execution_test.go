package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	queryauditsqlite "github.com/flidai/leapview/internal/analytics/queryaudit/sqlite"
	"github.com/flidai/leapview/internal/dashboard/api"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	"github.com/flidai/leapview/internal/manageddata"
	manageddatasqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	analyticsmaterializesqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	releasefilesystem "github.com/flidai/leapview/internal/release/filesystem"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	storagemaintenance "github.com/flidai/leapview/internal/servingstate/retention"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

type duckLakeHarness struct {
	*harness
	homeDir     string
	dataDir     string
	artifactDir string
	duckDBDir   string
	runtimeDir  string
	catalogPath string
	dataPath    string
	deployments *servingstatesqlite.Repository
	registry    *runtimehost.Registry
	appServer   *appTestHarness
	database    *analyticsducklake.Environment
}

func (h *duckLakeHarness) runStorageMaintenance(ctx context.Context, dryRun bool) (report storagemaintenance.Report, err error) {
	snapshots, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: h.homeDir, CatalogPath: h.catalogPath, DataPath: h.dataPath,
	})
	if err != nil {
		return storagemaintenance.Report{}, err
	}
	defer func() {
		if closeErr := snapshots.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return storagemaintenance.Run(ctx, h.deployments, storagemaintenance.Options{
		Snapshots: snapshots, Environment: string(servingstate.DefaultEnvironment),
		CatalogPath: h.catalogPath, DataPath: h.dataPath, DryRun: dryRun,
	})
}

func newDuckLakeHarness(t *testing.T, opts ...func(*assemblyConfig)) *duckLakeHarness {
	t.Helper()
	ctx := context.Background()
	homeDir := t.TempDir()
	dataDir := filepath.Join(homeDir, "source")
	artifactDir := filepath.Join(homeDir, "artifacts")
	duckDBDir := filepath.Join(homeDir, ".leapview", "duckdb")
	runtimeDir := filepath.Join(homeDir, ".leapview", "runtime")
	dataPath := filepath.Join(homeDir, ".leapview", "data")
	platformDBPath := filepath.Join(homeDir, ".leapview", "leapview.db")
	catalogPath := filepath.Join(homeDir, ".leapview", "ducklake", "catalog.duckdb")
	for _, dir := range []string{dataDir, artifactDir, duckDBDir, runtimeDir, dataPath, filepath.Dir(platformDBPath), filepath.Dir(catalogPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create harness dir %s: %v", dir, err)
		}
	}
	writeIntegrationMinimalOlistFixture(t, dataDir)
	store, err := platform.Open(ctx, platformDBPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	duckDBEnvironment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: filepath.Dir(catalogPath), CatalogPath: catalogPath, DataPath: dataPath, MaxConnections: workload.DefaultConfig().MaxRunning,
	})
	if err != nil {
		t.Fatalf("open DuckDB environment: %v", err)
	}
	t.Cleanup(func() { _ = duckDBEnvironment.Close() })
	workspaceID := "sales"
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	if err := workspaceRepo.Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: "Sales Workspace"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	accessRepo := accesssqlite.NewRepository(store.SQLDB())
	if err := SeedLocalDeveloperPlatformAdmin(ctx, accessRepo); err != nil {
		t.Fatalf("seed local developer: %v", err)
	}
	deploymentRepo := servingstatesqlite.NewRepository(store.SQLDB())
	projectPath := discoverCatalogPath(t)
	initial := createAndActivateProjectDeployment(t, ctx, deploymentRepo, artifactDir, projectPath, dataDir, duckDBDir, workspaceID, "integration")
	seedIntegrationManagedDataRevision(t, ctx, store, initial.ProjectID)
	var registry *runtimehost.Registry
	registry = runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{
		Repo:         deploymentRepo,
		WorkspaceIDs: []servingstate.WorkspaceID{servingstate.WorkspaceID(workspaceID)},
		Environment:  servingstate.DefaultEnvironment,
		OnDrained: func(servingstate.ID, int64) {
			_, _ = storagemaintenance.Run(context.Background(), deploymentRepo, storagemaintenance.Options{
				Snapshots:                    duckDBEnvironment,
				Environment:                  string(servingstate.DefaultEnvironment),
				CatalogPath:                  catalogPath,
				DataPath:                     dataPath,
				AdditionalProtectedSnapshots: registryLeasedSnapshots(registry),
				DryRun:                       false,
			})
		},
		Factory: duckLakeIntegrationRuntimeFactory{
			database:         duckDBEnvironment,
			managedRoot:      dataDir,
			duckDBDir:        duckDBDir,
			runtimeDir:       runtimeDir,
			catalogPath:      catalogPath,
			duckLakeDataPath: dataPath,
		},
	})
	reloadController, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatalf("create reload workload controller: %v", err)
	}
	reloadLease, err := reloadController.Acquire(ctx, workload.Request{Class: workload.Refresh, WorkspaceID: workspaceID, Operation: "integration-reload"})
	if err != nil {
		reloadController.Close()
		t.Fatalf("admit registry reload: %v", err)
	}
	reloadErr := registry.Reload(reloadLease.Context())
	reloadLease.Release()
	reloadController.Close()
	if reloadErr != nil {
		t.Fatalf("reload registry for %s: %v", initial.ID, reloadErr)
	}
	t.Cleanup(func() { _ = registry.Close() })
	runtimeMetrics := NewDynamicRuntimeMetrics(func(workspaceID string) runtimehost.Provider {
		return registry.ProviderForWorkspace(servingstate.WorkspaceID(workspaceID))
	})
	auth := NewAuth(accessRepo, AuthConfig{DevBypass: true})
	options := testStoreOptions(store, assemblyConfig{
		ServingStateRepo: deploymentRepo,
		WorkspaceRepo:    workspaceRepo,
		AssetCatalog:     workspace.NewAssetCatalogService(workspaceRepo),
		AccessRepo:       accessRepo,
		Auth:             auth,
		Reloader:         registry,

		DuckLakeCatalogPath: catalogPath,
		DuckLakeDataPath:    dataPath,
		AnalyticsModule:     analyticsmodule.NewSurface(duckDBEnvironment, nil),
		ManagedDataResolver: staticIntegrationManagedDataResolver{root: dataDir},
		WorkspaceID:         workspaceID,
		DefaultEnvironment:  string(servingstate.DefaultEnvironment),
		Workload: func() *workload.Controller {
			controller, err := workload.New(workload.DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			return controller
		}(),
	})
	for _, opt := range opts {
		opt(&options)
	}
	server := assembleRuntime(runtimeMetrics, options)
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	server.StartBackgroundJobs(backgroundCtx)
	h := &duckLakeHarness{
		harness: &harness{
			handler:     server.Routes(),
			store:       store,
			workspaceID: workspaceID,
		},
		homeDir:     homeDir,
		dataDir:     dataDir,
		artifactDir: artifactDir,
		duckDBDir:   duckDBDir,
		runtimeDir:  runtimeDir,
		catalogPath: catalogPath,
		dataPath:    dataPath,
		deployments: deploymentRepo,
		registry:    registry,
		appServer:   server,
		database:    duckDBEnvironment,
	}
	h.server = httptest.NewServer(h.handler)
	t.Cleanup(h.server.Close)
	t.Cleanup(func() {
		stopBackground()
		stopServerBackgroundForTest(t, server)
	})
	return h
}

func seedIntegrationManagedDataRevision(t *testing.T, ctx context.Context, store *platform.Store, projectID string) {
	t.Helper()
	repository := manageddatasqlite.NewRepository(store.SQLDB())
	collection, err := repository.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: "olist", ProjectID: projectID, ConnectionName: "olist", Name: "Olist",
	})
	if err != nil {
		t.Fatalf("create integration managed-data collection: %v", err)
	}
	manifest := integrationManagedDataManifest()
	session, err := repository.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		CollectionID: collection.ID, Manifest: manifest, StorageBackend: "local",
		StagingPrefix: "integration/olist", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create integration managed-data upload: %v", err)
	}
	if _, err := repository.CompleteUpload(ctx, manageddata.CompleteUploadInput{
		SessionID: session.ID,
		Files: []manageddata.StoredFile{{
			File: manifest.Files[0], StorageKey: "integration/fixture.csv",
		}},
	}); err != nil {
		t.Fatalf("complete integration managed-data upload: %v", err)
	}
}

type staticIntegrationManagedDataResolver struct {
	root string
}

func (r staticIntegrationManagedDataResolver) ResolveManagedData(context.Context, servingstate.ID) (runtimehost.ManagedDataResolution, error) {
	return runtimehost.ManagedDataResolution{
		RevisionID: integrationOlistManagedDataRevision,
		Roots:      map[string]string{"olist": r.root},
	}, nil
}

func registryLeasedSnapshots(registry *runtimehost.Registry) []int64 {
	if registry == nil {
		return nil
	}
	return registry.LeasedSnapshots()
}

func createAndActivateProjectDeployment(t *testing.T, ctx context.Context, repo *servingstatesqlite.Repository, artifactDir, projectPath, dataDir, duckDBDir, workspaceID, createdBy string) servingstate.State {
	t.Helper()
	created, err := repo.Create(ctx, servingstate.CreateInput{WorkspaceID: servingstate.WorkspaceID(workspaceID), CreatedBy: createdBy})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	artifactPath := filepath.Join(artifactDir, string(created.ID)+".tar.gz")
	file, err := os.Create(artifactPath)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, _, err := projectbundle.PackProject(projectPath, projectbundle.PackProjectOptions{
		WorkspaceID:          workspaceID,
		Environment:          string(servingstate.DefaultEnvironment),
		ServingStateID:       string(created.ID),
		ManagedDataRevisions: integrationOlistManagedDataRevisions(),
	}, file); err != nil {
		_ = file.Close()
		t.Fatalf("pack artifact: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close artifact: %v", err)
	}
	bindManagedConnectionRootsInArtifact(t, artifactPath, dataDir)
	validation, err := releasefilesystem.ValidateArtifactWithOptions(artifactPath, servingstate.WorkspaceID(workspaceID), created.ID, releasefilesystem.ValidateOptions{
		Environment: servingstate.DefaultEnvironment,
	})
	if err != nil {
		t.Fatalf("validate artifact: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(validation.RootDir) })
	info, err := os.Stat(artifactPath)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	saved, err := repo.SaveValidated(ctx, created.ID, validation, servingstate.Artifact{
		ID:             "artifact_" + string(created.ID),
		ServingStateID: created.ID,
		WorkspaceID:    servingstate.WorkspaceID(workspaceID),
		Environment:    servingstate.DefaultEnvironment,
		Digest:         validation.Digest,
		Format:         projectbundle.BundleFormat,
		Path:           artifactPath,
		ManifestJSON:   validation.ManifestJSON,
		SizeBytes:      info.Size(),
	})
	if err != nil {
		t.Fatalf("save validated deployment: %v", err)
	}
	active, err := repo.Activate(ctx, servingstate.WorkspaceID(workspaceID), servingstate.DefaultEnvironment, saved.ID)
	if err != nil {
		t.Fatalf("activate serving state: %v", err)
	}
	return active
}

type duckLakeIntegrationRuntimeFactory struct {
	database         *analyticsducklake.Environment
	managedRoot      string
	duckDBDir        string
	runtimeDir       string
	catalogPath      string
	duckLakeDataPath string
}

func (f duckLakeIntegrationRuntimeFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.Runtime, error) {
	targetDir := filepath.Join(f.runtimeDir, string(input.State.ID))
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	if err := projectbundle.ExtractArtifact(input.Artifact.Path, targetDir); err != nil {
		return nil, err
	}
	compiled, _, err := projectbundle.LoadCompiledWorkspaceArtifact(targetDir)
	if err != nil {
		return nil, err
	}
	if err := bindManagedConnectionRoots(compiled.Manifest, f.managedRoot); err != nil {
		return nil, err
	}
	dashboardDefinition := projectartifact.DashboardProjection(compiled.Manifest)
	service, err := dashboardruntime.NewFromDefinition(ctx, filepath.Join(f.duckDBDir, string(servingstate.NormalizeEnvironment(input.State.Environment))), duckLakeIntegrationDataRuntimeFactory{
		database:         f.database,
		snapshotID:       input.State.DuckLakeSnapshotID,
		catalogPath:      f.catalogPath,
		duckLakeDataPath: f.duckLakeDataPath,
		deploymentID:     string(input.State.ID),
		workspaceID:      string(input.State.WorkspaceID),
		environment:      string(servingstate.NormalizeEnvironment(input.State.Environment)),
		semanticDigest:   input.State.Digest,
		artifactDigest:   input.Artifact.Digest,
	}, dashboardDefinition)
	if err != nil {
		return nil, err
	}
	if input.State.DuckLakeSnapshotID == 0 {
		snapshotID := service.DuckLakeSnapshotID()
		if snapshotID > 0 {
			if err := service.Close(); err != nil {
				return nil, err
			}
			service, err = dashboardruntime.NewFromDefinition(ctx, filepath.Join(f.duckDBDir, string(servingstate.NormalizeEnvironment(input.State.Environment))), duckLakeIntegrationDataRuntimeFactory{
				database:         f.database,
				snapshotID:       snapshotID,
				catalogPath:      f.catalogPath,
				duckLakeDataPath: f.duckLakeDataPath,
				deploymentID:     string(input.State.ID),
				workspaceID:      string(input.State.WorkspaceID),
				environment:      string(servingstate.NormalizeEnvironment(input.State.Environment)),
				semanticDigest:   input.State.Digest,
				artifactDigest:   input.Artifact.Digest,
			}, dashboardDefinition)
			if err != nil {
				return nil, err
			}
		}
	}
	return service, nil
}

type duckLakeIntegrationDataRuntimeFactory struct {
	database         *analyticsducklake.Environment
	snapshotID       int64
	catalogPath      string
	duckLakeDataPath string
	deploymentID     string
	workspaceID      string
	environment      string
	semanticDigest   string
	artifactDigest   string
}

func (f duckLakeIntegrationDataRuntimeFactory) OpenDashboardWorkspaceDataRuntimes(ctx context.Context, config dashboardruntime.WorkspaceDataRuntimeConfig) (map[string]dashboardruntime.DataRuntime, error) {
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models:         config.Definition.Models,
		Database:       f.database,
		SnapshotID:     f.snapshotID,
		ServingStateID: f.deploymentID,
		WorkspaceID:    f.workspaceID,
		Environment:    f.environment,
		SemanticDigest: f.semanticDigest,
		ArtifactDigest: f.artifactDigest,
	})
	if err != nil {
		return nil, err
	}
	closer := &sharedDuckLakeRuntimeCloser{runtime: runtime}
	runtimes := make(map[string]dashboardruntime.DataRuntime, len(config.Definition.Models))
	for modelID := range config.Definition.Models {
		runtimes[modelID] = duckLakeIntegrationDataRuntime{
			modelID: modelID,
			runtime: runtime,
			close:   closer,
			data:    reportdef.NewDataQueryService(modelID, runtime),
		}
	}
	return runtimes, nil
}

type sharedDuckLakeRuntimeCloser struct {
	once    sync.Once
	runtime *analyticsduckdb.WorkspaceRuntime
	err     error
}

func (c *sharedDuckLakeRuntimeCloser) Close() error {
	if c == nil || c.runtime == nil {
		return nil
	}
	c.once.Do(func() {
		c.err = c.runtime.Close()
		c.runtime = nil
	})
	return c.err
}

type duckLakeIntegrationDataRuntime struct {
	modelID string
	runtime *analyticsduckdb.WorkspaceRuntime
	close   *sharedDuckLakeRuntimeCloser
	data    reportdef.DataService
}

func (r duckLakeIntegrationDataRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.data.Query(ctx, request)
}

func (r duckLakeIntegrationDataRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.data.Rows(ctx, request)
}

func (r duckLakeIntegrationDataRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.data.Count(ctx, request)
}

func (r duckLakeIntegrationDataRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, binCount int) ([]reportdef.HistogramBin, error) {
	return r.data.Histogram(ctx, request, binCount)
}

func (r duckLakeIntegrationDataRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.data.Distribution(ctx, request, sort, limit)
}

func (r duckLakeIntegrationDataRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return r.runtime.ExecuteDataQuery(ctx, request)
}

func (r duckLakeIntegrationDataRuntime) Refresh(ctx context.Context) error {
	return r.runtime.Refresh(ctx)
}

func (r duckLakeIntegrationDataRuntime) RefreshTables(ctx context.Context, tableNames []string) error {
	return r.runtime.RefreshModelTables(ctx, r.modelID, tableNames)
}

func (r duckLakeIntegrationDataRuntime) Close() error {
	return r.close.Close()
}

func (r duckLakeIntegrationDataRuntime) LastRefresh() time.Time {
	return r.runtime.LastRefresh()
}

func (r duckLakeIntegrationDataRuntime) DuckLakeSnapshotID() int64 {
	return r.runtime.DuckLakeSnapshotID()
}

func TestDuckLakeAtomicRefreshCutover(t *testing.T) {
	h := newDuckLakeHarness(t)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")

	initialRevenue := h.queryRevenue(t)
	if initialRevenue != 165 {
		t.Fatalf("initial revenue = %v, want 165", initialRevenue)
	}
	initialSnapshot := h.activeSnapshot(t)
	if initialSnapshot <= 0 {
		t.Fatalf("initial snapshot = %d, want positive", initialSnapshot)
	}

	writeMutatedOlistFixture(t, h.dataDir)
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		t.Fatalf("refresh status = %d, want %d", got, http.StatusNoContent)
	}
	run := h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	if run.ServingStateID == "" {
		t.Fatalf("run has no deployment id: %#v", run)
	}
	newRevenue := h.queryRevenue(t)
	if newRevenue != 265 {
		t.Fatalf("new revenue = %v, want 265", newRevenue)
	}
	newSnapshot := h.activeSnapshot(t)
	if newSnapshot <= initialSnapshot {
		t.Fatalf("new snapshot = %d, want greater than initial %d", newSnapshot, initialSnapshot)
	}
	fileCount, fileBytes, tableCount, snapshotCount, storedDataPath := h.duckLakeCatalogSummary(t)
	if tableCount == 0 || snapshotCount == 0 {
		t.Fatalf("DuckLake catalog has %d active tables / %d snapshots, want nonzero", tableCount, snapshotCount)
	}
	if fileCount == 0 {
		t.Logf("DuckLake catalog has no active data files for this tiny fixture; active tables=%d snapshots=%d bytes=%d", tableCount, snapshotCount, fileBytes)
	}
	if filepath.Clean(storedDataPath) != filepath.Clean(h.dataPath) {
		t.Fatalf("DuckLake metadata data_path = %q, want %q", storedDataPath, h.dataPath)
	}
}

func TestAdminStorageReflectsDuckLakeAfterCleanup(t *testing.T) {
	h := newDuckLakeHarness(t)
	legacyDir := filepath.Join(h.duckDBDir, "dev")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("create legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "leapview-stale.duckdb"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy duckdb file: %v", err)
	}
	body := h.getAuthenticatedHydrated(t, "/admin/storage")
	for _, want := range []string{"Storage", "model", "orders", "Total data size"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin storage missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "leapview-stale.duckdb") {
		t.Fatalf("admin storage exposed legacy duckdb artifact:\n%s", body)
	}
}

func TestGlobalReadExecutionAuditsQueueTelemetry(t *testing.T) {
	h := newDuckLakeHarness(t)
	req := h.authedJSONRequest(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", `{"measures":[{"field":"revenue"}],"limit":1}`)
	req.Header.Set("X-Request-ID", "integration-read-telemetry")
	res, body := h.do(t, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("query status = %d body=%s", res.StatusCode, body)
	}
	repo := queryauditsqlite.NewRepository(h.store.SQLDB())
	events, err := repo.ListQueryEvents(context.Background(), queryaudit.Filter{
		WorkspaceID: "sales",
		Search:      "integration-read-telemetry",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list query events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one query event", events)
	}
	event := events[0]
	if event.Surface != dataquery.SurfaceAPI || event.Operation != dataquery.OperationAPIQuery || event.ExecutionState != dataquery.ExecutionSucceeded || event.ExecutionMS <= 0 {
		t.Fatalf("query event telemetry = %#v", event)
	}
}

func TestInteractiveOverloadDoesNotBlockReservedRefresh(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 2, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1},
		workload.Refresh:     {ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 4, MaximumQueuedPerWorkspace: 1, QueueTimeout: time.Minute},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := newDuckLakeHarness(t, func(options *assemblyConfig) {
		options.Workload = controller
	})
	held, err := controller.Acquire(context.Background(), workload.Request{Class: workload.Interactive, WorkspaceID: "sales", Operation: "integration.hold"})
	if err != nil {
		t.Fatal(err)
	}
	req := h.authedJSONRequest(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", `{"measures":[{"field":"revenue"}],"limit":1}`)
	res, body := h.do(t, req)
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "WORKLOAD_OVERLOADED") || res.Header.Get("Retry-After") != "1" {
		held.Release()
		t.Fatalf("overloaded read status=%d body=%s retry=%q", res.StatusCode, body, res.Header.Get("Retry-After"))
	}
	writeMutatedOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		held.Release()
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	held.Release()
}

func TestDuckLakeCleanupProtectsLeasedSnapshots(t *testing.T) {
	h := newDuckLakeHarness(t)
	ctx := context.Background()
	initial := h.activeSnapshot(t)
	leaseID, err := h.deployments.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{
		WorkspaceID:        "sales",
		Environment:        servingstate.DefaultEnvironment,
		ServingStateID:     h.activeServingStateID(t),
		DuckLakeSnapshotID: initial,
		OwnerID:            "integration",
		ExpiresAt:          time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create query snapshot lease: %v", err)
	}
	writeMutatedOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	report, err := h.runStorageMaintenance(ctx, true)
	if err != nil {
		t.Fatalf("cleanup dry-run: %v", err)
	}
	if !containsSnapshot(report.LeaseProtectedSnapshots, initial) {
		t.Fatalf("leased snapshots = %#v, want %d", report.LeaseProtectedSnapshots, initial)
	}
	if err := h.deployments.ReleaseQuerySnapshotLease(ctx, leaseID); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	report, err = h.runStorageMaintenance(ctx, false)
	if err != nil {
		t.Fatalf("cleanup apply: %v", err)
	}
	if containsSnapshot(report.ProtectedSnapshots, initial) {
		t.Fatalf("old snapshot %d still protected after lease release: %#v", initial, report)
	}
}

func TestDuckLakeSnapshotProtectedByRunningQueryLease(t *testing.T) {
	h := newDuckLakeHarness(t)
	ctx := context.Background()
	provider := h.registry.ProviderForWorkspace("sales")
	lease, err := provider.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire runtime lease: %v", err)
	}
	initial := lease.DuckLakeSnapshotID()
	if initial <= 0 {
		t.Fatalf("lease snapshot = %d, want positive", initial)
	}
	writeMutatedOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		lease.Release()
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	if got := h.activeSnapshot(t); got <= initial {
		lease.Release()
		t.Fatalf("active snapshot = %d, want newer than leased snapshot %d", got, initial)
	}
	report, err := h.runStorageMaintenance(ctx, true)
	if err != nil {
		lease.Release()
		t.Fatalf("cleanup dry-run: %v", err)
	}
	if !containsSnapshot(report.LeaseProtectedSnapshots, initial) {
		lease.Release()
		t.Fatalf("lease-protected snapshots = %#v, want %d", report.LeaseProtectedSnapshots, initial)
	}
	lease.Release()
	report, err = h.runStorageMaintenance(ctx, false)
	if err != nil {
		t.Fatalf("cleanup apply after lease release: %v", err)
	}
	if containsSnapshot(report.ProtectedSnapshots, initial) {
		t.Fatalf("old snapshot %d stayed protected after final lease release: %#v", initial, report)
	}
}

func TestDuckLakeCleanupRemovesUnleasedStaleSnapshots(t *testing.T) {
	h := newDuckLakeHarness(t)
	ctx := context.Background()
	provider := h.registry.ProviderForWorkspace("sales")
	lease, err := provider.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire runtime lease: %v", err)
	}
	initial := lease.DuckLakeSnapshotID()
	writeMutatedOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		lease.Release()
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	if !containsSnapshot(h.duckLakeSnapshotIDs(t), initial) {
		lease.Release()
		t.Fatalf("snapshot %d disappeared while lease was still held", initial)
	}
	lease.Release()
	report, err := h.runStorageMaintenance(ctx, false)
	if err != nil {
		t.Fatalf("cleanup apply: %v", err)
	}
	if containsSnapshot(report.ProtectedSnapshots, initial) {
		t.Fatalf("snapshot %d still protected after lease release: %#v", initial, report)
	}
	if containsSnapshot(h.duckLakeSnapshotIDs(t), initial) {
		t.Fatalf("snapshot %d still exists after cleanup", initial)
	}
}

func TestDuckLakeCleanupPreservesForeignEnvironmentSnapshots(t *testing.T) {
	h := newDuckLakeHarness(t)
	ctx := context.Background()
	initial := h.activeSnapshot(t)
	foreign, err := h.deployments.Create(ctx, servingstate.CreateInput{WorkspaceID: "sales", ProjectID: "historical", Environment: "prod", CreatedBy: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.deployments.RecordDuckLakeSnapshot(ctx, foreign.ID, initial); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.SQLDB().ExecContext(ctx, "UPDATE serving_states SET status = ? WHERE id = ?", string(servingstate.StatusInactive), string(foreign.ID)); err != nil {
		t.Fatal(err)
	}
	writeMutatedOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusSucceeded)
	report, err := h.runStorageMaintenance(ctx, false)
	if err != nil {
		t.Fatalf("cleanup apply: %v", err)
	}
	if !containsSnapshot(report.ForeignProtectedSnapshots, initial) || !containsSnapshot(h.duckLakeSnapshotIDs(t), initial) {
		t.Fatalf("foreign snapshot %d was not preserved: %#v", initial, report)
	}
}

func TestFailedRefreshLeavesActiveSnapshotQueryable(t *testing.T) {
	h := newDuckLakeHarness(t)
	initialRevenue := h.queryRevenue(t)
	initialSnapshot := h.activeSnapshot(t)
	writeBrokenOlistFixture(t, h.dataDir)
	pipelineAssetID := integrationAssetID(t, h.store, "sales", "refresh_pipeline", "sales.sales-refresh")
	if got := h.postAuthenticated(t, "/workspaces/sales/assets/"+pipelineAssetID+"/refresh"); got != http.StatusNoContent {
		t.Fatalf("refresh status = %d", got)
	}
	h.waitLatestRun(t, refreshrun.TargetRefreshPipeline, "sales.sales-refresh", refreshrun.RunStatusFailed)
	if got := h.activeSnapshot(t); got != initialSnapshot {
		t.Fatalf("active snapshot = %d after failed refresh, want %d", got, initialSnapshot)
	}
	if got := h.queryRevenue(t); got != initialRevenue {
		t.Fatalf("revenue after failed refresh = %v, want previous %v", got, initialRevenue)
	}
}

func TestDurableRefreshQueueResumesAfterStartup(t *testing.T) {
	h := newDuckLakeHarness(t)
	run := h.createQueuedRefreshPipelineRun(t)
	h.stopBackgroundJobs(t)
	h.registry.Close()
	h.registry = nil
	h.startReplacementRegistry(t)
	stored := h.waitRun(t, run.ID, refreshrun.RunStatusSucceeded)
	if stored.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("stored run = %#v", stored)
	}
}

func TestExpiredRefreshJobLeaseIsReclaimed(t *testing.T) {
	h := newDuckLakeHarness(t)
	ctx := context.Background()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(h.store.SQLDB())
	run := h.createQueuedRefreshPipelineRun(t)
	job, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "stale-worker", time.Second)
	if err != nil || !ok {
		t.Fatalf("claim job ok=%v err=%v", ok, err)
	}
	if _, err := h.store.SQLDB().ExecContext(ctx, `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
		t.Fatalf("expire job lease: %v", err)
	}
	h.startReplacementRegistry(t)
	stored := h.waitRun(t, run.ID, refreshrun.RunStatusSucceeded)
	if stored.Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("stored run = %#v", stored)
	}
}

func (h *duckLakeHarness) startReplacementRegistry(t *testing.T) {
	t.Helper()
	h.stopBackgroundJobs(t)
	registry := runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{
		Repo:         h.deployments,
		WorkspaceIDs: []servingstate.WorkspaceID{"sales"},
		Environment:  servingstate.DefaultEnvironment,
		Factory: duckLakeIntegrationRuntimeFactory{
			database:         h.database,
			managedRoot:      h.dataDir,
			duckDBDir:        h.duckDBDir,
			runtimeDir:       h.runtimeDir,
			catalogPath:      h.catalogPath,
			duckLakeDataPath: h.dataPath,
		},
	})
	if err := registry.Reload(context.Background()); err != nil {
		t.Fatalf("reload replacement registry: %v", err)
	}
	h.registry = registry
	server := assembleRuntime(NewDynamicRuntimeMetrics(func(workspaceID string) runtimehost.Provider {
		return registry.ProviderForWorkspace(servingstate.WorkspaceID(workspaceID))
	}), testStoreOptions(h.store, assemblyConfig{
		ServingStateRepo: h.deployments,
		WorkspaceRepo:    workspacesqlite.NewRepository(h.store.SQLDB()),
		AssetCatalog:     workspace.NewAssetCatalogService(workspacesqlite.NewRepository(h.store.SQLDB())),
		Auth:             NewAuth(accesssqlite.NewRepository(h.store.SQLDB()), AuthConfig{DevBypass: true}),
		Reloader:         registry,

		DuckLakeCatalogPath: h.catalogPath,
		DuckLakeDataPath:    h.dataPath,
		AnalyticsModule:     analyticsmodule.NewSurface(h.database, nil),
		WorkspaceID:         "sales",
		DefaultEnvironment:  string(servingstate.DefaultEnvironment),
	}))
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	server.StartBackgroundJobs(backgroundCtx)
	h.appServer = server
	h.handler = server.Routes()
	if h.server != nil {
		h.server.Close()
	}
	h.server = httptest.NewServer(h.handler)
	t.Cleanup(h.server.Close)
	t.Cleanup(func() { _ = registry.Close() })
	t.Cleanup(func() {
		stopBackground()
		stopServerBackgroundForTest(t, server)
	})
}

func (h *duckLakeHarness) stopBackgroundJobs(t *testing.T) {
	t.Helper()
	if h == nil || h.appServer == nil {
		return
	}
	stopServerBackgroundForTest(t, h.appServer)
	h.appServer = nil
}

func stopServerBackgroundForTest(t *testing.T, server *appTestHarness) {
	t.Helper()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.StopBackgroundJobs(ctx); err != nil {
		t.Errorf("stop background jobs: %v", err)
	}
}

func (h *duckLakeHarness) createQueuedRefreshPipelineRun(t *testing.T) refreshrun.RunRecord {
	t.Helper()
	ctx := context.Background()
	active, artifact, err := h.deployments.ActiveArtifact(ctx, "sales", servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("active artifact for refresh candidate: %v", err)
	}
	root := t.TempDir()
	if err := projectbundle.ExtractArtifact(artifact.Path, root); err != nil {
		t.Fatalf("extract active artifact: %v", err)
	}
	compiled, _, err := projectbundle.LoadCompiledWorkspaceArtifact(root)
	if err != nil {
		t.Fatalf("load active compiled artifact: %v", err)
	}
	created, err := h.deployments.Create(ctx, servingstate.CreateInput{
		WorkspaceID: active.WorkspaceID,
		Environment: active.Environment,
		CreatedBy:   "integration",
		Source:      servingstate.SourceRefresh,
	})
	if err != nil {
		t.Fatalf("create refresh candidate deployment: %v", err)
	}
	candidateArtifact := servingstate.Artifact{
		ID:             "artifact_" + string(created.ID),
		ServingStateID: created.ID,
		WorkspaceID:    active.WorkspaceID,
		Environment:    active.Environment,
		Digest:         artifact.Digest,
		Format:         artifact.Format,
		Path:           artifact.Path,
		ManifestJSON:   artifact.ManifestJSON,
		SizeBytes:      artifact.SizeBytes,
	}
	var accessPolicy workspace.AccessPolicy
	if err := json.Unmarshal([]byte(active.AccessPolicyJSON), &accessPolicy); err != nil {
		t.Fatalf("decode active access policy: %v", err)
	}
	if _, err := h.deployments.SaveValidated(ctx, created.ID, servingstate.Validation{
		Digest:            active.Digest,
		ManifestJSON:      active.ManifestJSON,
		ProjectID:         active.ProjectID,
		ProjectDigest:     active.ProjectDigest,
		ProjectWorkspaces: append([]string(nil), active.ProjectWorkspaces...),
		AccessPolicy:      testSnapshotAccessPolicy(t, accessPolicy),
		Graph:             testSnapshotGraph(t, integrationRetargetAssetGraph(compiled.Graph, workspace.WorkspaceID(active.WorkspaceID), workspace.ServingStateID(created.ID))),
	}, candidateArtifact); err != nil {
		t.Fatalf("save refresh candidate deployment: %v", err)
	}
	repo := analyticsmaterializesqlite.NewSQLRunRepository(h.store.SQLDB())
	run, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "sales",
		ModelID:        "sales",
		ServingStateID: string(created.ID),
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "sales.sales-refresh",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
		PayloadJSON:    `{"pipelineId":"sales-refresh"}`,
	})
	if err != nil {
		t.Fatalf("create queued workspace asset refresh run: %v", err)
	}
	return run
}

func integrationRetargetAssetGraph(graph workspace.AssetGraph, workspaceID workspace.WorkspaceID, deploymentID workspace.ServingStateID) workspace.AssetGraph {
	out := workspace.AssetGraph{
		Assets: make([]workspace.Asset, 0, len(graph.Assets)),
		Edges:  make([]workspace.AssetEdge, 0, len(graph.Edges)),
	}
	for _, asset := range graph.Assets {
		asset.WorkspaceID = workspaceID
		asset.ServingStateID = deploymentID
		asset.SnapshotID = workspace.NewAssetSnapshotID(deploymentID, asset.ID)
		out.Assets = append(out.Assets, asset)
	}
	for _, edge := range graph.Edges {
		edge.WorkspaceID = workspaceID
		edge.ServingStateID = deploymentID
		edge.ID = workspace.NewAssetEdgeID(deploymentID, edge.FromAssetID, edge.ToAssetID, edge.Type)
		out.Edges = append(out.Edges, edge)
	}
	return out
}

func (h *duckLakeHarness) authedJSONRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, h.serverURL(t)+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (h *duckLakeHarness) do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func (h *duckLakeHarness) queryRevenue(t *testing.T) float64 {
	t.Helper()
	req := h.authedJSONRequest(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", `{"measures":[{"field":"revenue"}],"limit":1}`)
	res, body := h.do(t, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("semantic query status=%d body=%s", res.StatusCode, body)
	}
	var decoded api.SemanticQueryResponse
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode semantic query: %v body=%s", err, body)
	}
	if len(decoded.Rows) != 1 || len(decoded.Columns) != 1 || len(decoded.Rows[0]) != 1 {
		t.Fatalf("semantic query rowset = %#v, want one cell", decoded)
	}
	cell, ok := decoded.Rows[0][0].(string)
	if !ok {
		t.Fatalf("semantic revenue cell = %T %#v, want string", decoded.Rows[0][0], decoded.Rows[0][0])
	}
	value, err := strconv.ParseFloat(cell, 64)
	if err != nil {
		t.Fatalf("parse semantic revenue %q: %v", cell, err)
	}
	return value
}

func integrationNumberValue(t *testing.T, value any) float64 {
	t.Helper()
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	default:
		t.Fatalf("value %T %#v is not numeric", value, value)
	}
	return 0
}

func (h *duckLakeHarness) activeSnapshot(t *testing.T) int64 {
	t.Helper()
	active, _, err := h.deployments.ActiveArtifact(context.Background(), "sales", servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("active artifact: %v", err)
	}
	return active.DuckLakeSnapshotID
}

func (h *duckLakeHarness) activeServingStateID(t *testing.T) servingstate.ID {
	t.Helper()
	active, _, err := h.deployments.ActiveArtifact(context.Background(), "sales", servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("active artifact: %v", err)
	}
	return active.ID
}

func (h *duckLakeHarness) waitLatestRun(t *testing.T, targetType, targetID, status string) refreshrun.RunRecord {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	repo := analyticsmaterializesqlite.NewSQLRunRepository(h.store.SQLDB())
	lastStatus := "missing"
	lastError := ""
	for time.Now().Before(deadline) {
		runs, err := repo.ListTargetRuns(context.Background(), "sales", targetType, targetID, refreshrun.RunPage{Limit: 1})
		if err != nil {
			t.Fatalf("list target runs: %v", err)
		}
		if len(runs) > 0 {
			lastStatus = runs[0].Status
			lastError = runs[0].Error
			if runs[0].Status == status {
				return runs[0]
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s %s run status %s; last status=%s error=%q", targetType, targetID, status, lastStatus, lastError)
	return refreshrun.RunRecord{}
}

func (h *duckLakeHarness) waitRun(t *testing.T, runID, status string) refreshrun.RunRecord {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	repo := analyticsmaterializesqlite.NewSQLRunRepository(h.store.SQLDB())
	lastStatus := "missing"
	lastError := ""
	for time.Now().Before(deadline) {
		run, err := repo.GetRun(context.Background(), "sales", runID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		lastStatus = run.Status
		lastError = run.Error
		if run.Status == status {
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run %s status %s; last status=%s error=%q", runID, status, lastStatus, lastError)
	return refreshrun.RunRecord{}
}

func writeMutatedOlistFixture(t *testing.T, dir string) {
	t.Helper()
	writeIntegrationMinimalOlistFixture(t, dir)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,210.00
o2,1,boleto,1,55.00
`)
}

func writeBrokenOlistFixture(t *testing.T, dir string) {
	t.Helper()
	writeIntegrationMinimalOlistFixture(t, dir)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments
o1,1,credit_card,1
o2,1,boleto,1
`)
}

func (h *duckLakeHarness) duckLakeCatalogSummary(t *testing.T) (int64, int64, int64, int64, string) {
	t.Helper()
	db := h.openDuckLakeMetadata(t)
	defer db.Close()
	var dataPath string
	if err := db.QueryRow(`SELECT value FROM __ducklake_metadata_lake.ducklake_metadata WHERE "key" = 'data_path' AND scope IS NULL LIMIT 1`).Scan(&dataPath); err != nil {
		t.Fatalf("query DuckLake data path metadata: %v", err)
	}
	var files, bytes int64
	if err := db.QueryRow(`SELECT count(*), coalesce(sum(file_size_bytes), 0) FROM __ducklake_metadata_lake.ducklake_data_file WHERE end_snapshot IS NULL`).Scan(&files, &bytes); err != nil {
		t.Fatalf("query DuckLake data files: %v", err)
	}
	var tables, snapshots int64
	if err := db.QueryRow(`SELECT count(*) FROM __ducklake_metadata_lake.ducklake_table WHERE end_snapshot IS NULL`).Scan(&tables); err != nil {
		t.Fatalf("query DuckLake tables: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM __ducklake_metadata_lake.ducklake_snapshot`).Scan(&snapshots); err != nil {
		t.Fatalf("query DuckLake snapshots: %v", err)
	}
	return files, bytes, tables, snapshots, dataPath
}

func (h *duckLakeHarness) duckLakeSnapshotIDs(t *testing.T) []int64 {
	t.Helper()
	db := h.openDuckLakeMetadata(t)
	defer db.Close()
	rows, err := db.Query(`SELECT snapshot_id FROM __ducklake_metadata_lake.ducklake_snapshot ORDER BY snapshot_id`)
	if err != nil {
		t.Fatalf("query DuckLake snapshots: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan DuckLake snapshot: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate DuckLake snapshots: %v", err)
	}
	return ids
}

func (h *duckLakeHarness) openDuckLakeMetadata(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB metadata connection: %v", err)
	}
	for _, stmt := range []string{
		"LOAD ducklake",
		fmt.Sprintf("ATTACH 'ducklake:%s' AS lake (DATA_PATH '%s')", integrationSQLString(h.catalogPath), integrationSQLString(h.dataPath)),
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			_ = db.Close()
			t.Fatalf("prepare DuckLake metadata inspection %q: %v", stmt, err)
		}
	}
	return db
}

func integrationSQLString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func containsSnapshot(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
