package postgres

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testCompatibility() physicalpool.Compatibility {
	return physicalpool.Compatibility{DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
}

func testPool(t *testing.T, location string) physicalpool.PhysicalPool {
	t.Helper()
	p, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: location, StorageNamespace: "tenant-a", Region: "us-east-1", Tenant: "tenant-a", IsolationBoundary: "target-a", EncryptionKeyRef: "kms:key-ref", CredentialReference: "credential:warehouse", RetentionAuthority: "target-a-retention", RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60}, Compatibility: testCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testEvidence(t *testing.T, c physicalpool.Compatibility, version, check string) physicalpool.Evidence {
	t.Helper()
	e, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: c, ConformanceVersion: version, Checks: []physicalpool.EvidenceCheck{{ID: check, Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type ownershipMarker struct {
	fail     bool
	acquired int
}

func (m *ownershipMarker) AcquireNamespaceOwnership(_ context.Context, _ physicalpool.OwnershipClaim) error {
	m.acquired++
	if m.fail {
		return physicalpool.ErrOwnershipConflict
	}
	return nil
}

func (m *ownershipMarker) VerifyNamespaceOwnership(context.Context, physicalpool.OwnershipClaim) error {
	if m.fail {
		return physicalpool.ErrOwnershipConflict
	}
	return nil
}

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "physical_pool_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdmissionRestartRoundTripAndIdempotentRetry(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "warehouse"))
	evidence := testEvidence(t, pool.Compatibility, "lea-001/v1", "marker")
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	a, err := repo.Admit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := repo.Admit(t.Context(), pool, evidence); err != nil || retry != a {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	contract, err := New(db).LoadAdmissionContract(t.Context(), pool.ID, pool.Compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Pool.ID != pool.ID || !contract.Pool.Admitted || contract.Evidence.Digest != evidence.Digest {
		t.Fatalf("contract=%#v", contract)
	}
	byEvidence, err := repo.LoadAdmissionByEvidence(t.Context(), pool.ID, evidence.Digest)
	if err != nil || byEvidence.Admission != contract.Admission || byEvidence.Evidence.Digest != evidence.Digest {
		t.Fatalf("evidence load=%#v err=%v", byEvidence, err)
	}
}

func TestLoadAdmissionByEvidenceRejectsPersistedCorruption(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "corruption"))
	evidence := testEvidence(t, pool.Compatibility, "lea-001/v1", "corruption")
	if _, _, err := repo.CreateAndAdmit(t.Context(), pool, evidence); err != nil {
		t.Fatal(err)
	}
	// Simulate an administrator-level persisted corruption. The immutable
	// trigger is intentionally removed only in this adversarial test; the
	// loader must still reject the row based on its content-addressed fields.
	if _, err := db.Exec(t.Context(), `DROP TRIGGER physical_pool_admissions_immutable ON physical_pool.physical_pool_admissions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE physical_pool.physical_pool_admissions SET compatibility_digest=$1 WHERE pool_id=$2 AND evidence_digest=$3`, "sha256:"+strings.Repeat("a", 64), pool.ID, evidence.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadAdmissionByEvidence(t.Context(), pool.ID, evidence.Digest); !errors.Is(err, physicalpool.ErrCompatibilityMismatch) {
		t.Fatalf("corrupt evidence err=%v", err)
	}
}

func TestNamespaceCollisionAndAtomicRollback(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	first := testPool(t, filepath.Join(t.TempDir(), "same"))
	second := first
	second.Identity.RetentionAuthority = "other-retention"
	second, err := physicalpool.NewPhysicalPool(second.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePhysicalPool(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePhysicalPool(t.Context(), second); !errors.Is(err, physicalpool.ErrPoolMismatch) {
		t.Fatalf("namespace collision err=%v", err)
	}
	// Altering a stable field while retaining the old digest must fail before SQL.
	bad := first
	bad.Identity.RetentionAuthority = "other"
	if _, err := repo.CreatePhysicalPool(t.Context(), bad); !errors.Is(err, physicalpool.ErrInvalidPool) {
		t.Fatalf("bad identity err=%v", err)
	}
	failed, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: first.Compatibility, ConformanceVersion: "lea-001/v1", Checks: []physicalpool.EvidenceCheck{{ID: "failed", Passed: false}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateAndAdmit(t.Context(), testPool(t, filepath.Join(t.TempDir(), "rollback")), failed); err == nil {
		t.Fatal("failed evidence admitted")
	}
}

func TestConcurrentAdmissionAndLeaseFencing(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "race"))
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	evidence := testEvidence(t, pool.Compatibility, "lea-001/v1", "race")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := repo.Admit(t.Context(), pool, evidence); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	token, err := repo.AcquireNamespaceDeletionLease(t.Context(), "owner-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireNamespaceDeletionLease(t.Context(), "owner-b", time.Minute); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("lease conflict=%v", err)
	}
	if err := repo.VerifyNamespaceDeletionLease(t.Context(), "owner-b", token); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("stale owner=%v", err)
	}
	if err := repo.VerifyNamespaceDeletionLease(t.Context(), "owner-a", token); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseNamespaceDeletionLease(t.Context(), "owner-a", token); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireNamespaceDeletionLease(t.Context(), "owner-b", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseNamespaceDeletionLease(t.Context(), "owner-b", "not-a-token"); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("stale release=%v", err)
	}
}

func TestOwnershipClaimsRequireExactAdmission(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "claim"))
	evidence := testEvidence(t, pool.Compatibility, "lea-001/v1", "claim")
	if _, _, err := repo.CreateAndAdmit(t.Context(), pool, evidence); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(t.Context(), `INSERT INTO physical_pool.namespace_ownership_claims(pool_id,compatibility_digest,evidence_digest,owner_id) VALUES($1,$2,$3,'owner')`, pool.ID, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64))
	if err == nil {
		t.Fatal("orphan ownership claim unexpectedly accepted")
	}
}

func TestExternalOwnershipMarkerIsRequiredAndClaimIsIdempotent(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "owned"))
	evidence := testEvidence(t, pool.Compatibility, "lea-001/v1", "ownership")
	failing := &ownershipMarker{fail: true}
	if _, _, err := repo.CreateAndAdmitWithOwnership(t.Context(), pool, evidence, "owner-a", failing); !errors.Is(err, physicalpool.ErrOwnershipConflict) {
		t.Fatalf("failed marker err=%v", err)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM physical_pool.physical_pools`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed marker persisted %d pools", count)
	}
	marker := &ownershipMarker{}
	created, admission, err := repo.CreateAndAdmitWithOwnership(t.Context(), pool, evidence, "owner-a", marker)
	if err != nil {
		t.Fatal(err)
	}
	if marker.acquired != 1 || created.ID != pool.ID || admission.EvidenceDigest != evidence.Digest {
		t.Fatalf("ownership result=%#v %#v marker=%d", created, admission, marker.acquired)
	}
	if _, _, err := repo.CreateAndAdmitWithOwnership(t.Context(), pool, evidence, "owner-a", marker); err != nil {
		t.Fatalf("same-owner retry=%v", err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM physical_pool.namespace_ownership_claims`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("ownership claims=%d, want one", count)
	}
	var nilRepo *Repository
	nilMarker := &ownershipMarker{}
	if _, _, err := nilRepo.CreateAndAdmitWithOwnership(t.Context(), pool, evidence, "owner-a", nilMarker); !errors.Is(err, physicalpool.ErrInvalidPool) {
		t.Fatalf("nil repository err=%v", err)
	}
	if nilMarker.acquired != 0 {
		t.Fatal("nil repository acquired external ownership marker")
	}
}

func TestExpiredLeaseCannotBeReleasedAndCanBeTakenOver(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	token, err := repo.AcquireNamespaceDeletionLease(t.Context(), "owner-a", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := repo.ReleaseNamespaceDeletionLease(t.Context(), "owner-a", token); !errors.Is(err, physicalpool.ErrDeletionLeaseConflict) {
		t.Fatalf("expired release=%v", err)
	}
	if _, err := repo.AcquireNamespaceDeletionLease(t.Context(), "owner-b", time.Minute); err != nil {
		t.Fatalf("takeover=%v", err)
	}
}

func TestAdmissionUpgradeAppendsStablePool(t *testing.T) {
	db := testDB(t)
	repo := New(db)
	pool := testPool(t, filepath.Join(t.TempDir(), "upgrade"))
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	first := testEvidence(t, pool.Compatibility, "lea-001/v1", "first")
	if _, err := repo.Admit(t.Context(), pool, first); err != nil {
		t.Fatal(err)
	}
	upgrade := pool.Compatibility
	upgrade.DuckDBRuntime = "duckdb:1.6.0"
	second := testEvidence(t, upgrade, "lea-001/v2", "second")
	if _, err := repo.Admit(t.Context(), pool, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadAdmissionContract(t.Context(), pool.ID, upgrade)
	if err != nil || loaded.Evidence.Digest != second.Digest {
		t.Fatalf("upgrade=%#v err=%v", loaded, err)
	}
	if strings.Contains(loaded.Pool.CanonicalIdentity(), "secret-value") {
		t.Fatal("secret leaked")
	}
}
