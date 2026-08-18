package physicalpool

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func testCompatibility() Compatibility {
	return Compatibility{
		DuckDBRuntime:         "duckdb:1.5.4",
		DuckLakeExtension:     "ducklake:0.3.0",
		CatalogFormat:         "ducklake-catalog:v1",
		StorageImplementation: "s3",
		ObjectNamingContract:  "uuidv7:v1",
	}
}

func testPool(t *testing.T) PhysicalPool {
	t.Helper()
	p, err := NewPhysicalPool(PoolIdentity{
		StorageLocation:     "s3://warehouse",
		StorageNamespace:    "tenant-a",
		Region:              "us-east-1",
		Tenant:              "tenant-a",
		IsolationBoundary:   "target-a",
		EncryptionKeyRef:    "kms:key-1",
		CredentialReference: "credential:warehouse",
		RetentionAuthority:  "target-a-retention",
		RetentionPolicy:     RetentionPolicy{OrphanGracePeriodSeconds: 3600, ReaderGracePeriodSeconds: 300},
		Compatibility:       testCompatibility(),
	})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return p
}

func TestPhysicalPoolIdentityIsCanonicalAndNonSecret(t *testing.T) {
	left := testPool(t)
	right, err := NewPhysicalPool(PoolIdentity{
		StorageLocation: "s3://warehouse", StorageNamespace: "tenant-a", Region: "us-east-1", Tenant: "tenant-a", IsolationBoundary: "target-a", EncryptionKeyRef: "kms:key-1", CredentialReference: "credential:warehouse", RetentionAuthority: "target-a-retention", RetentionPolicy: RetentionPolicy{ReaderGracePeriodSeconds: 300, OrphanGracePeriodSeconds: 3600}, Compatibility: testCompatibility(),
	})
	if err != nil {
		t.Fatalf("new equivalent pool: %v", err)
	}
	if left.ID != right.ID || left.IdentityDigest() != right.IdentityDigest() {
		t.Fatalf("equivalent pools differ: %q/%q", left.ID, right.ID)
	}
	canonicalPath := left.Identity
	canonicalPath.StorageLocation = "S3://WAREHOUSE/"
	canonicalPool, err := NewPhysicalPool(canonicalPath)
	if err != nil || canonicalPool.ID != left.ID {
		t.Fatalf("non-canonical storage spelling changed pool identity: %q/%v", canonicalPool.ID, err)
	}
	upgrade := testCompatibility()
	upgrade.DuckDBRuntime, upgrade.DuckLakeExtension, upgrade.CatalogFormat = "duckdb:1.6.0", "ducklake:0.4.0", "ducklake-catalog:v2"
	upgraded, err := NewPhysicalPool(PoolIdentity{StorageLocation: "s3://warehouse", StorageNamespace: "tenant-a", Region: "us-east-1", Tenant: "tenant-a", IsolationBoundary: "target-a", EncryptionKeyRef: "kms:key-1", CredentialReference: "credential:warehouse", RetentionAuthority: "target-a-retention", RetentionPolicy: RetentionPolicy{ReaderGracePeriodSeconds: 300, OrphanGracePeriodSeconds: 3600}, Compatibility: upgrade})
	if err != nil || upgraded.ID != left.ID {
		t.Fatalf("runtime upgrade changed stable pool identity: %q/%v", upgraded.ID, err)
	}
	if strings.Contains(left.CanonicalIdentity(), "secret-value") {
		t.Fatal("canonical identity contains a secret value")
	}
	if _, err := NewPhysicalPool(PoolIdentity{StorageLocation: "s3://warehouse", StorageNamespace: "tenant-a", IsolationBoundary: "target-a", RetentionAuthority: "target-a-retention", Compatibility: testCompatibility(), CredentialReference: "secret-value"}); err != nil {
		t.Fatalf("credential references are non-secret metadata and should be accepted: %v", err)
	}
}

func TestStorageLocationValidationSupportsLocalAndObjectPools(t *testing.T) {
	for _, location := range []string{"/var/lib/leapview/pool", "file:///var/lib/leapview/pool", "s3://bucket/prefix", "gs://bucket/prefix"} {
		identity := testPool(t).Identity
		identity.StorageLocation = location
		if _, err := NewPhysicalPool(identity); err != nil {
			t.Errorf("location %q rejected: %v", location, err)
		}
	}
	for _, location := range []string{"s3://user:pass@bucket/prefix", "s3://bucket/prefix?access_key=secret", "s3://bucket/prefix#fragment", "file://user@/tmp/pool"} {
		identity := testPool(t).Identity
		identity.StorageLocation = location
		if _, err := NewPhysicalPool(identity); !errors.Is(err, ErrInvalidPool) {
			t.Errorf("location %q error = %v, want ErrInvalidPool", location, err)
		}
	}
}

func TestPoolAdmissionRequiresMatchingEvidence(t *testing.T) {
	pool := testPool(t)
	evidence, err := NewEvidence(EvidenceInput{
		Compatibility:      pool.Compatibility,
		ConformanceVersion: "lea-405/v1",
		Checks:             []EvidenceCheck{{ID: "same-table-write", Passed: true, ObservationDigest: digestFor("same-table-write")}},
	})
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		t.Fatalf("admit pool: %v", err)
	}
	if admission.PoolID != pool.ID || admission.EvidenceDigest != evidence.Digest {
		t.Fatalf("admission = %#v", admission)
	}
	admitted, err := pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatalf("apply admission: %v", err)
	}
	if err := VerifyAdmission(admitted, evidence.Compatibility, admission, evidence); err != nil {
		t.Fatalf("verify admission: %v", err)
	}
	tamperedAdmission := admission
	tamperedAdmission.ConformanceVersion = "lea-405/tampered"
	if err := VerifyAdmission(admitted, evidence.Compatibility, tamperedAdmission, evidence); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("tampered admission error = %v, want ErrEvidenceInvalid", err)
	}
	upgrade := testCompatibility()
	upgrade.DuckDBRuntime = "duckdb:1.6.0"
	upgradeEvidence, err := NewEvidence(EvidenceInput{Compatibility: upgrade, ConformanceVersion: "lea-405/v2", Checks: []EvidenceCheck{{ID: "same-table-write", Passed: true}}})
	if err != nil {
		t.Fatalf("new upgrade evidence: %v", err)
	}
	upgradeAdmission, err := pool.Admit(upgradeEvidence)
	if err != nil {
		t.Fatalf("admit runtime upgrade to same pool: %v", err)
	}
	if upgradeAdmission.PoolID != pool.ID || upgradeAdmission.CompatibilityDigest == admission.CompatibilityDigest {
		t.Fatalf("upgrade admission did not record a new tuple: %#v", upgradeAdmission)
	}
	if upgradedPool, err := admitted.ApplyAdmission(upgradeAdmission); err != nil {
		t.Fatalf("apply upgrade admission: %v", err)
	} else if err := VerifyAdmission(upgradedPool, upgrade, upgradeAdmission, upgradeEvidence); err != nil {
		t.Fatalf("verify upgrade admission: %v", err)
	}
	wrongStorage := upgrade
	wrongStorage.StorageImplementation = "filesystem"
	wrongEvidence, err := NewEvidence(EvidenceInput{Compatibility: wrongStorage, ConformanceVersion: "lea-405/v3", Checks: []EvidenceCheck{{ID: "same-table-write", Passed: true}}})
	if err != nil {
		t.Fatalf("new wrong evidence: %v", err)
	}
	if _, err := pool.Admit(wrongEvidence); !errors.Is(err, ErrCompatibilityMismatch) {
		t.Fatalf("wrong storage tuple error = %v, want ErrCompatibilityMismatch", err)
	}

	tampered := evidence
	tampered.Checks[0].Passed = false
	if _, err := pool.Admit(tampered); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("tampered evidence error = %v, want ErrEvidenceInvalid", err)
	}

	if err := VerifyAdmission(pool, evidence.Compatibility, admission, evidence); !errors.Is(err, ErrPoolNotAdmitted) {
		t.Fatalf("unadmitted pool error = %v, want ErrPoolNotAdmitted", err)
	}
}

func TestCatalogSealAndZeroCopyBaseChild(t *testing.T) {
	pool := testPool(t)
	evidence, err := NewEvidence(EvidenceInput{Compatibility: pool.Compatibility, ConformanceVersion: "lea-405/v1", Checks: []EvidenceCheck{{ID: "global-mark", Passed: true}}})
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	base, err := NewCatalogBinding(CatalogBindingInput{PhysicalPoolID: pool.ID, CatalogDigest: digestFor("base"), ObjectKey: "catalogs/sha256/base.ducklake", SizeBytes: 42, Compatibility: pool.Compatibility, CompatibilityDigest: admission.CompatibilityDigest, EvidenceDigest: evidence.Digest})
	if err != nil {
		t.Fatalf("new base: %v", err)
	}
	base, err = base.Seal(admission)
	if err != nil {
		t.Fatalf("seal base: %v", err)
	}
	zero, err := NewCatalogBinding(CatalogBindingInput{PhysicalPoolID: pool.ID, CatalogDigest: digestFor("zero"), ObjectKey: "catalogs/sha256/zero.ducklake", SizeBytes: 0, Compatibility: pool.Compatibility, CompatibilityDigest: admission.CompatibilityDigest, EvidenceDigest: evidence.Digest})
	if err != nil {
		t.Fatalf("new zero-size binding: %v", err)
	}
	if _, err := zero.Seal(admission); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("zero-size sealed catalog error = %v, want ErrInvalidCatalog", err)
	}
	child, err := NewCatalogBinding(CatalogBindingInput{PhysicalPoolID: pool.ID, CatalogDigest: digestFor("child"), ObjectKey: "catalogs/sha256/child.ducklake", SizeBytes: 43, Compatibility: pool.Compatibility, CompatibilityDigest: admission.CompatibilityDigest, BaseCatalogDigest: base.CatalogDigest, BasePhysicalPoolID: base.PhysicalPoolID, EvidenceDigest: evidence.Digest})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := ValidateZeroCopyBaseChild(base, child); err != nil {
		t.Fatalf("zero-copy validation: %v", err)
	}
	if _, err := base.RebindPool(PoolID("sha256:" + strings.Repeat("f", 64))); !errors.Is(err, ErrSealedBinding) {
		t.Fatalf("rebind sealed catalog error = %v, want ErrSealedBinding", err)
	}
	if _, err := child.Seal(admission); err != nil {
		t.Fatalf("seal child: %v", err)
	}
}

func TestZeroCopyRejectsCrossTargetBasePool(t *testing.T) {
	basePool := testPool(t)
	otherIdentity := basePool.Identity
	otherIdentity.IsolationBoundary = "target-b"
	otherPool, err := NewPhysicalPool(otherIdentity)
	if err != nil {
		t.Fatalf("other target pool: %v", err)
	}
	evidence, err := NewEvidence(EvidenceInput{Compatibility: basePool.Compatibility, ConformanceVersion: "lea-405/v1", Checks: []EvidenceCheck{{ID: "global-mark", Passed: true}}})
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	admission, err := basePool.Admit(evidence)
	if err != nil {
		t.Fatalf("admit base: %v", err)
	}
	base, err := NewCatalogBinding(CatalogBindingInput{PhysicalPoolID: basePool.ID, CatalogDigest: digestFor("base-cross-target"), ObjectKey: "catalogs/sha256/base-cross-target.ducklake", SizeBytes: 1, Compatibility: basePool.Compatibility, CompatibilityDigest: admission.CompatibilityDigest, EvidenceDigest: evidence.Digest})
	if err != nil {
		t.Fatalf("new base: %v", err)
	}
	base, err = base.Seal(admission)
	if err != nil {
		t.Fatalf("seal base: %v", err)
	}
	child, err := NewCatalogBinding(CatalogBindingInput{PhysicalPoolID: otherPool.ID, CatalogDigest: digestFor("child-cross-target"), ObjectKey: "catalogs/sha256/child-cross-target.ducklake", SizeBytes: 1, Compatibility: basePool.Compatibility, CompatibilityDigest: admission.CompatibilityDigest, BaseCatalogDigest: base.CatalogDigest, BasePhysicalPoolID: base.PhysicalPoolID, EvidenceDigest: evidence.Digest})
	if !errors.Is(err, ErrPoolMismatch) {
		t.Fatalf("cross-target child error = %v, want ErrPoolMismatch", err)
	}
	if child != (CatalogBinding{}) {
		t.Fatal("cross-target child unexpectedly returned a binding")
	}
}

func TestValidationFailsClosedWithoutLeakingValues(t *testing.T) {
	_, err := NewPhysicalPool(PoolIdentity{StorageLocation: "s3://warehouse", StorageNamespace: "tenant-a", IsolationBoundary: "target-a", RetentionAuthority: "target-a-retention", Compatibility: Compatibility{DuckDBRuntime: "duckdb:1.5.4"}})
	if err == nil {
		t.Fatal("expected invalid tuple error")
	}
	if !errors.Is(err, ErrInvalidCompatibility) || !strings.Contains(err.Error(), "storage_implementation") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "s3://warehouse") {
		t.Fatal("diagnostic leaked storage location")
	}
}

func TestEvidenceArtifactRoundTripVerifiesCanonicalDigest(t *testing.T) {
	evidence, err := NewEvidence(EvidenceInput{
		Compatibility: testCompatibility(), ConformanceVersion: "lea-414/v1",
		Checks: []EvidenceCheck{{ID: "safe-close", Passed: true, ObservationDigest: digestFor("safe-close")}},
	})
	if err != nil {
		t.Fatalf("new evidence: %v", err)
	}
	encoded, err := MarshalEvidenceArtifact(evidence)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	decoded, err := UnmarshalEvidenceArtifact(encoded)
	if err != nil {
		t.Fatalf("unmarshal artifact: %v", err)
	}
	if !reflect.DeepEqual(decoded, evidence) {
		t.Fatalf("artifact changed evidence: %#v vs %#v", decoded, evidence)
	}
	encoded = append(encoded, []byte("{}")...)
	if _, err := UnmarshalEvidenceArtifact(encoded); err == nil {
		t.Fatal("concatenated artifact unexpectedly accepted")
	}
}

func digestFor(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest)
}
