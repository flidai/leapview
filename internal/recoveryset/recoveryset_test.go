package recoveryset

import (
	"encoding/json"
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
	s = testSet(t)
	s.Status = StatusPublished
	if err := s.Validate(); err == nil {
		t.Fatal("published set without validation pointer accepted")
	}
	s.PublishedValidationAttemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
	if err := s.Validate(); err != nil {
		t.Fatalf("published set with validation pointer rejected: %v", err)
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
	a.PublishedValidationAttemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
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
	set := testSet(t)
	const attemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
	envelope, err := NewValidationEvidenceEnvelope(set, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := NewValidationResult(envelope, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	evidence := append([]byte(nil), base.Evidence...)
	normalized, err := base.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized.Evidence) != string(evidence) {
		t.Fatalf("canonical evidence changed = %s", normalized.Evidence)
	}
	base.Evidence = []byte(`[]`)
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("array evidence error = %v", err)
	}
	base.Evidence = append(append([]byte(nil), evidence[:len(evidence)-1]...), []byte(`,"unknown":true}`)...)
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown evidence field error = %v", err)
	}
	base.Evidence = []byte(strings.Replace(string(evidence), `"set_id"`, `"SET_ID"`, 1))
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("case-variant evidence field error = %v", err)
	}
	base.Evidence = append(append([]byte(nil), evidence[:len(evidence)-1]...), []byte(`,"set_id":"018f3f83-7b2f-7b37-9f9e-000000000010"}`)...)
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate evidence field error = %v", err)
	}
	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(evidence, &rawEnvelope); err != nil {
		t.Fatal(err)
	}
	var rawRoots []map[string]json.RawMessage
	if err := json.Unmarshal(rawEnvelope["object_roots"], &rawRoots); err != nil {
		t.Fatal(err)
	}
	missingFrontier := -1
	for i, root := range rawRoots {
		var frontier string
		if err := json.Unmarshal(root["provider_recovery_frontier"], &frontier); err != nil {
			t.Fatal(err)
		}
		if frontier == "" {
			missingFrontier = i
			break
		}
	}
	if missingFrontier < 0 {
		t.Fatal("test envelope has no local object root")
	}
	delete(rawRoots[missingFrontier], "provider_recovery_frontier")
	rawEnvelope["object_roots"], err = json.Marshal(rawRoots)
	if err != nil {
		t.Fatal(err)
	}
	base.Evidence, err = json.Marshal(rawEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing provider frontier field error = %v", err)
	}
	rawRoots[missingFrontier]["provider_recovery_frontier"] = json.RawMessage("null")
	rawEnvelope["object_roots"], err = json.Marshal(rawRoots)
	if err != nil {
		t.Fatal(err)
	}
	base.Evidence, err = json.Marshal(rawEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("null provider frontier field error = %v", err)
	}
	mismatchedEnvelope := envelope
	mismatchedEnvelope.AttemptID = "018f3f83-7b2f-7b37-9f9e-000000000021"
	mismatchedResult, err := NewValidationResult(mismatchedEnvelope, base.RecordedAt)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedResult.AttemptID = attemptID
	if _, err := mismatchedResult.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched evidence attempt ID error = %v", err)
	}
	base.Evidence = evidence
	base.ResultDigest = testDigest('8')
	if _, err := base.Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestValidationEvidenceEnvelopeBindsExactFrontier(t *testing.T) {
	set := testSet(t)
	const attemptID = "018f3f83-7b2f-7b37-9f9e-000000000020"
	envelope, err := NewValidationEvidenceEnvelope(set, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.ValidateFor(set, attemptID); err != nil {
		t.Fatalf("exact envelope rejected: %v", err)
	}
	mutated := envelope
	mutated.ObjectRoots = append([]ValidationEvidenceObjectRoot(nil), envelope.ObjectRoots...)
	mutated.ObjectRoots[0].VersionID = "other-version"
	if err := mutated.ValidateFor(set, attemptID); err == nil {
		t.Fatal("object version drift accepted")
	}
	mutated = envelope
	mutated.ObjectRoots = append([]ValidationEvidenceObjectRoot(nil), envelope.ObjectRoots...)
	remoteIndex := -1
	for i, root := range mutated.ObjectRoots {
		if validationRemoteObjectRoot(root.URI) {
			remoteIndex = i
			break
		}
	}
	if remoteIndex < 0 {
		t.Fatal("test envelope has no remote object root")
	}
	mutated.ObjectRoots[remoteIndex].ProviderRecoveryFrontier = ""
	if err := mutated.Validate(); err == nil {
		t.Fatal("remote root without provider frontier accepted")
	}
	mutated.ObjectRoots[remoteIndex].URI = "s3://bucket/%zz"
	if err := mutated.Validate(); err == nil {
		t.Fatal("malformed escaped object root URI accepted")
	}
	mutated.ObjectRoots[remoteIndex].URI = "/tmp/local-root"
	if err := mutated.Validate(); err != nil {
		t.Fatalf("explicit local root without provider frontier rejected: %v", err)
	}
	mutated = envelope
	mutated.ClusterPoints = append([]ClusterRecoveryPoint(nil), envelope.ClusterPoints...)
	mutated.ClusterPoints[0].RecoveryIdentity = "lsn:0/999"
	if err := mutated.Validate(); err == nil {
		t.Fatal("shared-cluster recovery-point drift accepted")
	}
}
