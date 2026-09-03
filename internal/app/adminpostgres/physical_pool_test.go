package adminpostgres

import (
	"bytes"
	"context"
	"strings"
	"testing"

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

func TestProductionPhysicalPoolBootstrapRejectsReturnedIdentityDrift(t *testing.T) {
	request := testNativePoolBootstrapRequest(t, true)
	ops := New(Dependencies{
		LoadConfig:  func() (config.Config, error) { return config.Config{Production: true, HomeDir: t.TempDir()}, nil },
		AcquireLock: func(string) (adminoffline.Lock, error) { return &testAdminLock{}, nil },
		BootstrapPool: func(context.Context, config.Config, adminoffline.PhysicalPoolBootstrapRequest) (adminoffline.PhysicalPoolBootstrapResult, error) {
			return adminoffline.PhysicalPoolBootstrapResult{PoolID: "sha256:" + strings.Repeat("f", 64), Applied: true}, nil
		},
	})
	if err := ops.BootstrapPhysicalPool(t.Context(), request, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "different identity evidence") {
		t.Fatalf("identity drift error = %v", err)
	}
}
