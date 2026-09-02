package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform"
)

func repositoryCompatibility() physicalpool.Compatibility {
	return physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0",
		CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "s3",
		ObjectNamingContract: "uuidv7:v1",
	}
}

func repositoryPool(t *testing.T, location string) physicalpool.PhysicalPool {
	t.Helper()
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: location, StorageNamespace: "tenant-a", Region: "us-east-1",
		Tenant: "tenant-a", EncryptionDomain: "encryption-a", IsolationBoundary: "target-a", EncryptionKeyRef: "kms:key-1",
		CredentialReference: "credential:warehouse", RetentionAuthority: "target-a-retention",
		RetentionPolicy: physicalpool.RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300, BuildGracePeriodSeconds: 60},
		Compatibility:   repositoryCompatibility(),
	})
	if err != nil {
		t.Fatalf("new physical pool: %v", err)
	}
	return pool
}

func repositoryEvidence(t *testing.T, compatibility physicalpool.Compatibility, version, check string) physicalpool.Evidence {
	t.Helper()
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: version,
		Checks: []physicalpool.EvidenceCheck{{ID: check, Passed: true}},
	})
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	return evidence
}

func repositoryStore(t *testing.T) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "physical-pool.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepository(store.SQLDB())
}

func TestAdmissionRestartRoundTripAndIdempotentRetry(t *testing.T) {
	store, repo := repositoryStore(t)
	pool := repositoryPool(t, "s3://warehouse")
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatalf("create physical pool: %v", err)
	}
	evidence := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "same-table-write")
	admission, err := repo.Admit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatalf("admit physical pool: %v", err)
	}
	retry, err := repo.Admit(t.Context(), pool, evidence)
	if err != nil || retry != admission {
		t.Fatalf("idempotent admission = %#v/%v, want %#v", retry, err, admission)
	}
	contract, err := repo.LoadAdmissionContract(t.Context(), pool.ID, pool.Compatibility)
	if err != nil {
		t.Fatalf("load admission: %v", err)
	}
	if !contract.Pool.Admitted || contract.Pool.ID != pool.ID || contract.Evidence.Digest != evidence.Digest {
		t.Fatalf("contract = %#v", contract)
	}
	if contract.Pool.Identity.CredentialReference != "credential:warehouse" {
		t.Fatalf("credential reference not retained as opaque metadata")
	}
	if strings.Contains(contract.Pool.CanonicalIdentity(), "secret-value") {
		t.Fatal("contract leaked a credential value")
	}

	// The same database handle models a process restart: all admission state is
	// read from SQLite and verified again rather than relying on process memory.
	restarted := NewRepository(store.SQLDB())
	restartedContract, err := restarted.LoadAdmissionContract(t.Context(), pool.ID)
	if err != nil {
		t.Fatalf("restart load admission: %v", err)
	}
	if restartedContract.Admission != contract.Admission || restartedContract.Evidence.Digest != contract.Evidence.Digest {
		t.Fatalf("restart contract differs: %#v vs %#v", restartedContract, contract)
	}
}

func TestCreateAndAdmitIsExplicitBootstrapAndMigrationPath(t *testing.T) {
	_, repo := repositoryStore(t)
	pool := repositoryPool(t, filepath.Join(t.TempDir(), "bootstrap-pool"))
	evidence := repositoryEvidence(t, pool.Compatibility, "lea-414/v1", "shared-pool-conformance")

	created, admission, err := repo.CreateAndAdmit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatalf("create and admit: %v", err)
	}
	if !created.Admitted || created.AdmissionDigest != evidence.Digest || admission.EvidenceDigest != evidence.Digest {
		t.Fatalf("bootstrap result = %#v/%#v", created, admission)
	}
	retry, retryAdmission, err := repo.MigrateAndAdmit(t.Context(), pool, evidence)
	if err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	if retry.ID != created.ID || retryAdmission != admission {
		t.Fatalf("migration retry changed identity: %#v/%#v", retry, retryAdmission)
	}

	// A pool row by itself is not an admission. The explicit helper is the only
	// supported transition into a writer/reader-usable contract.
	contract, err := repo.LoadAdmissionContract(t.Context(), pool.ID, pool.Compatibility)
	if err != nil || !contract.Pool.Admitted {
		t.Fatalf("bootstrap contract = %#v/%v", contract, err)
	}
}

func TestCreateAndAdmitRollsBackPoolWhenEvidenceFails(t *testing.T) {
	store, repo := repositoryStore(t)
	pool := repositoryPool(t, filepath.Join(t.TempDir(), "atomic-bootstrap"))
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: pool.Compatibility, ConformanceVersion: "lea-414/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "failed-check", Passed: false, ObservationDigest: "sha256:" + strings.Repeat("a", 64)}},
	})
	if err != nil {
		t.Fatalf("new failed evidence: %v", err)
	}
	if _, _, err := repo.CreateAndAdmit(t.Context(), pool, evidence); err == nil {
		t.Fatal("failed conformance unexpectedly admitted")
	}
	var pools, admissions int
	if err := store.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM physical_pools WHERE id = ?", pool.ID).Scan(&pools); err != nil {
		t.Fatalf("count pool rows: %v", err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM physical_pool_admissions WHERE pool_id = ?", pool.ID).Scan(&admissions); err != nil {
		t.Fatalf("count admission rows: %v", err)
	}
	if pools != 0 || admissions != 0 {
		t.Fatalf("failed bootstrap left partial state: pools=%d admissions=%d", pools, admissions)
	}
}

func TestAdmissionRuntimeUpgradeAppendsToStablePool(t *testing.T) {
	_, repo := repositoryStore(t)
	pool := repositoryPool(t, filepath.Join(t.TempDir(), "pool"))
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	first := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "global-mark")
	firstAdmission, err := repo.Admit(t.Context(), pool, first)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	upgrade := pool.Compatibility
	upgrade.DuckDBRuntime = "duckdb:1.6.0"
	upgrade.DuckLakeExtension = "ducklake:0.4.0"
	upgrade.CatalogFormat = "ducklake-catalog:v2"
	second := repositoryEvidence(t, upgrade, "lea-405/v2", "global-mark-upgrade")
	secondAdmission, err := repo.Admit(t.Context(), pool, second)
	if err != nil {
		t.Fatalf("upgrade admission: %v", err)
	}
	if firstAdmission.PoolID != secondAdmission.PoolID || firstAdmission.CompatibilityDigest == secondAdmission.CompatibilityDigest {
		t.Fatalf("upgrade did not append one stable pool: %#v %#v", firstAdmission, secondAdmission)
	}
	contract, err := repo.LoadAdmissionContract(t.Context(), pool.ID, upgrade)
	if err != nil || contract.Evidence.Digest != second.Digest {
		t.Fatalf("load upgrade = %#v/%v", contract, err)
	}
}

func TestAdmissionExactEvidenceLoadAndAmbiguousImplicitLoadFailClosed(t *testing.T) {
	_, repo := repositoryStore(t)
	pool := repositoryPool(t, filepath.Join(t.TempDir(), "pool"))
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	first := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "first-observation")
	second := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "second-observation")
	if _, err := repo.Admit(t.Context(), pool, first); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	if _, err := repo.Admit(t.Context(), pool, second); err != nil {
		t.Fatalf("second admission: %v", err)
	}
	if _, err := repo.LoadAdmissionContract(t.Context(), pool.ID); !errors.Is(err, physicalpool.ErrEvidenceInvalid) {
		t.Fatalf("implicit ambiguous load error = %v", err)
	}
	older, err := repo.LoadAdmissionByEvidence(t.Context(), pool.ID, first.Digest)
	if err != nil {
		t.Fatalf("exact older evidence load: %v", err)
	}
	if older.Evidence.Digest != first.Digest || older.Evidence.ConformanceVersion != first.ConformanceVersion {
		t.Fatalf("exact older evidence = %#v", older)
	}
}

func TestAdmissionRejectsWrongTupleRootAndRetentionAuthority(t *testing.T) {
	_, repo := repositoryStore(t)
	pool := repositoryPool(t, "s3://warehouse")
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	evidence := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "global-mark")
	wrongTuple := pool.Compatibility
	wrongTuple.StorageImplementation = "filesystem"
	wrongEvidence := repositoryEvidence(t, wrongTuple, "lea-405/v2", "global-mark")
	if _, err := repo.Admit(t.Context(), pool, wrongEvidence); !errors.Is(err, physicalpool.ErrCompatibilityMismatch) {
		t.Fatalf("wrong tuple error = %v", err)
	}
	wrongRoot := pool
	wrongRoot.Identity.StorageLocation = "s3://other-warehouse"
	wrongRoot.ID = pool.ID
	if _, err := repo.Admit(t.Context(), wrongRoot, evidence); !errors.Is(err, physicalpool.ErrInvalidPool) {
		t.Fatalf("wrong root error = %v", err)
	}
	wrongAuthority := pool
	wrongAuthority.Identity.RetentionAuthority = "other-retention-authority"
	wrongAuthority.ID = pool.ID
	if _, err := repo.Admit(t.Context(), wrongAuthority, evidence); !errors.Is(err, physicalpool.ErrInvalidPool) {
		t.Fatalf("wrong retention authority error = %v", err)
	}
}

func TestLoadAdmissionFailsClosedOnTamperedEvidenceAndMissingAdmission(t *testing.T) {
	store, repo := repositoryStore(t)
	pool := repositoryPool(t, "s3://warehouse")
	if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if _, err := repo.LoadAdmissionContract(t.Context(), pool.ID); !errors.Is(err, physicalpool.ErrPoolNotAdmitted) {
		t.Fatalf("missing admission error = %v", err)
	}
	evidence := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "global-mark")
	if _, err := repo.Admit(t.Context(), pool, evidence); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `DROP TRIGGER physical_pool_admissions_append_only_update`); err != nil {
		t.Fatalf("drop test trigger: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE physical_pool_admissions SET evidence_json = '{"compatibility":{},"conformance_version":"tampered","checks":[],"digest":"sha256:' || printf('%064d', 4) || '"}' WHERE pool_id = ?`, pool.ID); err != nil {
		t.Fatalf("tamper evidence: %v", err)
	}
	if _, err := repo.LoadAdmissionContract(t.Context(), pool.ID); !errors.Is(err, physicalpool.ErrEvidenceInvalid) {
		t.Fatalf("tampered evidence error = %v", err)
	}
}

func TestLocalAndS3PoolsHaveIndependentCanonicalIdentities(t *testing.T) {
	_, repo := repositoryStore(t)
	local := repositoryPool(t, filepath.Join(t.TempDir(), "local-pool"))
	remote := repositoryPool(t, "s3://warehouse")
	if local.ID == remote.ID {
		t.Fatal("local and S3 pool IDs unexpectedly match")
	}
	for _, pool := range []physicalpool.PhysicalPool{local, remote} {
		if _, err := repo.CreatePhysicalPool(t.Context(), pool); err != nil {
			t.Fatalf("create %s pool: %v", pool.Identity.StorageLocation, err)
		}
		evidence := repositoryEvidence(t, pool.Compatibility, "lea-405/v1", "global-mark")
		if _, err := repo.Admit(t.Context(), pool, evidence); err != nil {
			t.Fatalf("admit %s pool: %v", pool.Identity.StorageLocation, err)
		}
		if _, err := repo.LoadAdmissionContract(t.Context(), pool.ID); err != nil {
			t.Fatalf("load %s pool: %v", pool.Identity.StorageLocation, err)
		}
	}
}
