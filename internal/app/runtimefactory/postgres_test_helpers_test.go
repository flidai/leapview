package runtimefactory

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

// deliveryCredentialTestContract creates a fully admitted S3 contract for
// tests that exercise target-owned credential guards in the runtime seam.
func deliveryCredentialTestContract(t *testing.T) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: "sha256:" + strings.Repeat("a", 64)})
	}
	p, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://bucket/prefix", StorageNamespace: "delivery", EncryptionDomain: "target", IsolationBoundary: "target", RetentionAuthority: "target", RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 300, OrphanGracePeriodSeconds: 3600}, Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := p.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	return &ducklake.PoolContract{Pool: p, Tuple: tuple, Admission: admission, Evidence: evidence}
}
