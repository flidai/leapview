package observationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/recoveryset/observation"
	managedpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
)

type emptyProjectionSource struct{}

type expiringWriteClient struct {
	*versionedFakeS3
	afterWrite func()
}

func (c *expiringWriteClient) PutObject(ctx context.Context, input *s3.PutObjectInput, options ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	result, err := c.versionedFakeS3.PutObject(ctx, input, options...)
	if c.afterWrite != nil {
		c.afterWrite()
	}
	return result, err
}

func TestEvidenceCannotExpireDuringAcceptance(t *testing.T) {
	for _, frontier := range []bool{false, true} {
		name := "manifest"
		if frontier {
			name = "frontier"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			until := now.Add(time.Hour)
			provider := &expiringWriteClient{versionedFakeS3: newVersionedFakeS3()}
			store, err := New(provider, Config{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence", Clock: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			captured := capturedEmptyObservation(t)
			if !frontier {
				provider.afterWrite = func() { now = until.Add(time.Second) }
				_, err = store.Save(t.Context(), &captured, nil, until)
			} else {
				ref, saveErr := store.Save(t.Context(), &captured, nil, until)
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				set := regressionV2Set(t, captured.Boundary(), "sha256:"+ref.Manifest.SHA256, mustDigest(t, captured.Boundary()), "sha256:"+ref.Descriptor.SHA256)
				provider.afterWrite = func() { now = until.Add(time.Second) }
				_, err = store.PersistFrontier(t.Context(), set, ref, until)
			}
			if !errors.Is(err, ErrRetention) {
				t.Fatalf("accepted evidence after its retention expired: %v", err)
			}
		})
	}
}

func TestReloadRejectsNoncanonicalEvidenceDespiteMatchingRawDigests(t *testing.T) {
	for _, kind := range []string{"manifest", "boundary"} {
		t.Run(kind, func(t *testing.T) {
			provider := newFakeS3()
			store, _, ref, until := savedEmptyEvidence(t, provider, time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
			descriptor, err := parseDescriptor(fakeBody(t, provider, ref.Descriptor.Key))
			if err != nil {
				t.Fatal(err)
			}
			object := ref.Manifest
			if kind == "boundary" {
				object = ref.Boundary
			}
			raw := append([]byte("\n"), fakeBody(t, provider, object.Key)...)
			replacement, err := store.putEvidence(t.Context(), kind, digestBytes(raw), raw, until)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "manifest" {
				ref.Manifest, descriptor.Manifest = replacement, replacement
				descriptor.ManifestDigest = digestBytes(raw)
			} else {
				ref.Boundary, descriptor.Boundary = replacement, replacement
				descriptor.BoundaryDigest = digestBytes(raw)
			}
			descriptorBytes, err := descriptor.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			ref.Descriptor, err = store.putEvidence(t.Context(), "descriptor", digestBytes(descriptorBytes), descriptorBytes, until)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Reload(t.Context(), ref); !errors.Is(err, ErrIntegrity) || !strings.Contains(err.Error(), "not canonical") {
				t.Fatalf("noncanonical %s did not fail canonical validation: %v", kind, err)
			}
		})
	}
}

func TestStoreRejectsNoncanonicalProviderEndpoints(t *testing.T) {
	for _, endpoint := range []string{"https://example.com?", "HTTPS://example.com", "https://EXAMPLE.com"} {
		if _, err := New(newFakeS3(), Config{Endpoint: endpoint, Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted noncanonical endpoint %q: %v", endpoint, err)
		}
	}
}

func TestStoreRejectsUnverifiableSourceSizeLimits(t *testing.T) {
	for _, limit := range []int64{-1, DefaultMaxSourceBytes + 1} {
		if _, err := New(newFakeS3(), Config{Endpoint: "https://example.com", Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence", MaxSourceBytes: limit}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted unverifiable source size limit %d: %v", limit, err)
		}
	}
}

func (emptyProjectionSource) CaptureManagedProjection(context.Context) (manageddata.CapturedProjection, error) {
	return manageddata.CapturedProjection{
		DatabaseIdentity: "control_db",
		SystemIdentity:   "12345",
		Timeline:         1,
		LSN:              "0/100",
		RestorePointName: "provider_observation_test",
		Revisions:        make([]manageddata.CapturedProjectionRevision, 0),
	}, nil
}

func capturedEmptyObservation(t *testing.T) managedpostgres.CapturedObservation {
	t.Helper()
	captured, err := managedpostgres.Capture(t.Context(), emptyProjectionSource{})
	if err != nil {
		t.Fatal(err)
	}
	return captured
}

func savedEmptyEvidence(t *testing.T, provider *fakeS3, now time.Time) (*Store, managedpostgres.CapturedObservation, Ref, time.Time) {
	t.Helper()
	return savedEmptyEvidenceWithClient(t, provider, provider, now)
}

func savedEmptyEvidenceWithClient(t *testing.T, client Client, provider *fakeS3, now time.Time) (*Store, managedpostgres.CapturedObservation, Ref, time.Time) {
	t.Helper()
	store := newTestStoreWithClient(t, client, now)
	captured := capturedEmptyObservation(t)
	until := now.Add(2 * time.Hour)
	ref, err := store.Save(t.Context(), &captured, []observation.Observation{}, until)
	if err != nil {
		t.Fatal(err)
	}
	return store, captured, ref, ceilSecond(until)
}

func TestPersistFrontierReadFrontierRoundTripEmptyCapture(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)

	manifestBody := fakeBody(t, provider.fakeS3, evidence.Manifest.Key)
	manifest, err := observation.ParseManifest(manifestBody)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Inventory.Revisions == nil || manifest.Objects == nil {
		t.Fatalf("empty evidence arrays were not preserved: %#v", manifest)
	}
	if len(manifest.Inventory.Revisions) != 0 || len(manifest.Objects) != 0 {
		t.Fatalf("empty capture manifest = %#v", manifest)
	}

	boundary := captured.Boundary()
	boundaryDigest, err := boundary.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, boundaryDigest, "sha256:"+evidence.Descriptor.SHA256)
	frontier, err := store.PersistFrontier(t.Context(), set, evidence, until)
	if err != nil {
		t.Fatalf("PersistFrontier() = %v", err)
	}
	got, err := store.ReadFrontier(t.Context(), frontier, until)
	if err != nil {
		t.Fatalf("ReadFrontier() = %v", err)
	}
	if got.ID != set.ID || got.SchemaVersion != recoveryset.EvidenceSchemaVersion || got.ManagedEvidence == nil || got.ManagedEvidence.Boundary != boundary {
		t.Fatalf("round-trip set = %#v", got)
	}
	wantDigest, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := got.Digest()
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("round-trip digest = %q, want %q (err=%v)", gotDigest, wantDigest, err)
	}
}

func TestReadFrontierRejectsForgedBoundaryBinding(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	boundary := captured.Boundary()
	set := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, mustDigest(t, boundary), "sha256:"+evidence.Descriptor.SHA256)
	frontier, err := store.PersistFrontier(t.Context(), set, evidence, until)
	if err != nil {
		t.Fatal(err)
	}

	// Keep the evidence digests unchanged while forging the boundary marker in
	// both immutable frontier objects. ReadFrontier must reject the resulting
	// recovery set instead of treating the frontier's marker as authoritative.
	forged := cloneRecoverySet(set)
	forged.ManagedEvidence.Boundary.LSN = "0/101"
	forged.ClusterPoints[0].RecoveryIdentity = forged.ManagedEvidence.Boundary.RecoveryIdentity()
	forgedBytes, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedDigest := normalizedDigest(forgedBytes)
	provider.add(store.frontierKey(forgedDigest), forgedBytes, until)
	provider.mu.Lock()
	frontierObject := provider.objects[store.frontierKey(forgedDigest)]
	setObject := provider.objects[frontier.SetIDObject.Key]
	setObject.body = append([]byte(nil), forgedBytes...)
	provider.objects[frontier.SetIDObject.Key] = setObject
	provider.mu.Unlock()
	forgedRef := frontier
	forgedRef.Digest = forgedDigest
	forgedRef.Frontier = ObjectRef{Bucket: "evidence", Key: store.frontierKey(forgedDigest), VersionID: frontierObject.version, SHA256: rawDigest(forgedDigest), Size: int64(len(forgedBytes))}
	forgedRef.SetIDObject.SHA256 = rawDigest(forgedDigest)
	forgedRef.SetIDObject.Size = int64(len(forgedBytes))
	if _, err := store.ReadFrontier(t.Context(), forgedRef); err == nil {
		t.Fatal("ReadFrontier accepted a forged boundary binding")
	}
}

func TestReadFrontierRejectsShortFrontierRetention(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	boundary := captured.Boundary()
	set := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, mustDigest(t, boundary), "sha256:"+evidence.Descriptor.SHA256)
	frontier, err := store.PersistFrontier(t.Context(), set, evidence, until)
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	for _, key := range []string{frontier.Frontier.Key, frontier.SetIDObject.Key} {
		object := provider.objects[key]
		object.retain = now.Add(time.Hour)
		provider.objects[key] = object
	}
	provider.mu.Unlock()
	if _, err := store.ReadFrontier(t.Context(), frontier); !errors.Is(err, ErrRetention) {
		t.Fatalf("short frontier retention = %v, want %v", err, ErrRetention)
	}
}

func TestPersistFrontierRejectsBoundaryAndVersionDrift(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	boundary := captured.Boundary()
	boundaryDigest, err := boundary.Digest()
	if err != nil {
		t.Fatal(err)
	}
	base := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, boundaryDigest, "sha256:"+evidence.Descriptor.SHA256)

	for name, mutate := range map[string]func(*recoveryset.RecoverySet){
		"wrong LSN": func(set *recoveryset.RecoverySet) {
			set.ManagedEvidence.Boundary.LSN = "0/101"
			set.ManagedEvidence.BoundaryDigest = mustDigest(t, set.ManagedEvidence.Boundary)
			set.ClusterPoints[0].RecoveryIdentity = set.ManagedEvidence.Boundary.RecoveryIdentity()
		},
		"wrong database": func(set *recoveryset.RecoverySet) {
			set.ManagedEvidence.Boundary.DatabaseIdentity = "other_db"
			set.ManagedEvidence.BoundaryDigest = mustDigest(t, set.ManagedEvidence.Boundary)
			set.ClusterPoints[0].DatabaseIdentity = "other_db"
			set.ClusterPoints[0].RecoveryIdentity = set.ManagedEvidence.Boundary.RecoveryIdentity()
		},
		"unknown schema": func(set *recoveryset.RecoverySet) { set.SchemaVersion = 3 },
		"downgrade": func(set *recoveryset.RecoverySet) {
			set.SchemaVersion = recoveryset.SchemaVersion
			set.ManagedEvidence = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			set := cloneRecoverySet(base)
			mutate(&set)
			if _, err := store.PersistFrontier(t.Context(), set, evidence, until); err == nil {
				t.Fatal("PersistFrontier accepted invalid boundary/version")
			}
		})
	}

	frontier, err := store.PersistFrontier(t.Context(), base, evidence, until)
	if err != nil {
		t.Fatal(err)
	}
	badRef := frontier
	badRef.Frontier.SHA256 = strings.Repeat("f", 64)
	if _, err := store.ReadFrontier(t.Context(), badRef, until); err == nil {
		t.Fatal("ReadFrontier accepted a mutated frontier ref hash")
	}
}

func TestPersistFrontierSameSetIDConflict(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	boundary := captured.Boundary()
	boundaryDigest := mustDigest(t, boundary)
	set := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, boundaryDigest, "sha256:"+evidence.Descriptor.SHA256)
	if _, err := store.PersistFrontier(t.Context(), set, evidence, until); err != nil {
		t.Fatal(err)
	}
	conflict := cloneRecoverySet(set)
	conflict.AuditIdentity = "different-audit"
	if _, err := store.PersistFrontier(t.Context(), conflict, evidence, until); !errors.Is(err, ErrConflict) {
		t.Fatalf("same set ID conflict = %v, want %v", err, ErrConflict)
	}
}

func TestPersistFrontierFailsClosedOnEmptySetIDVersionListing(t *testing.T) {
	provider := newVersionedFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, captured, evidence, until := savedEmptyEvidenceWithClient(t, provider, provider.fakeS3, now)
	boundary := captured.Boundary()
	set := regressionV2Set(t, boundary, "sha256:"+evidence.Manifest.SHA256, mustDigest(t, boundary), "sha256:"+evidence.Descriptor.SHA256)
	provider.mu.Lock()
	provider.listEmpty = true
	provider.mu.Unlock()
	if _, err := store.PersistFrontier(t.Context(), set, evidence, until); err == nil || (!errors.Is(err, ErrBackend) && !errors.Is(err, ErrIntegrity)) {
		t.Fatalf("empty set-ID version listing = %v, want a fail-closed backend/integrity error", err)
	}
}

func TestReloadRejectsHorizonRefSizeAndBadVersionClosesBody(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, _, evidence, _ := savedEmptyEvidence(t, provider, now)
	provider.mu.Lock()
	for key, object := range provider.objects {
		object.retain = now.Add(10 * time.Hour)
		provider.objects[key] = object
	}
	provider.mu.Unlock()
	if _, err := store.Reload(t.Context(), evidence, now.Add(3*time.Hour)); !errors.Is(err, ErrRetention) {
		t.Fatalf("short descriptor horizon = %v, want %v", err, ErrRetention)
	}

	tracked := &trackingFakeS3{fakeS3: provider}
	trackedStore := newTrackedTestStore(t, tracked, now)
	badSize := evidence
	badSize.Descriptor.Size = maxDescriptorBytes + 1
	getsBefore := tracked.getCount.Load()
	if _, err := trackedStore.Reload(t.Context(), badSize); err == nil {
		t.Fatal("Reload accepted an oversized evidence reference")
	}
	if tracked.getCount.Load() != getsBefore {
		t.Fatalf("oversized ref caused GET calls: before=%d after=%d", getsBefore, tracked.getCount.Load())
	}

	tracked.closeCount.Store(0)
	provider.mu.Lock()
	provider.wrongGetVersion = true
	provider.mu.Unlock()
	trackedStore = newTrackedTestStore(t, tracked, now)
	if _, err := trackedStore.Reload(t.Context(), evidence); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("bad GET version error = %v", err)
	}
	if tracked.closeCount.Load() == 0 {
		t.Fatal("bad GET version did not close response body")
	}
}

func TestParseDescriptorRejectsOmittedCaseAliasedAndNullFields(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	_, _, evidence, _ := savedEmptyEvidence(t, provider, now)
	raw := fakeBody(t, provider, evidence.Descriptor.Key)
	cases := map[string][]byte{
		"omitted zero size":   removeJSONField(raw, "size"),
		"case aliased schema": bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1),
		"null size":           replaceJSONFieldWithNull(raw, "size"),
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDescriptor(invalid); err == nil {
				t.Fatal("parseDescriptor accepted malformed descriptor")
			}
		})
	}
}

func TestReloadRejectsExpiredEvidenceRetention(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store, _, evidence, _ := savedEmptyEvidence(t, provider, now)
	provider.mu.Lock()
	for key, object := range provider.objects {
		object.retain = now.Add(-time.Minute)
		provider.objects[key] = object
	}
	provider.mu.Unlock()
	if _, err := store.Reload(t.Context(), evidence); !errors.Is(err, ErrRetention) {
		t.Fatalf("expired evidence retention = %v, want %v", err, ErrRetention)
	}
}

func regressionV2Set(t *testing.T, boundary observation.Boundary, manifestDigest, boundaryDigest, descriptorDigest string) recoveryset.RecoverySet {
	t.Helper()
	compat := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatDigest, err := compat.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := recoveryset.RecoverySet{
		ID: "018f3f83-7b2f-7b37-9f9e-000000000010", SchemaVersion: recoveryset.EvidenceSchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{
			{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: boundary.ClusterIdentity(), DatabaseIdentity: boundary.DatabaseIdentity, RecoveryIdentity: boundary.RecoveryIdentity()},
			{DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "ducklake-cluster", DatabaseIdentity: "ducklake-db", RecoveryIdentity: "lsn:0/100"},
		},
		Delivery:      recoveryset.DeliveryPointer{TargetID: "target/prod", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000002", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000003", TargetRevision: 4},
		Serving:       recoveryset.SnapshotSeal{SealID: "018f3f83-7b2f-7b37-9f9e-000000000001", PhysicalPoolID: "pool-a", TenantDomain: "tenant-a", Region: "us-east", EncryptionDomain: "enc-a", ObjectNamespace: "objects/prod", CatalogDatabase: "ducklake-db", CatalogID: "catalog-a", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, DuckLakeSnapshotID: 42, RelationManifestDigest: testDigest('a'), RelationNamespace: "candidate/1", ClosureDigest: testDigest('b'), ObjectRoot: "s3://bucket/prod", ObjectRootDigest: testDigest('c'), ArtifactRoot: "artifacts/prod", ArtifactRootDigest: testDigest('d'), ServingArtifactID: "artifact-a", ServingArtifactDigest: testDigest('e'), CompiledGraphDigest: testDigest('f'), CompiledConfigDigest: testDigest('0'), SecurityDomainFingerprint: testDigest('1'), RequestDigest: testDigest('2'), PlanDigest: testDigest('3'), CompatibilityDigest: compatDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       recoveryset.CatalogCommit{CatalogID: "catalog-a", CatalogDatabase: "ducklake-db", CatalogUUID: "catalog-uuid-a", CatalogVersion: 9, SnapshotID: 42},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: "s3://bucket/prod", VersionID: "v9", Digest: testDigest('c'), ProviderRecoveryFrontier: "version:v9"}, {Kind: recoveryset.ObjectRootServingArtifact, URI: "artifacts/prod", VersionID: "v4", Digest: testDigest('d')}},
		Compatibility: compat, FenceEpoch: 3, AuditIdentity: "audit-1", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.FixedZone("offset", 3600)),
		ManagedEvidence: &recoveryset.ManagedEvidence{ManifestDigest: manifestDigest, BoundaryDigest: boundaryDigest, DescriptorDigest: descriptorDigest, Boundary: boundary},
	}
	return set
}

func cloneRecoverySet(set recoveryset.RecoverySet) recoveryset.RecoverySet {
	clone := set
	clone.ClusterPoints = append([]recoveryset.ClusterRecoveryPoint(nil), set.ClusterPoints...)
	clone.ObjectRoots = append([]recoveryset.ObjectRoot(nil), set.ObjectRoots...)
	if set.ManagedEvidence != nil {
		evidence := *set.ManagedEvidence
		clone.ManagedEvidence = &evidence
	}
	return clone
}

func mustDigest(t *testing.T, boundary observation.Boundary) string {
	t.Helper()
	digest, err := boundary.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func testDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func fakeBody(t *testing.T, provider *fakeS3, key string) []byte {
	t.Helper()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	object, ok := provider.objects[key]
	if !ok {
		t.Fatalf("fake object %q not found", key)
	}
	return append([]byte(nil), object.body...)
}

func removeJSONField(raw []byte, name string) []byte {
	prefix := []byte(`"` + name + `":`)
	start := bytes.Index(raw, prefix)
	if start < 0 {
		return raw
	}
	valueStart := start + len(prefix)
	valueEnd := valueStart
	if valueStart < len(raw) && raw[valueStart] == '"' {
		valueEnd++
		for valueEnd < len(raw) {
			if raw[valueEnd] == '"' && raw[valueEnd-1] != '\\' {
				valueEnd++
				break
			}
			valueEnd++
		}
	} else {
		for valueEnd < len(raw) && raw[valueEnd] != ',' && raw[valueEnd] != '}' {
			valueEnd++
		}
	}
	if valueEnd < len(raw) && raw[valueEnd] == ',' {
		valueEnd++
	} else if start > 0 && raw[start-1] == ',' {
		start--
	}
	return append(append([]byte(nil), raw[:start]...), raw[valueEnd:]...)
}

func replaceJSONFieldWithNull(raw []byte, name string) []byte {
	prefix := []byte(`"` + name + `":`)
	start := bytes.Index(raw, prefix)
	if start < 0 {
		return raw
	}
	valueStart := start + len(prefix)
	valueEnd := valueStart
	for valueEnd < len(raw) && raw[valueEnd] != ',' && raw[valueEnd] != '}' {
		valueEnd++
	}
	result := make([]byte, 0, len(raw)+4)
	result = append(result, raw[:valueStart]...)
	result = append(result, "null"...)
	result = append(result, raw[valueEnd:]...)
	return result
}

type trackingFakeS3 struct {
	*fakeS3
	closeCount atomic.Int32
	getCount   atomic.Int32
}

func newTrackedTestStore(t *testing.T, provider *trackingFakeS3, now time.Time) *Store {
	t.Helper()
	store := newTestStoreWithClient(t, provider, now)
	return store
}

func newTestStoreWithClient(t *testing.T, client Client, now time.Time) *Store {
	t.Helper()
	store, err := New(client, Config{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence", Prefix: "test", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type versionedFakeS3 struct {
	*fakeS3
	listEmpty bool
}

func newVersionedFakeS3() *versionedFakeS3 {
	return &versionedFakeS3{fakeS3: newFakeS3()}
}

func (f *versionedFakeS3) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listEmpty {
		truncated := false
		return &s3.ListObjectVersionsOutput{IsTruncated: &truncated, Versions: []types.ObjectVersion{}}, nil
	}
	key := ""
	if in != nil && in.Prefix != nil {
		key = *in.Prefix
	}
	versions := make([]types.ObjectVersion, 0, 1)
	if object, ok := f.objects[key]; ok {
		objectKey, version := key, object.version
		versions = append(versions, types.ObjectVersion{Key: &objectKey, VersionId: &version})
	}
	truncated := false
	return &s3.ListObjectVersionsOutput{IsTruncated: &truncated, Versions: versions}, nil
}

func (f *trackingFakeS3) GetObject(ctx context.Context, in *s3.GetObjectInput, options ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getCount.Add(1)
	out, err := f.fakeS3.GetObject(ctx, in, options...)
	if err != nil || out == nil || out.Body == nil {
		return out, err
	}
	out.Body = &trackingBody{ReadCloser: out.Body, closed: &f.closeCount}
	return out, nil
}

type trackingBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b *trackingBody) Close() error {
	b.closed.Add(1)
	return b.ReadCloser.Close()
}
