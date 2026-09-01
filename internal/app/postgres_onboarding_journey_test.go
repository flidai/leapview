package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/flidai/leapview/internal/app/config"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

// TestPostgres18ProductionOnboardingJourney is the executable operator
// contract for a clean target: migrate, initialize, admit the exact physical
// pool and catalog, then start the PostgreSQL-only production graph. Replays
// must converge without recovering acknowledged credential material.
func TestPostgres18ProductionOnboardingJourney(t *testing.T) {
	h := postgrestest.StartTLS(t)
	roles := provisionPostgresOnboardingRoles(t, h)
	control := h.NewDatabase(t, "leapview_control")
	catalog := h.NewDatabase(t, "leapview_ducklake")
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

	target, err := BuildProduction(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build production application after native onboarding: %v", err)
	}
	if err := target.Start(t.Context()); err != nil {
		t.Fatalf("start production application after native onboarding: %v", err)
	}
	requestHTTP := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	requestHTTP.Host = "localhost"
	response := httptest.NewRecorder()
	target.Handler().ServeHTTP(response, requestHTTP)
	if response.Code != http.StatusOK {
		t.Fatalf("onboarded production readiness = %d; body=%s", response.Code, response.Body.String())
	}
	if err := target.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown onboarded production application: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(cfg.HomeDir, "*.db*")); err != nil || len(matches) != 0 {
		t.Fatalf("production onboarding created SQLite authority files: matches=%v err=%v", matches, err)
	}
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
		QueryResultMaxRows: 10_000, QueryResultMaxBytes: 32 << 20,
		QueryCacheRuntimeMaxEntries: 16, QueryCacheRuntimeMaxBytes: 4 << 20, QueryCacheNodeMaxEntries: 64, QueryCacheNodeMaxBytes: 16 << 20,
		CSRFKey: strings.Repeat("c", 32), TokenHashKey: strings.Repeat("t", 32), MetricsBearerToken: strings.Repeat("m", 32),
		APITokenOnlyAuth: true, PublicURL: "https://localhost", AllowedHosts: "localhost",
	}
}
