package recoveryset

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

func testSet(t *testing.T) RecoverySet {
	t.Helper()
	compat := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatDigest, err := compat.Digest()
	if err != nil {
		t.Fatal(err)
	}
	sealID := "018f3f83-7b2f-7b37-9f9e-000000000001"
	return RecoverySet{
		ID: "018f3f83-7b2f-7b37-9f9e-000000000010", SchemaVersion: SchemaVersion,
		ClusterPoints: []ClusterRecoveryPoint{{DatabaseRole: DatabaseControl, ClusterIdentity: "cluster-a", DatabaseIdentity: "control-db", RecoveryIdentity: "lsn:0/100"}, {DatabaseRole: DatabaseDuckLake, ClusterIdentity: "cluster-a", DatabaseIdentity: "ducklake-db", RecoveryIdentity: "lsn:0/100"}},
		Delivery:      DeliveryPointer{TargetID: "target/prod", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000002", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000003", TargetRevision: 4},
		Serving:       SnapshotSeal{SealID: sealID, PhysicalPoolID: "pool-a", TenantDomain: "tenant-a", Region: "us-east", EncryptionDomain: "enc-a", ObjectNamespace: "objects/prod", CatalogDatabase: "ducklake-db", CatalogID: "catalog-a", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, DuckLakeSnapshotID: 42, RelationManifestDigest: testDigest('a'), RelationNamespace: "candidate/1", ClosureDigest: testDigest('b'), ObjectRoot: "objects/prod", ObjectRootDigest: testDigest('c'), ArtifactRoot: "artifacts/prod", ArtifactRootDigest: testDigest('d'), ServingArtifactID: "artifact-a", ServingArtifactDigest: testDigest('e'), CompiledGraphDigest: testDigest('f'), CompiledConfigDigest: testDigest('0'), SecurityDomainFingerprint: testDigest('1'), RequestDigest: testDigest('2'), PlanDigest: testDigest('3'), CompatibilityDigest: compatDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       CatalogCommit{CatalogID: "catalog-a", CatalogDatabase: "ducklake-db", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, SnapshotID: 42},
		ObjectRoots:   []ObjectRoot{{Kind: "catalog", URI: "s3://bucket/catalog", VersionID: "v9", Digest: testDigest('6'), ProviderRecoveryFrontier: "version:v9"}, {Kind: "artifact", URI: "artifacts/prod", VersionID: "v4", Digest: testDigest('7')}},
		Compatibility: compat, FenceEpoch: 3, AuditIdentity: "audit-1", Status: StatusPrepared, CreatedBy: "operator", CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("offset", 3600)),
	}
}

func testDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func TestRecoverySetValidateStrictBindings(t *testing.T) {
	s := testSet(t)
	if err := s.Validate(); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}
	s.ClusterPoints[1].RecoveryIdentity = "lsn:0/101"
	if err := s.Validate(); err == nil {
		t.Fatal("shared cluster recovery drift accepted")
	}
	s = testSet(t)
	s.Catalog.SnapshotID++
	if err := s.Validate(); err == nil {
		t.Fatal("catalog/snapshot mismatch accepted")
	}
	s = testSet(t)
	s.ObjectRoots[0].URI = "s3://bucket/../escape"
	if err := s.Validate(); err == nil {
		t.Fatal("object-root traversal accepted")
	}
}

func TestRecoverySetNormalizeAndDigest(t *testing.T) {
	a, b := testSet(t), testSet(t)
	a.ClusterPoints[0], a.ClusterPoints[1] = a.ClusterPoints[1], a.ClusterPoints[0]
	a.ObjectRoots[0], a.ObjectRoots[1] = a.ObjectRoots[1], a.ObjectRoots[0]
	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("child ordering changed digest: %s != %s", da, db)
	}
	a.Status = StatusPublished
	if !a.FrontierEqual(b) {
		t.Fatal("publication metadata changed frontier digest")
	}
	a.FrontierDigest = da
	if err := a.Validate(); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	a.FrontierDigest = testDigest('f')
	if err := a.Validate(); err == nil {
		t.Fatal("frontier digest mismatch accepted")
	}
}

func TestRecoverySetDigestRejectsInvalidFrontier(t *testing.T) {
	s := testSet(t)
	s.Delivery.TargetRevision = 0
	if _, err := s.Digest(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest invalid frontier error = %v", err)
	}
}

func TestValidationResultNormalizeRequiresJSONObject(t *testing.T) {
	base := ValidationResult{AttemptID: "018f3f83-7b2f-7b37-9f9e-000000000020", ResultDigest: testDigest('8'), Evidence: []byte(`{"b":2, "a":1}`), RecordedAt: time.Now()}
	normalized, err := base.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized.Evidence) != `{"a":1,"b":2}` {
		t.Fatalf("canonical evidence = %s", normalized.Evidence)
	}
	base.Evidence = []byte(`[]`)
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("array evidence error = %v", err)
	}
	base.Evidence = []byte(`{"outer":{"ok":1,"ok":2}}`)
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate evidence key error = %v", err)
	}
}
