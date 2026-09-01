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
	projectsource "github.com/flidai/leapview/internal/app/projectsource"
	"github.com/flidai/leapview/internal/deployment"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/flidai/leapview/internal/servingstate"
)

type postgresTargetLookupFake struct {
	target deployment.DeliveryTarget
	err    error
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
		if path == "database/sql" || path == "github.com/flidai/leapview/internal/project/sqlite" {
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
	for _, required := range []string{
		"NewNativeCreatePlanCoordinator(",
		"NewNativeBuildCoordinator(",
		"NewNativeDeliveryCoordinator(",
		"NativeDeliveryMutations: nativeDelivery",
	} {
		if !strings.Contains(source, required) {
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
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("PostgreSQL composition is missing %q", required)
		}
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

func TestBuildProductionFailsClosedBeforeLegacySQLiteComposition(t *testing.T) {
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

func TestBuildCannotBypassProductionPostgreSQLGate(t *testing.T) {
	_, err := Build(context.Background(), config.Config{Production: true})
	if err == nil || !strings.Contains(err.Error(), "LEAPVIEW_POSTGRES_CONTROL_URL") {
		t.Fatalf("Build production gate error = %v", err)
	}
}

func TestOpenPostgresControlPlaneRejectsMissingPoolConfiguration(t *testing.T) {
	_, err := openPostgresControlPlane(context.Background(), config.Config{})
	if err == nil {
		t.Fatal("openPostgresControlPlane accepted an empty pool configuration")
	}
}
