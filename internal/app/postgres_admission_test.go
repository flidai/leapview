package app

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	extensionfixture "github.com/flidai/leapview/internal/app/testing/extensionfixture"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

// TestPostgres18ProductionAdmissionProvesNativeGraphBeforeSentinel exercises
// the real production bootstrap against PostgreSQL 18. BuildProduction must
// still stop at the intentional composition sentinel. The same admitted
// baseline must nevertheless support the native authority graph with exact,
// identity-preserving pools.
func TestPostgres18ProductionAdmissionProvesNativeGraphBeforeSentinel(t *testing.T) {
	h := postgrestest.StartTLS(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "admission-migrator", Login: true})
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "admission-runtime", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "admission-maintenance", Login: true})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	ducklakeRuntime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_runtime", Password: "admission-ducklake", Login: true})
	ducklakeMaintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_maintenance", Password: "admission-ducklake-maintenance", Login: true})
	h.GrantRole(t, owner, migrator)

	control := h.NewDatabase(t, "leapview_production_admission_control")
	ducklake := h.NewDatabase(t, "leapview_production_admission_ducklake")
	h.GrantDatabase(t, control.Name, owner, "CREATE")
	h.GrantDatabase(t, control.Name, migrator, "CONNECT", "CREATE")
	h.GrantDatabase(t, control.Name, runtime, "CONNECT")
	h.GrantDatabase(t, control.Name, maintenance, "CONNECT")
	h.GrantDatabase(t, ducklake.Name, ducklakeRuntime, "CONNECT", "CREATE", "TEMPORARY")
	h.GrantDatabase(t, ducklake.Name, ducklakeMaintenance, "CONNECT", "CREATE", "TEMPORARY")

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
	cfg.QueryResultMaxRows = 10_000
	cfg.QueryResultMaxBytes = 32 << 20
	cfg.QueryCacheRuntimeMaxEntries = 16
	cfg.QueryCacheRuntimeMaxBytes = 4 << 20
	cfg.QueryCacheNodeMaxEntries = 64
	cfg.QueryCacheNodeMaxBytes = 16 << 20
	cfg.CSRFKey = strings.Repeat("c", 32)
	cfg.TokenHashKey = strings.Repeat("t", 32)
	cfg.AllowedHosts = "localhost"
	cfg.Environment = "prod"
	cfg.DeliveryPhysicalPoolID = "sha256:" + strings.Repeat("b", 64)
	cfg.DeliveryPhysicalPoolCompatibilityDigest = "sha256:" + strings.Repeat("a", 64)

	// The direct target builder exercises the complete native graph while the
	// public production gate remains in place. A fresh install has no claim or
	// delivery target yet, so startup must still succeed without fabricating an
	// active project or generation.
	target, targetErr := buildPostgresProductionTarget(t.Context(), cfg)
	if targetErr != nil {
		t.Fatalf("full PostgreSQL target composition on fresh unclaimed state: %v", targetErr)
	}
	if target == nil {
		t.Fatal("full PostgreSQL target composition returned a nil application without an error")
	}
	if err := target.Start(t.Context()); err != nil {
		t.Fatalf("start directly composed PostgreSQL target on fresh unclaimed state: %v", err)
	}
	if err := target.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown directly composed PostgreSQL target: %v", err)
	}

	// BuildProduction performs the exact process admission work and then
	// refuses to expose the incomplete target graph. The sentinel is part of
	// the contract until canonical delivery/retention are fully connected.
	_, err := BuildProduction(t.Context(), cfg)
	if err == nil || !errors.Is(err, errPostgresProductionCompositionIncomplete) {
		t.Fatalf("BuildProduction error = %v, want intentional PostgreSQL composition sentinel", err)
	}
	if !strings.Contains(err.Error(), "canonical plan/build/seal delivery") || !strings.Contains(err.Error(), "native retention") {
		t.Fatalf("BuildProduction sentinel diagnostic = %v, want remaining target prerequisites", err)
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
	if graph.Bootstrap != graph.Settings || graph.Access.DB() != runtimePool || graph.DeploymentRepository.DB() != runtimePool || graph.DuckLakeControlLedger.DB() != runtimePool {
		t.Fatal("native authority graph split canonical runtime repository identity")
	}
	if graph.RefreshJobs == nil || graph.RefreshJobs.Jobs != graph.Jobs || graph.RefreshJobs.Refresh != graph.Refresh {
		t.Fatal("refresh dispatch adapter does not preserve canonical Jobs and Refresh authority identity")
	}
	if graph.RefreshCancelAudit == nil || graph.RefreshCancelAudit.Audit != graph.AccessAudit {
		t.Fatal("refresh cancellation audit adapter does not preserve canonical Access audit identity")
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
