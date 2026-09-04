package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
)

// TestPostgres18ProductionOnboardingJourney is the executable operator
// contract for a clean target: migrate, initialize, admit the exact physical
// pool and catalog, then start the PostgreSQL-only production graph. Replays
// must converge without recovering acknowledged credential material. A
// second build/start against the same target proves restart persistence for
// the native authority, pool admission, and baseline state.
func TestPostgres18ProductionOnboardingJourney(t *testing.T) {
	h := postgrestest.StartTLS(t)
	roles := provisionPostgresOnboardingRoles(t, h)
	control := h.NewDatabase(t, "leapview_control")
	catalog := h.NewDatabase(t, ducklakepostgres.DefaultDuckLakeDatabase)
	grantPostgresOnboardingDatabases(t, h, control, catalog, roles)

	fixture := extensionfixture.New(t, "ducklake", "postgres")
	cfg := postgresOnboardingConfig(t, control, catalog, roles, fixture)

	operations := adminpostgres.New(adminpostgres.Dependencies{
		LoadConfig: func() (config.Config, error) { return cfg, nil },
	})
	var firstCredentials bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &firstCredentials); err != nil {
		t.Fatalf("initialize fresh PostgreSQL target: %v", err)
	}
	credentials, err := adminoffline.DecodeInitialCredentials(firstCredentials.Bytes())
	if err != nil || credentials.Email != cfg.BootstrapEmail || credentials.TemporaryPassword == "" || credentials.PublisherToken == "" {
		t.Fatalf("decode one-time credentials: credentials=%#v err=%v", credentials, err)
	}
	var replayCredentials bytes.Buffer
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &replayCredentials); err != nil {
		t.Fatalf("replay initialization before acknowledgement: %v", err)
	}
	if replayCredentials.String() != firstCredentials.String() {
		t.Fatal("initialization replay changed one-time credential evidence")
	}
	if err := operations.AcknowledgeInitialCredentials(t.Context()); err != nil {
		t.Fatalf("acknowledge one-time credentials: %v", err)
	}
	if err := operations.Initialize(t.Context(), adminoffline.InitializeRequest{Format: "json"}, &bytes.Buffer{}); !errors.Is(err, adminoffline.ErrInstanceAlreadyInitialized) {
		t.Fatalf("credential read after acknowledgement error = %v", err)
	}

	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:1.0.0", CatalogFormat: "ducklake:1.0",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	evidence, err := ducklake.RunLocalPoolConformance(t.Context(), filepath.Join(cfg.HomeDir, "conformance"), tuple, fixture.Admission)
	if err != nil {
		t.Fatalf("qualify local physical pool: %v", err)
	}
	poolIdentity := physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(cfg.HomeDir, "physical-pools"), StorageNamespace: "delivery", Region: "local", Tenant: "production",
		EncryptionDomain: "production", IsolationBoundary: "production", RetentionAuthority: "production",
		RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60},
		Compatibility:   tuple,
	}
	request := adminoffline.PhysicalPoolBootstrapRequest{Pool: poolIdentity, Evidence: evidence, Apply: true}
	var firstBootstrap bytes.Buffer
	if err := operations.BootstrapPhysicalPool(t.Context(), request, &firstBootstrap); err != nil {
		t.Fatalf("bootstrap native PostgreSQL physical pool: %v", err)
	}
	var replayBootstrap bytes.Buffer
	if err := operations.BootstrapPhysicalPool(t.Context(), request, &replayBootstrap); err != nil {
		t.Fatalf("replay native PostgreSQL physical pool bootstrap: %v", err)
	}
	if replayBootstrap.String() != firstBootstrap.String() || !strings.Contains(firstBootstrap.String(), "applied: true") {
		t.Fatalf("physical-pool replay drift: first=%q replay=%q", firstBootstrap.String(), replayBootstrap.String())
	}
	for _, secret := range []string{roles.controlRuntime.Password, roles.controlMigrator.Password, roles.catalogRuntime.Password, roles.catalogMigrator.Password, credentials.TemporaryPassword, credentials.PublisherToken} {
		if strings.Contains(firstBootstrap.String(), secret) {
			t.Fatal("physical-pool bootstrap output exposed credential material")
		}
	}
	pool, err := physicalpool.NewPhysicalPool(poolIdentity)
	if err != nil {
		t.Fatal(err)
	}
	compatibilityDigest, err := tuple.Digest()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeliveryPhysicalPoolID = pool.ID.String()
	cfg.DeliveryPhysicalPoolCompatibilityDigest = compatibilityDigest
	originalConfig := cfg

	target, err := BuildProduction(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build production application after native onboarding: %v", err)
	}
	if err := target.Start(t.Context()); err != nil {
		t.Fatalf("start production application after native onboarding: %v", err)
	}
	firstInstance := assertPostgresOnboardingTarget(t, target, credentials.PublisherToken, credentials.Email)
	if err := target.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown onboarded production application: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(cfg.HomeDir, "*.db*")); err != nil || len(matches) != 0 {
		t.Fatalf("production onboarding created SQLite authority files: matches=%v err=%v", matches, err)
	}
	firstPersistence := capturePostgresOnboardingPersistence(t, cfg, control, catalog, roles, pool.ID, tuple)
	if firstPersistence.InstanceID != firstInstance.Id {
		t.Fatalf("first target instance identity = %q, persistence identity = %q", firstInstance.Id, firstPersistence.InstanceID)
	}

	// BuildProduction consumes the same initialized databases, admitted pool
	// evidence, home directory, and config. A successful second start must not
	// synthesize a new target identity or alter those durable records.
	if cfg != originalConfig {
		t.Fatal("production target build mutated the onboarding config")
	}
	secondTarget, err := BuildProduction(t.Context(), cfg)
	if err != nil {
		t.Fatalf("rebuild production application after native onboarding restart: %v", err)
	}
	if err := secondTarget.Start(t.Context()); err != nil {
		t.Fatalf("start rebuilt production application after native onboarding restart: %v", err)
	}
	secondInstance := assertPostgresOnboardingTarget(t, secondTarget, credentials.PublisherToken, credentials.Email)
	if secondInstance != firstInstance {
		t.Fatalf("restart changed instance API identity: first=%#v second=%#v", firstInstance, secondInstance)
	}
	if err := secondTarget.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown rebuilt production application: %v", err)
	}
	secondPersistence := capturePostgresOnboardingPersistence(t, cfg, control, catalog, roles, pool.ID, tuple)
	if secondPersistence.InstanceID != firstPersistence.InstanceID {
		t.Fatalf("restart changed PostgreSQL instance identity: first=%q second=%q", firstPersistence.InstanceID, secondPersistence.InstanceID)
	}
	if !reflect.DeepEqual(secondPersistence.Contract, firstPersistence.Contract) {
		t.Fatalf("restart changed admitted physical-pool contract: first=%#v second=%#v", firstPersistence.Contract, secondPersistence.Contract)
	}
	if cfg != originalConfig {
		t.Fatal("rebuilt production target mutated the onboarding config")
	}
	if matches, err := filepath.Glob(filepath.Join(cfg.HomeDir, "*.db*")); err != nil || len(matches) != 0 {
		t.Fatalf("production onboarding restart created SQLite authority files: matches=%v err=%v", matches, err)
	}
}

type postgresOnboardingPersistenceSnapshot struct {
	InstanceID string
	Contract   physicalpool.AdmissionContract
}

func assertPostgresOnboardingTarget(t *testing.T, target *Application, publisherToken, expectedEmail string) apigenapi.InstanceResponse {
	t.Helper()
	requestHTTP := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	requestHTTP.Host = "localhost"
	response := httptest.NewRecorder()
	target.Handler().ServeHTTP(response, requestHTTP)
	if response.Code != http.StatusOK {
		t.Fatalf("onboarded production readiness = %d; body=%s", response.Code, response.Body.String())
	}

	instanceRequest := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	instanceRequest.Host = "localhost"
	instanceResponse := httptest.NewRecorder()
	target.Handler().ServeHTTP(instanceResponse, instanceRequest)
	if instanceResponse.Code != http.StatusOK {
		t.Fatalf("onboarded production instance API = %d; body=%s", instanceResponse.Code, instanceResponse.Body.String())
	}
	var instance apigenapi.InstanceResponse
	if err := json.NewDecoder(instanceResponse.Body).Decode(&instance); err != nil {
		t.Fatalf("decode onboarded production instance API: %v", err)
	}
	if strings.TrimSpace(instance.Id) == "" || instance.Environment != "prod" || instance.CanonicalOrigin != "https://localhost" {
		t.Fatalf("onboarded production instance API = %#v", instance)
	}

	// A publisher token is an initialized, read-only credential smoke through
	// the public API. Governed semantic-model/dashboard reads are intentionally
	// not attempted: clean onboarding has no claimed project or active serving
	// generation, and creating that fixture would broaden this restart proof.
	meRequest := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meRequest.Host = "localhost"
	meRequest.Header.Set("Authorization", "Bearer "+publisherToken)
	meResponse := httptest.NewRecorder()
	target.Handler().ServeHTTP(meResponse, meRequest)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("onboarded production current-principal API = %d; body=%s", meResponse.Code, meResponse.Body.String())
	}
	var principal struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(meResponse.Body).Decode(&principal); err != nil {
		t.Fatalf("decode onboarded production current-principal API: %v", err)
	}
	if principal.Email != expectedEmail {
		t.Fatalf("onboarded production current-principal email = %q", principal.Email)
	}
	return instance
}

func capturePostgresOnboardingPersistence(t *testing.T, cfg config.Config, control, catalog *postgrestest.Database, roles postgresOnboardingRoles, poolID physicalpool.PoolID, tuple physicalpool.Compatibility) postgresOnboardingPersistenceSnapshot {
	t.Helper()
	lifecycle, err := openPostgresControlPlane(t.Context(), cfg)
	if err != nil {
		t.Fatalf("reopen PostgreSQL authorities for restart persistence proof: %v", err)
	}
	defer func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Errorf("close PostgreSQL authorities after restart persistence proof: %v", err)
		}
	}()
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatalf("start PostgreSQL authorities for restart persistence proof: %v", err)
	}
	assertProductionAdmissionPoolIdentity(t, lifecycle.RuntimePool(), control.Name, roles.controlRuntime.Name)
	assertProductionAdmissionPoolIdentity(t, lifecycle.MaintenancePool(), control.Name, roles.controlMaintenance.Name)
	assertProductionAdmissionPoolIdentity(t, lifecycle.DuckLakePool(), catalog.Name, roles.catalogRuntime.Name)
	if lifecycle.pools.Readonly == nil {
		t.Fatal("reopened PostgreSQL authorities omitted configured readonly pool")
	}
	assertProductionAdmissionPoolIdentity(t, lifecycle.pools.Readonly, control.Name, roles.controlReadonly.Name)

	instanceID, err := postgresauthority.ResolveInstanceIdentity(t.Context(), lifecycle.RuntimePool(), cfg.Environment)
	if err != nil {
		t.Fatalf("resolve PostgreSQL instance identity after restart: %v", err)
	}
	contract, err := physicalpoolpostgres.New(lifecycle.RuntimePool()).LoadAdmissionContract(t.Context(), poolID, tuple)
	if err != nil {
		t.Fatalf("load admitted PostgreSQL physical-pool contract after restart: %v", err)
	}
	if contract.Pool.ID != poolID || contract.Admission.PoolID != poolID || contract.Admission.CompatibilityDigest == "" || contract.Admission.EvidenceDigest == "" {
		t.Fatalf("incomplete admitted PostgreSQL physical-pool contract after restart: %#v", contract)
	}
	if err := physicalpool.VerifyAdmission(contract.Pool, tuple, contract.Admission, contract.Evidence); err != nil {
		t.Fatalf("verify admitted PostgreSQL physical-pool contract after restart: %v", err)
	}
	// The compatibility ledger is part of the control-plane baseline, while
	// the separately authenticated DuckLake pool contains only per-pool
	// metadata.  Exercise both identities so runtime attach cannot regress to
	// querying the external catalog database for control evidence.
	ledger := ducklakepostgres.New(lifecycle.RuntimePool())
	compatibility, err := ledger.LoadCatalogRuntimeCompatibility(t.Context(), poolID.String())
	if err != nil {
		t.Fatalf("load control-plane DuckLake runtime compatibility after restart: %v", err)
	}
	eligibility, err := ledger.CheckRuntimeAttachEligibility(t.Context(), ducklakepostgres.RuntimeAttachInput{
		PhysicalPoolID: poolID.String(), CatalogID: compatibility.CatalogID, Compatibility: compatibility.RuntimeCompatibility,
	})
	if err != nil || !eligibility.Eligible {
		t.Fatalf("control-plane DuckLake runtime attach eligibility = %#v, err %v", eligibility, err)
	}
	externalChecker := ducklakepostgres.New(lifecycle.DuckLakePool())
	if _, err := externalChecker.CheckRuntimeAttachEligibility(t.Context(), ducklakepostgres.RuntimeAttachInput{
		PhysicalPoolID: poolID.String(), CatalogID: compatibility.CatalogID, Compatibility: compatibility.RuntimeCompatibility,
	}); err == nil {
		t.Fatal("external DuckLake catalog unexpectedly exposed control-plane runtime compatibility")
	}
	return postgresOnboardingPersistenceSnapshot{InstanceID: instanceID, Contract: contract}
}

type postgresOnboardingRoles struct {
	controlOwner, controlMigrator, controlRuntime, controlMaintenance, controlReadonly, controlBackup postgrestest.Role
	controlUpgrade, catalogOwner, catalogMigrator, catalogRuntime, catalogMaintenance                 postgrestest.Role
}

func provisionPostgresOnboardingRoles(t *testing.T, h *postgrestest.Harness) postgresOnboardingRoles {
	t.Helper()
	roles := postgresOnboardingRoles{
		controlOwner:       h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"}),
		controlMigrator:    h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "onboarding-control-migrator", Login: true}),
		controlRuntime:     h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "onboarding-control-runtime", Login: true}),
		controlMaintenance: h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "onboarding-control-maintenance", Login: true}),
		controlReadonly:    h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "onboarding-control-readonly", Login: true}),
		controlBackup:      h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"}),
		controlUpgrade:     h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_upgrade_coordinator", Password: "onboarding-control-upgrade", Login: true}),
		catalogOwner:       h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_owner"}),
		catalogMigrator:    h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_migrator", Password: "onboarding-catalog-migrator", Login: true}),
		catalogRuntime:     h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_runtime", Password: "onboarding-catalog-runtime", Login: true}),
		catalogMaintenance: h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_maintenance", Password: "onboarding-catalog-maintenance", Login: true}),
	}
	h.GrantRole(t, roles.controlOwner, roles.controlMigrator)
	h.GrantRole(t, roles.catalogOwner, roles.catalogMigrator)
	return roles
}

func grantPostgresOnboardingDatabases(t *testing.T, h *postgrestest.Harness, control, catalog *postgrestest.Database, roles postgresOnboardingRoles) {
	t.Helper()
	for _, role := range []postgrestest.Role{roles.controlOwner, roles.controlMigrator} {
		h.GrantDatabase(t, control.Name, role, "CONNECT", "CREATE")
	}
	for _, role := range []postgrestest.Role{roles.controlRuntime, roles.controlMaintenance, roles.controlReadonly, roles.controlUpgrade} {
		h.GrantDatabase(t, control.Name, role, "CONNECT")
	}
	h.GrantDatabase(t, catalog.Name, roles.catalogMigrator, "CONNECT", "CREATE", "TEMPORARY")
	h.GrantDatabase(t, catalog.Name, roles.catalogRuntime, "CONNECT", "TEMPORARY")
	h.GrantDatabase(t, catalog.Name, roles.catalogMaintenance, "CONNECT", "TEMPORARY")
	admin, err := pgx.Connect(t.Context(), control.AdminURL())
	if err != nil {
		t.Fatalf("open onboarding control administrator: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(t.Context(), `GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatalf("grant Goose version-schema ownership to control migrator: %v", err)
	}
}

func postgresOnboardingConfig(t *testing.T, control, catalog *postgrestest.Database, roles postgresOnboardingRoles, fixture extensionfixture.Fixture) config.Config {
	t.Helper()
	tlsURL := productionAdmissionTLSURL
	home := t.TempDir()
	return config.Config{
		HomeDir: home, Production: true, Environment: "prod", BootstrapEmail: "admin@example.com",
		PostgresExpectedMajor: platformpostgres.DefaultExpectedMajor, PostgresRequireTLS: true,
		PostgresControlURL:                    tlsURL(control.URL(roles.controlRuntime)),
		PostgresControlMigratorURL:            tlsURL(control.URL(roles.controlMigrator)),
		PostgresControlMaintenanceURL:         tlsURL(control.URL(roles.controlMaintenance)),
		PostgresControlReadonlyURL:            tlsURL(control.URL(roles.controlReadonly)),
		PostgresControlUpgradeCoordinatorURL:  tlsURL(control.URL(roles.controlUpgrade)),
		PostgresDuckLakeURL:                   tlsURL(catalog.URL(roles.catalogRuntime)),
		PostgresDuckLakeMigratorURL:           tlsURL(catalog.URL(roles.catalogMigrator)),
		PostgresDuckLakeMaintenanceURL:        tlsURL(catalog.URL(roles.catalogMaintenance)),
		PostgresControlRuntimeRole:            roles.controlRuntime.Name,
		PostgresControlMigratorRole:           roles.controlMigrator.Name,
		PostgresControlMaintenanceRole:        roles.controlMaintenance.Name,
		PostgresControlReadonlyRole:           roles.controlReadonly.Name,
		PostgresControlUpgradeCoordinatorRole: roles.controlUpgrade.Name,
		PostgresDuckLakeRuntimeRole:           roles.catalogRuntime.Name,
		PostgresDuckLakeMigratorRole:          roles.catalogMigrator.Name,
		PostgresDuckLakeMaintenanceRole:       roles.catalogMaintenance.Name,
		ManagedDataBackend:                    "local", ManagedDataDir: filepath.Join(home, "managed-data"),
		ManagedDataMaxFiles: 100, ManagedDataMaxFileBytes: 1 << 20, ManagedDataMaxRevisionBytes: 10 << 20,
		ManagedDataMinFreeBytes: 1, ManagedDataUploadSessionTTL: time.Hour, ManagedDataGCInterval: time.Hour, ManagedDataGCGracePeriod: time.Hour,
		DuckDBExtensionSupplyPath: fixture.SupplyPath, DuckDBExtensionSupplySHA256: fixture.SupplySHA256, DuckDBExtensionCacheDir: fixture.CacheDir,
		DuckDBNodeMemoryMaxBytes: 256 << 20, DuckDBNodeTempMaxBytes: 1 << 30, DuckDBNodeMaxThreads: 2,
		DuckLakeRetentionInterval: time.Hour, DuckLakeRetentionFileGracePeriod: 24 * time.Hour,
		QueryResultMaxRows: 10_000, QueryResultMaxBytes: 32 << 20,
		QueryCacheRuntimeMaxEntries: 16, QueryCacheRuntimeMaxBytes: 4 << 20, QueryCacheNodeMaxEntries: 64, QueryCacheNodeMaxBytes: 16 << 20,
		CSRFKey: strings.Repeat("c", 32), TokenHashKey: strings.Repeat("t", 32), MetricsBearerToken: strings.Repeat("m", 32),
		APITokenOnlyAuth: true, PublicURL: "https://localhost", AllowedHosts: "localhost",
	}
}
