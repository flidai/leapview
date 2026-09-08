package observationstore

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/flidai/leapview/internal/recoveryset/observation"
)

func TestPutEvidenceIsConditionalAndComplianceRetained(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t, provider, now)
	until := now.Add(2 * time.Hour)
	body := []byte("immutable evidence")
	digestValue := normalizedDigest(body)
	ref, err := store.putEvidence(t.Context(), "manifest", digestValue, body, until)
	if err != nil {
		t.Fatal(err)
	}
	if ref.VersionID == "" || ref.SHA256 != rawDigest(digestValue) {
		t.Fatalf("ref = %#v", ref)
	}
	provider.mu.Lock()
	put := provider.puts[0]
	provider.mu.Unlock()
	if put.IfNoneMatch == nil || *put.IfNoneMatch != "*" || put.ObjectLockMode != types.ObjectLockModeCompliance || put.ObjectLockRetainUntilDate == nil || !put.ObjectLockRetainUntilDate.Equal(ceilSecond(until)) {
		t.Fatalf("PutObject did not pin conditional compliance retention: %#v", put)
	}
	if _, err := store.putEvidence(t.Context(), "manifest", digestValue, body, until); err != nil {
		t.Fatalf("idempotent evidence write: %v", err)
	}
	provider.mu.Lock()
	puts := len(provider.puts)
	provider.mu.Unlock()
	if puts != 2 { // provider receives the conditional retry; it never receives an overwrite
		t.Fatalf("PutObject calls = %d, want 2 conditional attempts", puts)
	}
}

func TestPutEvidenceRejectsDifferentBytesOnPrecondition(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t, provider, now)
	key := store.evidenceKey("manifest", normalizedDigest([]byte("expected")))
	provider.add(key, []byte("different"), now.Add(time.Hour))
	_, err := store.putEvidence(t.Context(), "manifest", normalizedDigest([]byte("expected")), []byte("expected"), now.Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("different precondition object error = %v", err)
	}
	if strings.Contains(err.Error(), "different") {
		t.Fatal("provider object bytes leaked through error")
	}
}

func TestReloadBindsExactVersionAndRecordedHorizon(t *testing.T) {
	provider := newFakeS3()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newTestStore(t, provider, now)
	until := now.Add(2 * time.Hour)
	inventory := observation.Inventory{SchemaVersion: observation.SchemaVersion, Revisions: []observation.Revision{}}
	inventoryDigest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	boundary := observation.Boundary{ProtocolVersion: observation.ProtocolVersion, DatabaseIdentity: "control_db", SystemIdentity: "12345", Timeline: 1, LSN: "0/100", RestorePointName: "point", InventoryDigest: inventoryDigest}
	manifest := observation.Manifest{SchemaVersion: observation.SchemaVersion, Boundary: boundary, Inventory: inventory, Objects: []observation.Observation{}}
	manifestBytes, err := manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	boundaryBytes, err := boundary.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	manifestRef, err := store.putEvidence(t.Context(), "manifest", normalizedDigest(manifestBytes), manifestBytes, until)
	if err != nil {
		t.Fatal(err)
	}
	boundaryRef, err := store.putEvidence(t.Context(), "boundary", normalizedDigest(boundaryBytes), boundaryBytes, until)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := Descriptor{SchemaVersion: SchemaVersion, ManifestDigest: normalizedDigest(manifestBytes), BoundaryDigest: normalizedDigest(boundaryBytes), Manifest: manifestRef, Boundary: boundaryRef, RequiredRetainUntil: ceilSecond(until), SourceProtections: []observation.Protection{}}
	descriptorBytes, err := descriptor.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	descriptorRef, err := store.putEvidence(t.Context(), "descriptor", normalizedDigest(descriptorBytes), descriptorBytes, until)
	if err != nil {
		t.Fatal(err)
	}
	ref := Ref{SchemaVersion: SchemaVersion, Manifest: manifestRef, Boundary: boundaryRef, Descriptor: descriptorRef}
	if _, err := store.Reload(t.Context(), ref, until); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	provider.mu.Lock()
	provider.wrongGetVersion = true
	provider.mu.Unlock()
	if _, err := store.Reload(t.Context(), ref, until); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("Reload accepted mismatched GET version: %v", err)
	}
}

func newTestStore(t *testing.T, provider *fakeS3, now time.Time) *Store {
	t.Helper()
	store, err := New(provider, Config{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", SourceBucket: "source", EvidenceBucket: "evidence", Prefix: "test", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type fakeS3 struct {
	mu              sync.Mutex
	objects         map[string]fakeObject
	seq             int
	puts            []*s3.PutObjectInput
	gets            int
	wrongGetVersion bool
}

type fakeObject struct {
	body    []byte
	version string
	retain  time.Time
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: make(map[string]fakeObject)} }

func (f *fakeS3) add(key string, body []byte, retain time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	f.objects[key] = fakeObject{body: append([]byte(nil), body...), version: "v" + string(rune('0'+f.seq)), retain: retain}
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, in)
	key := *in.Key
	if _, exists := f.objects[key]; exists && in.IfNoneMatch != nil && *in.IfNoneMatch == "*" {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "exists"}
	}
	f.seq++
	version := "v" + string(rune('0'+f.seq))
	body, _ := io.ReadAll(in.Body)
	f.objects[key] = fakeObject{body: body, version: version, retain: in.ObjectLockRetainUntilDate.UTC()}
	return &s3.PutObjectOutput{VersionId: &version}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[*in.Key]
	if !ok || in.VersionId != nil && *in.VersionId != object.version {
		return nil, &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "missing"}
	}
	version := object.version
	size := int64(len(object.body))
	return &s3.HeadObjectOutput{VersionId: &version, ContentLength: &size}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	object, ok := f.objects[*in.Key]
	if !ok || in.VersionId != nil && *in.VersionId != object.version {
		return nil, &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "missing"}
	}
	version := object.version
	if f.wrongGetVersion {
		version = "wrong-version"
	}
	size := int64(len(object.body))
	return &s3.GetObjectOutput{VersionId: &version, ContentLength: &size, Body: io.NopCloser(bytes.NewReader(object.body))}, nil
}

func (f *fakeS3) GetObjectRetention(_ context.Context, in *s3.GetObjectRetentionInput, _ ...func(*s3.Options)) (*s3.GetObjectRetentionOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[*in.Key]
	if !ok || in.VersionId != nil && *in.VersionId != object.version {
		return nil, &smithy.GenericAPIError{Code: "NoSuchVersion", Message: "missing"}
	}
	return &s3.GetObjectRetentionOutput{Retention: &types.ObjectLockRetention{Mode: types.ObjectLockRetentionModeCompliance, RetainUntilDate: &object.retain}}, nil
}
