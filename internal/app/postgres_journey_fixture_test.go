package app

// This file is the reusable PostgreSQL-native route-qualification fixture.
// It intentionally lives in the app package so journey tests can exercise the
// same dataAssemblyInputs boundary as production without selecting a local
// database implementation. Optional module wiring is kept behind options so a
// route track can qualify only the authorities it needs:
//   - access/jobs/agent are assembled by default;
//   - dashboard authoring/publication and refresh are enabled together with
//     NativeDashboard;
//   - RuntimeHost and AcquireRuntime are optional seams for deterministic
//     serving-generation tests.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardpublication "github.com/flidai/leapview/internal/dashboard/publication"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	postgresJourneyTargetID = "journey_target"
	postgresJourneyProject  = projectgraph.ResourceID("project:journey")
)

// PostgresJourneyFixtureOptions controls optional route-track assembly. The
// zero value assembles native access, jobs, and agent surfaces, and wires the
// graph's durable API protocol authorities. NativeDashboard enables the full
// dashboard authoring/publication path and its native refresh persistence.
type PostgresJourneyFixtureOptions struct {
	TargetID  string
	ProjectID projectgraph.ResourceID

	// SkipRouteAssembly leaves the graph and native capability handles
	// available without constructing HTTP routes. The default assembles routes.
	SkipRouteAssembly bool
	// NativeDashboard enables the complete graph dashboard persistence bundle,
	// authoring application, publication reconciler, and native refresh store.
	NativeDashboard bool

	// RuntimeHost is an optional deterministic serving-generation module. When
	// supplied, its provider is also used by dashboard authoring.
	RuntimeHost *runtimehostmodule.Module
	// AcquireRuntime is the authoring runtime seam for tests that provide a
	// deterministic lease without constructing a runtimehost module.
	AcquireRuntime func(context.Context) (runtimehostmodule.Lease, error)
	// ServingSnapshotResolver supplies the cursor snapshot callback used by
	// assembled routes. A stable callback is useful for deterministic tracks.
	ServingSnapshotResolver func(context.Context) (string, error)
}

// PostgresJourneyFixture owns one disposable PostgreSQL database, bounded
// native pools, the validated authority graph, and optionally assembled app
// surfaces. All application-owned resources are cleaned before the database
// cleanup registered by postgrestest.NewDatabase.
type PostgresJourneyFixture struct {
	Harness         *postgrestest.Harness
	Database        *postgrestest.Database
	RuntimePool     *platformpostgres.Pool
	MaintenancePool *platformpostgres.Pool
	Graph           *postgresauthority.PostgresAuthorityGraph

	AccessPersistence              accessmodule.Persistence
	AccessModule                   *accessmodule.Module
	JobsPersistence                jobsmodule.Persistence
	JobsModule                     *jobsmodule.Module
	Workload                       *workloadmodule.Module
	DashboardAuthoring             *dashboardmodule.AuthoringApplication
	DashboardPublicationReconciler *NativeDashboardPublicationReconciler
	RefreshPersistence             *refreshmodule.Persistence

	// Handler is non-nil when route assembly is enabled and composition
	// succeeds. Route tests should use Request/WithPrincipal below so request
	// identity remains explicit and does not depend on ambient auth state.
	Handler  http.Handler
	routes   *capabilityRoutes
	runtime  *runtimeServices
	platform *platformServices
	policy   *httpPolicy
}

// NewPostgresJourneyFixture starts the pinned PostgreSQL conformance harness,
// applies the native baseline, opens runtime/maintenance pools, validates the
// authority graph, and assembles the requested app surfaces.
func NewPostgresJourneyFixture(t *testing.T, options PostgresJourneyFixtureOptions) *PostgresJourneyFixture {
	t.Helper()
	if strings.TrimSpace(options.TargetID) == "" {
		options.TargetID = postgresJourneyTargetID
	}
	if options.ProjectID == "" {
		options.ProjectID = postgresJourneyProject
	}
	// Keep this literal: postgres-conformance-tests.sh inventories the app
	// package by looking for the normal-shard skip-aware harness entrypoint.
	h := postgrestest.Start(t)
	roles := provisionPostgresJourneyRoles(t, h)
	h.GrantRole(t, roles.owner, roles.migrator)
	database := h.NewDatabase(t, "")
	for _, role := range []postgrestest.Role{roles.owner, roles.migrator, roles.runtime, roles.maintenance, roles.readonly, roles.backup} {
		privileges := []string{"CONNECT"}
		if role.Name == roles.owner.Name || role.Name == roles.migrator.Name {
			privileges = append(privileges, "CREATE")
		}
		h.GrantDatabase(t, database.Name, role, privileges...)
	}
	applyPostgresJourneyBaseline(t, database, roles)

	runtimePool := openPostgresJourneyPool(t, database, roles.runtime, 4)
	maintenancePool := openPostgresJourneyPool(t, database, roles.maintenance, 1)
	graph, err := postgresauthority.NewPostgresAuthorityGraph(runtimePool, maintenancePool, postgresauthority.PostgresAuthorityGraphOptions{
		TargetID: options.TargetID, FingerprintKey: []byte(strings.Repeat("journey-fingerprint", 2)),
	})
	if err != nil {
		t.Fatalf("construct PostgreSQL journey authority graph: %v", err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("validate PostgreSQL journey authority graph: %v", err)
	}

	fixture := &PostgresJourneyFixture{Harness: h, Database: database, RuntimePool: runtimePool, MaintenancePool: maintenancePool, Graph: graph}
	fixture.buildCapabilities(t, options)
	if !options.SkipRouteAssembly {
		fixture.assembleRoutes(t, options)
	}
	return fixture
}

// NewPostgresJourneyFixtureDefault is a concise zero-options constructor for
// route tracks that want the canonical target/project defaults.
func NewPostgresJourneyFixtureDefault(t *testing.T) *PostgresJourneyFixture {
	t.Helper()
	return NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{})
}

type postgresJourneyRoles struct {
	owner, migrator, runtime, maintenance, readonly, backup postgrestest.Role
}

func provisionPostgresJourneyRoles(t *testing.T, h *postgrestest.Harness) postgresJourneyRoles {
	t.Helper()
	return postgresJourneyRoles{
		owner:       h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"}),
		migrator:    h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "journey-migrator", Login: true}),
		runtime:     h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "journey-runtime", Login: true}),
		maintenance: h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "journey-maintenance", Login: true}),
		readonly:    h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "journey-readonly", Login: true}),
		backup:      h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Password: "journey-backup", Login: true}),
	}
}

func applyPostgresJourneyBaseline(t *testing.T, database *postgrestest.Database, roles postgresJourneyRoles) {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatalf("open PostgreSQL journey administrator: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("grant PostgreSQL Goose version-table authority: %v", err)
	}
	if err := admin.Close(t.Context()); err != nil {
		t.Fatalf("close PostgreSQL journey administrator: %v", err)
	}
	db, err := sql.Open("pgx", database.URL(roles.migrator))
	if err != nil {
		t.Fatalf("open PostgreSQL journey baseline administrator: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	riverPool, err := pgxpool.New(t.Context(), database.URL(roles.migrator))
	if err != nil {
		t.Fatalf("open PostgreSQL journey River migrator: %v", err)
	}
	t.Cleanup(riverPool.Close)
	if err := platformmigrations.ApplyRiver(t.Context(), riverPool); err != nil {
		t.Fatalf("apply PostgreSQL journey River schema: %v", err)
	}
	if err := postgresbaseline.Apply(t.Context(), db); err != nil {
		t.Fatalf("apply PostgreSQL journey baseline: %v", err)
	}
}

func openPostgresJourneyPool(t *testing.T, database *postgrestest.Database, role postgrestest.Role, maxConns int32) *platformpostgres.Pool {
	t.Helper()
	pool, err := platformpostgres.Open(t.Context(), platformpostgres.Config{
		URL: database.URL(role), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: role.Name, Intent: platformpostgres.IntentReadWrite,
		MinConns: 1, MaxConns: maxConns, AcquireTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open PostgreSQL journey %s pool: %v", role.Name, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (f *PostgresJourneyFixture) buildCapabilities(t *testing.T, options PostgresJourneyFixtureOptions) {
	t.Helper()
	accessPersistence, err := accessmodule.NewPostgresPersistence(f.Graph.Access, nil)
	if err != nil {
		t.Fatalf("build PostgreSQL journey access persistence: %v", err)
	}
	f.AccessPersistence = accessPersistence
	accessConfig := accessmodule.Config{
		Persistence: &f.AccessPersistence, Production: true,
		Auth:      accessmodule.AuthConfig{Disabled: true, CSRFKey: strings.Repeat("journey-csrf", 4)},
		PublicURL: "http://localhost", InstanceID: options.TargetID,
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) { return options.ProjectID, nil },
	}
	f.AccessModule, err = accessmodule.Build(t.Context(), accessConfig)
	if err != nil {
		t.Fatalf("build PostgreSQL journey access module: %v", err)
	}

	f.Workload, err = workloadmodule.Build(t.Context(), workloadmodule.Config{Policy: workloadmodule.DefaultConfig()})
	if err != nil {
		t.Fatalf("build PostgreSQL journey workload admission: %v", err)
	}
	t.Cleanup(f.Workload.Close)
	f.JobsPersistence, err = jobsmodule.NewPostgresPersistence(f.Graph.Jobs)
	if err != nil {
		t.Fatalf("build PostgreSQL journey jobs persistence: %v", err)
	}
	f.JobsModule, err = jobsmodule.Build(t.Context(), jobsmodule.Config{
		Persistence: &f.JobsPersistence, Production: true,
		Admission: workloadmodule.JobAdmitter(f.Workload), LeaseTimeout: 2 * time.Minute,
		OwnerID: "postgres-journey-fixture",
	})
	if err != nil {
		t.Fatalf("build PostgreSQL journey jobs module: %v", err)
	}
	t.Cleanup(func() { _ = f.JobsModule.Stop(context.Background()) })
}

func (f *PostgresJourneyFixture) assembleRoutes(t *testing.T, options PostgresJourneyFixtureOptions) {
	t.Helper()
	data := dataAssemblyInputs{
		PlatformHealth: f.RuntimePool, ServingStateRepo: f.Graph.ServingState,
		AccessRepo: f.Graph.Access, APIIdempotency: f.Graph.Idempotency,
		CursorSigning:              f.Graph.CursorSigning,
		RequireExplicitAPIProtocol: true,
	}
	capabilities := capabilityAssemblyInputs{
		JobModule: f.JobsModule, AccessModule: f.AccessModule,
		AgentPersistence: f.Graph.AgentPersistence,
	}
	workflow := workflowAssemblyInputs{Workload: f.Workload, AgentSettings: f.Graph.Bootstrap}
	runtimeConfig := runtimeAssemblyInputs{
		RuntimeHost: options.RuntimeHost, ProjectID: options.ProjectID,
		ProjectIDResolver:       func(context.Context) (projectgraph.ResourceID, error) { return options.ProjectID, nil },
		ServingSnapshotResolver: options.ServingSnapshotResolver,
		InstanceID:              options.TargetID, DefaultEnvironment: "prod", AllowDevAuthBypass: true,
	}
	if runtimeConfig.ServingSnapshotResolver == nil {
		runtimeConfig.ServingSnapshotResolver = func(context.Context) (string, error) {
			return "0198f2c0-7c7a-7f00-8a11-000000000001", nil
		}
	}
	if options.NativeDashboard {
		f.assembleNativeDashboard(t, options)
		data.DashboardPublicationReconciler = f.DashboardPublicationReconciler
		data.DashboardPersistence = f.Graph.DashboardPersistence
		data.RefreshPersistence = f.RefreshPersistence
		data.RequireNativeDashboard = true
		capabilities.Authoring = f.DashboardAuthoring
	}
	routes, runtime, platform, policy, err := buildApplicationSurfaces(t.Context(), nil, data, capabilities, workflow, runtimeConfig, httpAssemblyInputs{PublicURL: "http://localhost"})
	if err != nil {
		t.Fatalf("assemble PostgreSQL journey application surfaces: %v", err)
	}
	f.routes, f.runtime, f.platform, f.policy = routes, runtime, platform, policy
	f.Handler = Routes(routes, runtime, platform, policy)
}

func (f *PostgresJourneyFixture) assembleNativeDashboard(t *testing.T, options PostgresJourneyFixtureOptions) {
	t.Helper()
	acquire := options.AcquireRuntime
	if acquire == nil && options.RuntimeHost != nil {
		acquire = options.RuntimeHost.Provider().Acquire
	}
	if acquire == nil {
		acquire = func(context.Context) (runtimehostmodule.Lease, error) {
			return nil, errors.New("PostgreSQL journey runtime lease is not configured")
		}
	}
	authoring, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{
		Persistence: f.Graph.DashboardPersistence,
		AuthorizeResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
		AuthorizeProjectCapability: func(context.Context, string, projectgraph.ResourceID, access.Capability) (bool, error) {
			return true, nil
		},
		AcquireRuntime: acquire,
	})
	if err != nil {
		t.Fatalf("build PostgreSQL journey dashboard authoring: %v", err)
	}
	f.DashboardAuthoring = authoring
	identityResolver, err := apprefreshpostgres.NewPostgresPublicationIdentityResolverAdapter(f.Graph.DeploymentRepository, options.TargetID)
	if err != nil {
		t.Fatalf("build PostgreSQL journey refresh identity resolver: %v", err)
	}
	canonicalVerifier, err := apprefreshpostgres.NewPostgresCanonicalVerifierAdapter(f.Graph.DeploymentRepository, options.TargetID)
	if err != nil {
		t.Fatalf("build PostgreSQL journey refresh verifier: %v", err)
	}
	finalizer, err := apprefreshpostgres.NewPostgresNativeRefreshFinalizer(f.Graph.Refresh, f.Graph.DeploymentRepository, options.TargetID)
	if err != nil {
		t.Fatalf("build PostgreSQL journey refresh finalizer: %v", err)
	}
	operations, err := apprefreshpostgres.NewPostgresOperationAuthorityAdapter(f.Graph.Operation)
	if err != nil {
		t.Fatalf("build PostgreSQL journey refresh operation authority: %v", err)
	}
	persistence, err := refreshmodule.NewPostgresPersistence(f.Graph.Refresh, refreshmodule.PostgresPersistenceConfig{
		SchedulerOwner: "postgres-journey-fixture", PublicationIdentityResolver: identityResolver,
		Jobs: f.Graph.RefreshJobs, CanonicalVerifier: canonicalVerifier, NativeFinalizer: finalizer,
		Operations: operations, CancelAuditWriter: f.Graph.RefreshCancelAudit, CreateAuditWriter: f.Graph.RefreshCancelAudit,
	})
	if err != nil {
		t.Fatalf("build PostgreSQL journey refresh persistence: %v", err)
	}
	f.RefreshPersistence = &persistence
	reconciler, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: f.RuntimePool, Publications: f.Graph.DashboardPublication, Project: f.Graph.Project,
		Access: f.AccessModule, GenerationFence: f.Graph.DashboardGenerationFence,
	})
	if err != nil {
		t.Fatalf("build PostgreSQL journey dashboard publication reconciler: %v", err)
	}
	f.DashboardPublicationReconciler = reconciler
}

// Request creates a route request with an explicit host and no ambient auth.
// Call WithPrincipal or WithAPICredential before dispatching protected routes.
func (f *PostgresJourneyFixture) Request(ctx context.Context, method, path string, body io.Reader) *http.Request {
	request := httptest.NewRequestWithContext(ctx, method, path, body)
	request.Host = "localhost"
	return request
}

// WithPrincipal attaches a native journey principal to a request context.
func (f *PostgresJourneyFixture) WithPrincipal(request *http.Request, principal accessmodule.Principal) *http.Request {
	if request == nil {
		return nil
	}
	return request.WithContext(accessmodule.WithPrincipal(request.Context(), principal))
}

// WithAPICredential attaches an explicitly seeded API credential to a request
// context. This is useful for routes whose authorization is credential-scoped
// rather than session-scoped.
func (f *PostgresJourneyFixture) WithAPICredential(request *http.Request, credential access.APICredential) *http.Request {
	if request == nil {
		return nil
	}
	return request.WithContext(accessmodule.WithAPICredential(request.Context(), credential))
}

// SeedPrincipal inserts or updates a principal through the native PostgreSQL
// access authority and returns the durable identity for request seeding.
func (f *PostgresJourneyFixture) SeedPrincipal(ctx context.Context, input access.PrincipalInput) (access.Principal, error) {
	if f == nil || f.Graph == nil || f.Graph.Access == nil {
		return access.Principal{}, errors.New("PostgreSQL journey access authority is unavailable")
	}
	return f.Graph.Access.UpsertPrincipal(ctx, input)
}

// SeedNativePublicationTx runs the native dashboard publication projection on
// a caller-owned transaction. The activatePrincipal callback should be the
// graph's access activator for tracks that seed a real published principal.
func (f *PostgresJourneyFixture) SeedNativePublicationTx(ctx context.Context, tx pgx.Tx, input dashboardpublication.ReconcileInput, activatePrincipal func(context.Context, dashboardpublicationpostgres.Tx, projectgraph.ResourceID, string) error) error {
	if f == nil || f.Graph == nil || f.Graph.DashboardPublication == nil {
		return errors.New("PostgreSQL journey dashboard publication authority is unavailable")
	}
	return f.Graph.DashboardPublication.ReconcileTx(ctx, tx, input, activatePrincipal)
}

// SeedNativePublication applies one dashboard publication projection with a
// fixture-owned transaction and the graph's native Access activator. The
// helper commits only after the complete projection succeeds.
func (f *PostgresJourneyFixture) SeedNativePublication(ctx context.Context, input dashboardpublication.ReconcileInput) error {
	if f == nil || f.Graph == nil || f.RuntimePool == nil || f.Graph.DashboardPublication == nil || f.AccessModule == nil {
		return errors.New("PostgreSQL journey dashboard publication fixture is unavailable")
	}
	tx, err := f.RuntimePool.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := f.SeedNativePublicationTx(ctx, tx, input, func(activateCtx context.Context, activateTx dashboardpublicationpostgres.Tx, projectID projectgraph.ResourceID, name string) error {
		return f.AccessModule.ActivateDashboardPublicationThroughPersistence(activateCtx, activateTx, projectID, name)
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// TestPostgresJourneyFixtureSmoke proves the native graph and route-surface
// assembly path without any local database authority. The normal shards skip
// via postgrestest.Start; the dedicated conformance lane sets REQUIRED.
func TestPostgresJourneyFixtureSmoke(t *testing.T) {
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{NativeDashboard: true})
	if fixture.Graph == nil || fixture.RuntimePool == nil || fixture.MaintenancePool == nil {
		t.Fatal("PostgreSQL journey fixture did not retain native graph and pools")
	}
	if fixture.Graph.Idempotency == nil || fixture.Graph.CursorSigning == nil {
		t.Fatal("PostgreSQL journey fixture omitted graph API protocol authorities")
	}
	if fixture.Handler == nil {
		t.Fatal("PostgreSQL journey fixture did not assemble an application handler")
	}
	if got := fixture.MaintenancePool.PoolConfig().MaxConns; got != 1 {
		t.Fatalf("PostgreSQL journey maintenance pool MaxConns=%d, want 1", got)
	}
}
