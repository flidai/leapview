package app

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	postgresducklake "github.com/flidai/leapview/internal/app/postgresducklake"
	projectsource "github.com/flidai/leapview/internal/app/projectsource"
	"github.com/flidai/leapview/internal/deployment"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/flidai/leapview/internal/servingstate"
)

type postgresTargetLookupFake struct {
	target deployment.DeliveryTarget
	err    error
}

func TestPostgresLifecycleNamedPoolsIncludesOnlyRetainedServingPools(t *testing.T) {
	lifecycle := &postgresControlPlaneLifecycle{
		pools: &platformpostgres.ControlPlanePools{
			Migrator:    &platformpostgres.Pool{},
			Runtime:     &platformpostgres.Pool{},
			Maintenance: &platformpostgres.Pool{},
			Readonly:    &platformpostgres.Pool{},
		},
		ducklake:            &platformpostgres.Pool{},
		ducklakeMaintenance: &platformpostgres.Pool{},
	}

	got := lifecycle.NamedPools()
	if len(got) != 5 {
		t.Fatalf("named serving pools = %d, want 5", len(got))
	}
	want := []string{
		platformpostgres.ControlRuntimePoolName,
		platformpostgres.ControlMaintenancePoolName,
		platformpostgres.DuckLakeRuntimePoolName,
		platformpostgres.DuckLakeMaintenancePoolName,
		platformpostgres.ControlReadonlyPoolName,
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("pool %d name = %q, want %q", i, got[i].Name, name)
		}
		if got[i].Pool == nil {
			t.Fatalf("pool %d (%q) is nil", i, name)
		}
	}
	for _, pool := range got {
		if pool.Name == "" || pool.Name == "migrator" {
			t.Fatalf("unexpected telemetry pool name %q", pool.Name)
		}
	}
}

func (f postgresTargetLookupFake) ResolveDeliveryTarget(context.Context, string) (deployment.DeliveryTarget, error) {
	return f.target, f.err
}

func TestResolvePostgresSealedActiveStateAllowsFreshUnclaimedTarget(t *testing.T) {
	id, err := resolvePostgresSealedActiveState(t.Context(), postgresTargetLookupFake{err: deployment.ErrNotFound}, "target-prod")
	if !errors.Is(err, servingstate.ErrNotFound) {
		t.Fatalf("missing target error = %v, want serving-state not found", err)
	}
	if id != "" {
		t.Fatalf("missing target generation id = %q, want empty", id)
	}
}

func TestResolvePostgresSealedActiveStateRequiresActiveGeneration(t *testing.T) {
	id, err := resolvePostgresSealedActiveState(t.Context(), postgresTargetLookupFake{target: deployment.DeliveryTarget{TargetID: "target-prod"}}, "target-prod")
	if !errors.Is(err, servingstate.ErrNotFound) {
		t.Fatalf("target without active generation error = %v, want serving-state not found", err)
	}
	if id != "" {
		t.Fatalf("target without active generation id = %q, want empty", id)
	}
}

func TestResolvePostgresSealedActiveStatePropagatesAuthorityFailure(t *testing.T) {
	want := errors.New("database unavailable")
	_, err := resolvePostgresSealedActiveState(t.Context(), postgresTargetLookupFake{err: want}, "target-prod")
	if !errors.Is(err, want) {
		t.Fatalf("authority failure = %v, want %v", err, want)
	}
}

func TestComposeNativeProjectSourceUsesExactDomainAndProjectAuthority(t *testing.T) {
	home := t.TempDir()
	rootParent := filepath.Join(home, "objects")
	if err := os.Mkdir(rootParent, 0o700); err != nil {
		t.Fatal(err)
	}
	repo := projectpostgres.New(nil)
	begin := projectsource.BeginFunc(func(context.Context) (projectsource.Tx, error) {
		return nil, errors.New("unused test transaction")
	})
	composed, err := composeNativeProjectSource(context.Background(), config.Config{HomeDir: home, ObjectStoreFilesystemRoot: filepath.Join(rootParent, "store")}, "instance-a", "prod", begin, repo)
	if err != nil {
		t.Fatal(err)
	}
	if composed.CandidateSourceReader == nil {
		t.Fatal("native candidate source reader is nil")
	}
	if composed.Sources != repo {
		t.Fatalf("source authority identity changed: got %T, want exact repository", composed.Sources)
	}
	if _, ok := composed.Objects.(*platformobjectstore.FilesystemStore); !ok {
		t.Fatalf("object store type = %T, want native filesystem store", composed.Objects)
	}
	if composed.StorageSecurityDomain == "" {
		t.Fatal("storage security domain is empty")
	}
}

func TestComposeNativeProjectSourceRejectsFreshMissingAuthoritiesWithoutStoreSideEffects(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "objects")
	repo := projectpostgres.New(nil)
	_, err := composeNativeProjectSource(context.Background(), config.Config{HomeDir: home, ObjectStoreFilesystemRoot: root}, "instance-a", "prod", nil, repo)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL begin") {
		t.Fatalf("missing begin error = %v, want PostgreSQL authority validation", err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing-authority composition touched object-store root: %v", statErr)
	}
}

func TestPostgresBuildSourceCompositionHasNoSQLiteOrPathFallbackImports(t *testing.T) {
	contents, err := os.ReadFile("postgres_build.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "postgres_build.go", contents, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path == "database/sql" {
			t.Fatalf("postgres build imports forbidden SQLite fallback %q", path)
		}
	}
	if strings.Contains(string(contents), "DBPath(") {
		t.Fatal("postgres build contains a database-path fallback")
	}
}

func TestPostgresBuildComposesOnlyNativeDeliveryMutations(t *testing.T) {
	contents, err := os.ReadFile("postgres_build.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	normalizedSource := strings.Join(strings.Fields(source), " ")
	for _, required := range []string{
		"NewNativeCreatePlanCoordinator(",
		"NewNativeBuildCoordinator(",
		"NewNativeDeliveryCoordinator(",
		"NativeDeliveryMutations: nativeDelivery",
		"ProjectClaims:           graph.DeploymentRepository",
		"BindClaimedProject:      bindClaimedProject(runtimeHost, environment)",
	} {
		if !strings.Contains(normalizedSource, strings.Join(strings.Fields(required), " ")) {
			t.Fatalf("PostgreSQL composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		" DeliveryMutations:",
		"DeliveryCandidateBuilder:",
		"CanonicalDeliveryAdapter:",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("PostgreSQL composition contains legacy delivery wiring %q", forbidden)
		}
	}
}

func TestPostgresBuildComposesPoolScopedL3Maintenance(t *testing.T) {
	contents, err := os.ReadFile("postgres_build.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{
		"shouldResolveL3CacheMaintenance(cfg)",
		"analyticsl3.NewCollector(",
		"cachepostgres.NewMaintenance(bootstrap.MaintenancePool())",
		"SecurityDomain: contract.PhysicalPoolID",
		"GracePeriod: time.Duration(orphanGraceSeconds) * time.Second",
		"newL3GCWorker(",
		"workloadmodule.MaintenanceRequest(\"cache.l3.gc\")",
		"AdditionalWorkers: additionalWorkers",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("PostgreSQL composition is missing L3 maintenance seam %q", required)
		}
	}
}

func TestPostgresBuildComposesNativeRefreshExecutionAndFinalization(t *testing.T) {
	contents, err := os.ReadFile("postgres_build.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	for _, required := range []string{
		"NewPostgresNativeRefreshFinalizer(",
		"NativeFinalizer: nativeRefreshFinalizer",
		"NewPostgresNativeRefreshExecutor(",
		"EnableRefreshDispatcher: true",
		"RefreshTargetRevision: resolveRefreshTargetRevision",
		"RefreshSourceDigest: resolveRefreshSourceDigest",
		"CanonicalRefreshExecutor: nativeRefreshExecutor.Execute",
		"CanonicalCompletionCoordinator: canonicalCompletionCoordinator",
		"CanonicalResultReconciler: canonicalResultReconciler",
		"SCIMBearerToken: cfg.SCIMBearerToken",
		"MetricsBearerToken: cfg.MetricsBearerToken",
		"rateLimits.UseRealIP = cfg.RateLimitingUsesRealIP()",
		"SecurityHeaders: apihttpmiddleware.SecurityHeaders(cfg.HSTSEnabled(cookieSecure))",
		"DesktopDiscovery: desktopdiscovery.Config",
		"nativeDeliveryReader.LoadGeneration(completionCtx, result.NativeGenerationID)",
		"runtimeHost.PrepareSealedActivation(completionCtx, result.ServingStateID, generation.CandidateID)",
		"runtimeHost.ActivatePreparedContext(completionCtx, prepared, complete)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("PostgreSQL composition is missing %q", required)
		}
	}
	if strings.Contains(source, "runtimeHost.ReconcileSealed(reconcileCtx, servingstate.ID(target.ActiveGenerationID))") {
		t.Fatal("PostgreSQL refresh reconciliation still performs a post-commit runtime cutover")
	}
}

func TestPostgresBuildWiresNativeAgentSettingsAuthority(t *testing.T) {
	contents, err := os.ReadFile("postgres_build.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	if !strings.Contains(source, "workflowAssemblyInputs{AgentSettings: graph.Settings") {
		t.Fatal("PostgreSQL composition does not pass the graph-owned settings authority to workflow assembly")
	}
	if strings.Contains(source, "AgentSettings: store") {
		t.Fatal("PostgreSQL composition falls back to the local SQLite settings store")
	}
}

func TestPostgresSettingsAuthoritySatisfiesAgentSettingsPortAndPreservesIdentity(t *testing.T) {
	settingsRepository := platformbootstrappostgres.New(nil)
	graph := &postgresauthority.PostgresAuthorityGraph{Settings: settingsRepository}

	var settings agentmodule.Settings = graph.Settings
	if settings == nil {
		t.Fatal("native PostgreSQL settings authority was converted to a nil agent settings port")
	}
	if settings != settingsRepository {
		t.Fatalf("agent settings authority identity changed: got %T, want exact PostgreSQL settings repository", settings)
	}
}

func TestBuildProductionFailsClosedBeforePostgresConnection(t *testing.T) {
	_, err := BuildProduction(context.Background(), config.Config{Production: true})
	if err == nil {
		t.Fatal("BuildProduction accepted missing PostgreSQL control-plane configuration")
	}
	if !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("BuildProduction error = %v, want control URL validation", err)
	}
}

func TestBuildProductionRejectsSecurityBypassBeforeConnecting(t *testing.T) {
	cfg := config.Config{
		Production:                              true,
		DevAuthBypass:                           true,
		PostgresRequireTLS:                      true,
		PostgresControlURL:                      "postgres://runtime:secret@localhost/control?sslmode=require",
		PostgresControlMigratorURL:              "postgres://migrator:secret@localhost/control?sslmode=require",
		PostgresControlMaintenanceURL:           "postgres://maintenance:secret@localhost/control?sslmode=require",
		PostgresDuckLakeURL:                     "postgres://ducklake:secret@localhost/ducklake?sslmode=require",
		PostgresDuckLakeMaintenanceURL:          "postgres://ducklake-maintenance:secret@localhost/ducklake?sslmode=require",
		PostgresControlRuntimeRole:              "runtime",
		PostgresControlMigratorRole:             "migrator",
		PostgresControlMaintenanceRole:          "leapview_control_maintenance",
		PostgresDuckLakeRuntimeRole:             "ducklake",
		PostgresDuckLakeMaintenanceRole:         "ducklake-maintenance",
		DeliveryPhysicalPoolID:                  "pool-prod",
		DeliveryPhysicalPoolCompatibilityDigest: "sha256:" + strings.Repeat("a", 64),
	}
	_, err := BuildProduction(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_DEV_AUTH_BYPASS") {
		t.Fatalf("BuildProduction security validation error = %v", err)
	}
}

func TestBuildAlwaysUsesProductionPostgreSQLGate(t *testing.T) {
	_, err := Build(context.Background(), config.Config{})
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("Build PostgreSQL gate error = %v", err)
	}
}

func TestOpenPostgresControlPlaneRejectsMissingPoolConfiguration(t *testing.T) {
	_, err := openPostgresControlPlane(context.Background(), config.Config{})
	if err == nil {
		t.Fatal("openPostgresControlPlane accepted an empty pool configuration")
	}
}

func TestPostgresDuckLakeMaintenanceConfigIsDedicatedSingleConnection(t *testing.T) {
	cfg := config.Config{
		PostgresDuckLakeMaintenanceURL:  "postgres://maintenance:secret@localhost/ducklake?sslmode=require",
		PostgresDuckLakeMaintenanceRole: "leapview_ducklake_maintenance",
	}
	got := cfg.PostgresDuckLakeMaintenanceConfig()
	if got.MinConns != 1 || got.MaxConns != 1 {
		t.Fatalf("DuckLake maintenance pool bounds = %d/%d, want 1/1", got.MinConns, got.MaxConns)
	}
	if got.RuntimeRole != "leapview_ducklake_maintenance" {
		t.Fatalf("DuckLake maintenance role = %q", got.RuntimeRole)
	}
}

func TestValidatePostgresDuckLakeRuntimeIdentity(t *testing.T) {
	tests := []struct {
		name     string
		database string
		identity postgresducklake.DatabaseIdentity
		wantErr  bool
	}{
		{
			name:     "exact database and role",
			database: postgresducklake.DefaultDuckLakeDatabase,
			identity: postgresducklake.DatabaseIdentity{Database: postgresducklake.DefaultDuckLakeDatabase, User: "runtime", SessionUser: "runtime"},
		},
		{
			name:     "wrong database",
			database: "other_database",
			identity: postgresducklake.DatabaseIdentity{Database: "other_database", User: "runtime", SessionUser: "runtime"},
			wantErr:  true,
		},
		{
			name:     "identity database disagrees with pool",
			database: postgresducklake.DefaultDuckLakeDatabase,
			identity: postgresducklake.DatabaseIdentity{Database: "other_database", User: "runtime", SessionUser: "runtime"},
			wantErr:  true,
		},
		{
			name:     "wrong login role",
			database: postgresducklake.DefaultDuckLakeDatabase,
			identity: postgresducklake.DatabaseIdentity{Database: postgresducklake.DefaultDuckLakeDatabase, User: "wrong", SessionUser: "runtime"},
			wantErr:  true,
		},
		{
			name:     "wrong session role",
			database: postgresducklake.DefaultDuckLakeDatabase,
			identity: postgresducklake.DatabaseIdentity{Database: postgresducklake.DefaultDuckLakeDatabase, User: "runtime", SessionUser: "wrong"},
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePostgresDuckLakeRuntimeIdentity(tt.database, tt.identity, "runtime")
			if (err != nil) != tt.wantErr {
				t.Fatalf("validation error = %v, wantErr=%t", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, postgresducklake.ErrWrongDatabaseCredential) {
				t.Fatalf("validation error = %v, want ErrWrongDatabaseCredential", err)
			}
		})
	}
}

func TestValidatePostgresDuckLakeMaintenanceIdentityUsesDedicatedRole(t *testing.T) {
	identity := postgresducklake.DatabaseIdentity{Database: postgresducklake.DefaultDuckLakeDatabase, User: "maintenance", SessionUser: "maintenance"}
	if err := validatePostgresDuckLakeMaintenanceIdentity(postgresducklake.DefaultDuckLakeDatabase, identity, "maintenance"); err != nil {
		t.Fatalf("maintenance identity validation error = %v", err)
	}
	if err := validatePostgresDuckLakeMaintenanceIdentity(postgresducklake.DefaultDuckLakeDatabase, identity, "runtime"); !errors.Is(err, postgresducklake.ErrWrongDatabaseCredential) {
		t.Fatalf("maintenance identity accepted wrong role: %v", err)
	}
}
