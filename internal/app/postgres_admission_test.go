package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ducklakecatalog "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	postgresducklake "github.com/flidai/leapview/internal/app/postgresducklake"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
)

// TestPostgres18ProductionAdmission exercises the public production entrypoint
// against PostgreSQL 18 and proves that a fresh, unclaimed target is
// administrable without fabricating an active project or generation.
func TestPostgres18ProductionAdmission(t *testing.T) {
	h := postgrestest.StartTLS(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "admission-migrator", Login: true})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "admission-runtime", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "admission-maintenance", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	ducklakeRuntime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_runtime", Password: "admission-ducklake", Login: true})
	ducklakeMaintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_maintenance", Password: "admission-ducklake-maintenance", Login: true})
	ducklakeOwner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_owner"})
	h.GrantRole(t, owner, migrator)

	control := h.NewDatabase(t, "leapview_production_admission_control")
	ducklake := h.NewDatabase(t, postgresducklake.DefaultDuckLakeDatabase)
	h.GrantDatabase(t, control.Name, owner, "CREATE")
	h.GrantDatabase(t, control.Name, migrator, "CONNECT", "CREATE")
	h.GrantDatabase(t, control.Name, runtime, "CONNECT")
	h.GrantDatabase(t, control.Name, maintenance, "CONNECT")
	bootstrapAdmin, err := pgx.Connect(t.Context(), control.AdminURL())
	if err != nil {
		t.Fatalf("open control administrator: %v", err)
	}
	if _, err := bootstrapAdmin.Exec(t.Context(), `GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		_ = bootstrapAdmin.Close(context.Background())
		t.Fatalf("grant Goose version-schema ownership to control migrator: %v", err)
	}
	if err := bootstrapAdmin.Close(context.Background()); err != nil {
		t.Fatalf("close control administrator: %v", err)
	}
	ducklakeAdmin, err := pgx.Connect(t.Context(), ducklake.AdminURL())
	if err != nil {
		t.Fatalf("open DuckLake admission administrator: %v", err)
	}
	t.Cleanup(func() { _ = ducklakeAdmin.Close(context.Background()) })
	ducklakeDatabase := pgx.Identifier{ducklake.Name}.Sanitize()
	ducklakeOwnerName := pgx.Identifier{ducklakeOwner.Name}.Sanitize()
	ducklakeRuntimeName := pgx.Identifier{ducklakeRuntime.Name}.Sanitize()
	ducklakeMaintenanceName := pgx.Identifier{ducklakeMaintenance.Name}.Sanitize()
	physicalPoolID := "sha256:" + strings.Repeat("b", 64)
	metadataSchema := pgx.Identifier{ducklakecatalog.MetadataSchemaForPool(physicalPoolID)}.Sanitize()
	for _, statement := range []string{
		"REVOKE ALL ON DATABASE " + ducklakeDatabase + " FROM PUBLIC",
		"CREATE SCHEMA " + metadataSchema + " AUTHORIZATION " + ducklakeOwnerName,
		"REVOKE ALL ON SCHEMA " + metadataSchema + " FROM PUBLIC",
		"GRANT USAGE ON SCHEMA " + metadataSchema + " TO " + ducklakeRuntimeName + ", " + ducklakeMaintenanceName,
	} {
		if _, err := ducklakeAdmin.Exec(t.Context(), statement); err != nil {
			t.Fatalf("prepare DuckLake admission policy: %v", err)
		}
	}
	h.GrantDatabase(t, ducklake.Name, ducklakeRuntime, "CONNECT")
	h.GrantDatabase(t, ducklake.Name, ducklakeMaintenance, "CONNECT")

	cfg := config.Config{
		Production: true, PostgresExpectedMajor: platformpostgres.DefaultExpectedMajor, PostgresRequireTLS: true,
		PostgresControlURL:             productionAdmissionTLSURL(control.URL(runtime)),
		PostgresControlMigratorURL:     productionAdmissionTLSURL(control.URL(migrator)),
		PostgresControlMaintenanceURL:  productionAdmissionTLSURL(control.URL(maintenance)),
		PostgresDuckLakeURL:            productionAdmissionTLSURL(ducklake.URL(ducklakeRuntime)),
		PostgresDuckLakeMaintenanceURL: productionAdmissionTLSURL(ducklake.URL(ducklakeMaintenance)),
		PostgresControlRuntimeRole:     runtime.Name, PostgresControlMigratorRole: migrator.Name,
		PostgresControlMaintenanceRole: maintenance.Name, PostgresDuckLakeRuntimeRole: ducklakeRuntime.Name,
		PostgresDuckLakeMaintenanceRole: ducklakeMaintenance.Name,
	}
	fixture := extensionfixture.New(t, "ducklake")
	cfg.HomeDir = t.TempDir()
	cfg.ManagedDataBackend = "local"
	cfg.ManagedDataDir = filepath.Join(cfg.HomeDir, "managed-data")
	cfg.ManagedDataMaxFiles = 100
	cfg.ManagedDataMaxFileBytes = 1 << 20
	cfg.ManagedDataMaxRevisionBytes = 10 << 20
	cfg.ManagedDataMinFreeBytes = 1
	cfg.ManagedDataUploadSessionTTL = time.Hour
	cfg.ManagedDataGCInterval = time.Hour
	cfg.ManagedDataGCGracePeriod = time.Hour
	cfg.DuckDBExtensionSupplyPath = fixture.SupplyPath
	cfg.DuckDBExtensionSupplySHA256 = fixture.SupplySHA256
	cfg.DuckDBExtensionCacheDir = fixture.CacheDir
	cfg.DuckDBNodeMemoryMaxBytes = 256 << 20
	cfg.DuckDBNodeTempMaxBytes = 1 << 30
	cfg.DuckDBNodeMaxThreads = 2
	cfg.DuckLakeRetentionInterval = time.Hour
	cfg.DuckLakeRetentionFileGracePeriod = 24 * time.Hour
	cfg.QueryResultMaxRows = 10_000
	cfg.QueryResultMaxBytes = 32 << 20
	cfg.QueryCacheRuntimeMaxEntries = 16
	cfg.QueryCacheRuntimeMaxBytes = 4 << 20
	cfg.QueryCacheNodeMaxEntries = 64
	cfg.QueryCacheNodeMaxBytes = 16 << 20
	cfg.CSRFKey = strings.Repeat("c", 32)
	cfg.TokenHashKey = strings.Repeat("t", 32)
	cfg.MetricsBearerToken = strings.Repeat("m", 32)
	cfg.APITokenOnlyAuth = true
	cfg.PublicURL = "https://localhost"
	cfg.AllowedHosts = "localhost"
	cfg.Environment = "prod"
	cfg.DeliveryPhysicalPoolID = physicalPoolID
	cfg.DeliveryPhysicalPoolCompatibilityDigest = "sha256:" + strings.Repeat("a", 64)
	migrationPool, err := platformpostgres.Open(t.Context(), cfg.PostgresControlPlaneConfig().Migrator)
	if err != nil {
		t.Fatalf("open explicit control migrator: %v", err)
	}
	if err := applyPostgresControlPlaneMigrations(t.Context(), migrationPool); err != nil {
		migrationPool.Close()
		t.Fatalf("apply explicit control migrations: %v", err)
	}
	migrationPool.Close()

	target, targetErr := BuildProduction(t.Context(), cfg)
	if targetErr != nil {
		t.Fatalf("build production application on fresh unclaimed state: %v", targetErr)
	}
	if target == nil {
		t.Fatal("production application is nil")
	}
	if err := target.Start(t.Context()); err != nil {
		t.Fatalf("start production application on fresh unclaimed state: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()
	target.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("fresh production readiness status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if err := target.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown production application: %v", err)
	}

	// Re-open the admitted pools to inspect ownership and graph identity. The
	// migrator must be consumed and closed before serving authorities retain
	// runtime/maintenance pools.
	lifecycle, err := openPostgresControlPlane(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open admitted PostgreSQL control plane: %v", err)
	}
	runtimePool := lifecycle.RuntimePool()
	maintenancePool := lifecycle.MaintenancePool()
	ducklakePool := lifecycle.DuckLakePool()
	if lifecycle.pools.Migrator != nil {
		t.Fatal("control-plane lifecycle retained the privileged migrator pool")
	}
	if runtimePool == nil || maintenancePool == nil || ducklakePool == nil {
		t.Fatalf("admitted lifecycle pools = runtime %p, maintenance %p, DuckLake %p; want all serving pools", runtimePool, maintenancePool, ducklakePool)
	}
	if runtimePool == maintenancePool || runtimePool == ducklakePool {
		t.Fatal("control and DuckLake authorities unexpectedly share pool identity")
	}
	defer lifecycle.Stop(context.Background())
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatalf("start admitted PostgreSQL lifecycle: %v", err)
	}
	assertProductionAdmissionPoolIdentity(t, runtimePool, control.Name, runtime.Name)
	assertProductionAdmissionPoolIdentity(t, maintenancePool, control.Name, maintenance.Name)
	assertProductionAdmissionPoolIdentity(t, ducklakePool, ducklake.Name, ducklakeRuntime.Name)

	instanceID, err := postgresauthority.ResolveInstanceIdentity(t.Context(), runtimePool, "prod")
	if err != nil {
		t.Fatalf("resolve native instance identity: %v", err)
	}
	if strings.TrimSpace(instanceID) == "" {
		t.Fatal("resolved native instance identity is empty")
	}
	graph, err := postgresauthority.NewPostgresAuthorityGraph(runtimePool, maintenancePool, postgresauthority.PostgresAuthorityGraphOptions{TargetID: instanceID, FingerprintKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatalf("compose native PostgreSQL authority graph: %v", err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("validate admitted native PostgreSQL authority graph: %v", err)
	}
	if graph.Access.DB() != runtimePool || graph.DeploymentRepository.DB() != runtimePool || graph.DuckLakeControlLedger.DB() != runtimePool {
		t.Fatal("native authority graph split canonical runtime repository identity")
	}
	if graph.RefreshJobs == nil || graph.RefreshJobs.Jobs != graph.Jobs || graph.RefreshJobs.Refresh != graph.Refresh {
		t.Fatal("refresh dispatch adapter does not preserve canonical Jobs and Refresh authority identity")
	}
	if graph.RefreshCancelAudit == nil || graph.RefreshCancelAudit.Audit != graph.AccessAudit {
		t.Fatal("refresh cancellation audit adapter does not preserve canonical Access audit identity")
	}

	if _, err := ducklakeAdmin.Exec(t.Context(), "REVOKE USAGE ON SCHEMA "+metadataSchema+" FROM "+ducklakeRuntimeName); err != nil {
		t.Fatalf("revoke DuckLake runtime metadata privilege: %v", err)
	}
	if err := lifecycle.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "DuckLake runtime admission") {
		t.Fatalf("lifecycle accepted missing DuckLake metadata privilege: %v", err)
	}
	if _, err := ducklakeAdmin.Exec(t.Context(), "GRANT USAGE ON SCHEMA "+metadataSchema+" TO "+ducklakeRuntimeName); err != nil {
		t.Fatalf("restore DuckLake runtime metadata privilege: %v", err)
	}

	controlAdmin, err := pgx.Connect(t.Context(), control.AdminURL())
	if err != nil {
		t.Fatalf("open control admission administrator: %v", err)
	}
	defer controlAdmin.Close(context.Background())
	runtimeName := pgx.Identifier{runtime.Name}.Sanitize()
	if _, err := controlAdmin.Exec(t.Context(), "REVOKE SELECT ON recovery.recovery_set FROM "+runtimeName); err != nil {
		t.Fatalf("revoke runtime recovery privilege: %v", err)
	}
	if err := lifecycle.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "control runtime admission") {
		t.Fatalf("lifecycle accepted missing runtime privilege: %v", err)
	}
	if _, err := controlAdmin.Exec(t.Context(), "GRANT SELECT ON recovery.recovery_set TO "+runtimeName); err != nil {
		t.Fatalf("restore runtime recovery privilege: %v", err)
	}
	if _, err := controlAdmin.Exec(t.Context(), "ALTER EXTENSION pgcrypto SET SCHEMA public"); err != nil {
		t.Fatalf("move required extension for negative admission test: %v", err)
	}
	if err := lifecycle.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "control runtime admission") {
		t.Fatalf("lifecycle accepted required extension in wrong schema: %v", err)
	}

	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("stop admitted PostgreSQL lifecycle: %v", err)
	}
	if err := runtimePool.Ping(context.Background()); err == nil {
		t.Fatal("runtime pool remained usable after lifecycle Stop; lifecycle does not own serving pool shutdown")
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatalf("idempotent lifecycle Stop: %v", err)
	}
}

func productionAdmissionTLSURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	query.Set("sslmode", "require")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestRequiresDuckLakeAdmissionDistinguishesDevelopmentBootstrapSentinel(t *testing.T) {
	tests := []struct {
		name       string
		production bool
		poolID     string
		digest     string
		want       bool
	}{
		{name: "development migration bootstrap", poolID: developmentPoolSentinelID, digest: developmentPoolSentinelDigest, want: false},
		{name: "development admitted pool", poolID: "sha256:" + strings.Repeat("a", 64), digest: "sha256:" + strings.Repeat("b", 64), want: true},
		{name: "development partial identity fails closed", poolID: developmentPoolSentinelID, digest: "", want: true},
		{name: "production always verifies", production: true, poolID: developmentPoolSentinelID, digest: developmentPoolSentinelDigest, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiresDuckLakeAdmission(config.Config{Production: tt.production, DeliveryPhysicalPoolID: tt.poolID, DeliveryPhysicalPoolCompatibilityDigest: tt.digest}); got != tt.want {
				t.Fatalf("requiresDuckLakeAdmission() = %t, want %t", got, tt.want)
			}
		})
	}
}

func assertProductionAdmissionPoolIdentity(t *testing.T, pool *platformpostgres.Pool, database, role string) {
	t.Helper()
	if got, err := pool.CurrentDatabase(t.Context()); err != nil || got != database {
		t.Fatalf("pool database identity = %q (err=%v), want %q", got, err, database)
	}
	var got string
	if err := pool.QueryRow(t.Context(), "SELECT current_user").Scan(&got); err != nil {
		t.Fatalf("read pool role identity: %v", err)
	}
	if got != role {
		t.Fatalf("pool role identity = %q, want %q", got, role)
	}
}
