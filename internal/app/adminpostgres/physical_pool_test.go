package adminpostgres

import (
	"bytes"
	"context"
	"strings"
	"testing"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/config"
)

func testNativePoolBootstrapRequest(t *testing.T, apply bool) adminoffline.PhysicalPoolBootstrapRequest {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:1.0.0", CatalogFormat: "ducklake:1.0",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: "sha256:" + strings.Repeat("a", 64)})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: compatibility, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	return adminoffline.PhysicalPoolBootstrapRequest{Apply: apply, Evidence: evidence, Pool: physicalpool.PoolIdentity{
		StorageLocation: t.TempDir(), StorageNamespace: "tenant-a", Region: "local", Tenant: "tenant-a",
		EncryptionDomain: "encryption-a", IsolationBoundary: "production-a", RetentionAuthority: "production-a-retention",
		RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60},
		Compatibility:   compatibility,
	}}
}

func testCatalogUpgradeRequest(t *testing.T, apply bool) admincli.CatalogUpgradeRequest {
	t.Helper()
	bootstrap := testNativePoolBootstrapRequest(t, apply)
	return admincli.CatalogUpgradeRequest{
		Pool: bootstrap.Pool, Evidence: bootstrap.Evidence,
		MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000001", CatalogSchemaVersion: "ducklake-schema-v2",
		RecoveryDecision: admincli.CatalogUpgradeRecoveryForwardRecovery,
		DrainVerified:    true, BackupVerified: true, Apply: apply,
	}
}

func testCatalogUpgradeConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Production: true, HomeDir: t.TempDir(), PostgresExpectedMajor: 18, PostgresRequireTLS: true,
		PostgresControlURL:                   "postgres://runtime:secret@db/control?sslmode=require",
		PostgresControlMigratorURL:           "postgres://migrator:secret@db/control?sslmode=require",
		PostgresDuckLakeURL:                  "postgres://ducklake:secret@db/ducklake?sslmode=require",
		PostgresControlUpgradeCoordinatorURL: "postgres://coordinator:secret@db/control?sslmode=require",
		PostgresDuckLakeMigratorURL:          "postgres://catalog-migrator:secret@db/ducklake?sslmode=require",
	}
}

func TestProductionPhysicalPoolBootstrapDryRunStaysReadOnly(t *testing.T) {
	request := testNativePoolBootstrapRequest(t, false)
	called, locked := false, false
	ops := New(Dependencies{
		LoadConfig:  func() (config.Config, error) { return config.Config{Production: true, HomeDir: t.TempDir()}, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) { locked = true; return &testAdminLock{}, nil },
		BootstrapPool: func(context.Context, config.Config, adminoffline.PhysicalPoolBootstrapRequest) (adminoffline.PhysicalPoolBootstrapResult, error) {
			called = true
			return adminoffline.PhysicalPoolBootstrapResult{}, nil
		},
	})
	var out bytes.Buffer
	if err := ops.BootstrapPhysicalPool(t.Context(), request, &out); err != nil {
		t.Fatal(err)
	}
	if called || locked {
		t.Fatalf("dry-run called native mutation=%t lock=%t", called, locked)
	}
	if !strings.Contains(out.String(), "pool_id: sha256:") || !strings.Contains(out.String(), "applied: false") || strings.Contains(out.String(), "secret") {
		t.Fatalf("dry-run output = %q", out.String())
	}
}

func TestProductionPhysicalPoolBootstrapRejectsIncompleteDeliveryOwnership(t *testing.T) {
	for _, field := range []string{"tenant", "region"} {
		t.Run(field, func(t *testing.T) {
			request := testNativePoolBootstrapRequest(t, false)
			switch field {
			case "tenant":
				request.Pool.Tenant = ""
			case "region":
				request.Pool.Region = ""
			}
			if _, _, err := validatePhysicalPoolBootstrap(request); err == nil {
				t.Fatalf("missing %s unexpectedly accepted", field)
			}
		})
	}
}

func TestProductionPhysicalPoolBootstrapApplyUsesNativeOwner(t *testing.T) {
	request := testNativePoolBootstrapRequest(t, true)
	pool, compatibilityDigest, err := validatePhysicalPoolBootstrap(request)
	if err != nil {
		t.Fatal(err)
	}
	called, locked := false, false
	ops := New(Dependencies{
		LoadConfig:  func() (config.Config, error) { return config.Config{Production: true, HomeDir: t.TempDir()}, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) { locked = true; return &testAdminLock{}, nil },
		BootstrapPool: func(_ context.Context, _ config.Config, got adminoffline.PhysicalPoolBootstrapRequest) (adminoffline.PhysicalPoolBootstrapResult, error) {
			called = true
			if !got.Apply || got.Evidence.Digest != request.Evidence.Digest {
				t.Fatalf("native bootstrap request = %#v", got)
			}
			return adminoffline.PhysicalPoolBootstrapResult{PoolID: pool.ID.String(), CompatibilityDigest: compatibilityDigest, EvidenceDigest: got.Evidence.Digest, ConformanceVersion: got.Evidence.ConformanceVersion, Applied: true}, nil
		},
	})
	var out bytes.Buffer
	if err := ops.BootstrapPhysicalPool(t.Context(), request, &out); err != nil {
		t.Fatal(err)
	}
	if !called || !locked || !strings.Contains(out.String(), "applied: true") {
		t.Fatalf("native apply called=%t locked=%t output=%q", called, locked, out.String())
	}
}

func TestProductionCatalogUpgradeDryRunStaysReadOnly(t *testing.T) {
	request := testCatalogUpgradeRequest(t, false)
	called, locked := false, false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return config.Config{Production: true, HomeDir: t.TempDir()}, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) {
			locked = true
			return &testAdminLock{}, nil
		},
		UpgradePool: func(context.Context, config.Config, admincli.CatalogUpgradeRequest) (admincli.CatalogUpgradeResult, error) {
			called = true
			return admincli.CatalogUpgradeResult{}, nil
		},
	})
	var out bytes.Buffer
	if err := ops.UpgradePhysicalPoolCatalog(t.Context(), request, &out); err != nil {
		t.Fatal(err)
	}
	if called || locked {
		t.Fatalf("dry-run called native mutation=%t lock=%t", called, locked)
	}
	if !strings.Contains(out.String(), "migration_id: "+request.MigrationID) || !strings.Contains(out.String(), "applied: false") || strings.Contains(out.String(), "secret") {
		t.Fatalf("dry-run output = %q", out.String())
	}
}

func TestProductionCatalogUpgradeApplyUsesFencedNativeOwner(t *testing.T) {
	request := testCatalogUpgradeRequest(t, true)
	pool, _, err := validateCatalogUpgradeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testCatalogUpgradeConfig(t)
	called, locked := false, false
	ops := New(Dependencies{
		LoadConfig: func() (config.Config, error) { return cfg, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) {
			locked = true
			return &testAdminLock{}, nil
		},
		UpgradePool: func(_ context.Context, _ config.Config, got admincli.CatalogUpgradeRequest) (admincli.CatalogUpgradeResult, error) {
			called = true
			if got.MigrationID != request.MigrationID || got.Evidence.Digest != request.Evidence.Digest || !got.Apply {
				t.Fatalf("native catalog upgrade request = %#v", got)
			}
			return admincli.CatalogUpgradeResult{
				MigrationID: got.MigrationID, PhysicalPoolID: pool.ID.String(), CatalogID: "ducklake:" + pool.ID.String(),
				CatalogSchemaVersion: got.CatalogSchemaVersion, RecoveryDecision: got.RecoveryDecision, Applied: true,
			}, nil
		},
	})
	var out bytes.Buffer
	if err := ops.UpgradePhysicalPoolCatalog(t.Context(), request, &out); err != nil {
		t.Fatal(err)
	}
	if !called || !locked || !strings.Contains(out.String(), "applied: true") {
		t.Fatalf("native apply called=%t locked=%t output=%q", called, locked, out.String())
	}
}

func TestProductionCatalogUpgradeRejectsDifferentCompletionEvidence(t *testing.T) {
	request := testCatalogUpgradeRequest(t, true)
	cfg := testCatalogUpgradeConfig(t)
	ops := New(Dependencies{
		LoadConfig:  func() (config.Config, error) { return cfg, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) { return &testAdminLock{}, nil },
		UpgradePool: func(context.Context, config.Config, admincli.CatalogUpgradeRequest) (admincli.CatalogUpgradeResult, error) {
			return admincli.CatalogUpgradeResult{MigrationID: request.MigrationID, PhysicalPoolID: "sha256:" + strings.Repeat("f", 64), CatalogID: "ducklake:different", CatalogSchemaVersion: request.CatalogSchemaVersion, RecoveryDecision: request.RecoveryDecision, Applied: true}, nil
		},
	})
	if err := ops.UpgradePhysicalPoolCatalog(t.Context(), request, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "different identity evidence") {
		t.Fatalf("different completion evidence error = %v", err)
	}
}
