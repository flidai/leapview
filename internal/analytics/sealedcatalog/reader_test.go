package sealedcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

type testObjectStore struct {
	mu     sync.Mutex
	bytes  []byte
	object Object
	events *[]string
}

func (s *testObjectStore) Open(_ context.Context, key string) (Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events != nil {
		*s.events = append(*s.events, "object:"+key)
	}
	if s.object.Body != nil {
		return s.object, nil
	}
	return Object{Body: io.NopCloser(bytes.NewReader(s.bytes)), Size: int64(len(s.bytes)), Metadata: map[string]string{}}, nil
}

type testLeases struct {
	events *[]string
	lease  QueryLease
}

type failingRenewer struct{}

func (failingRenewer) RenewQueryLease(context.Context, string, time.Time) error {
	return errors.New("renew failed")
}

type blockingRenewer struct {
	started chan struct{}
}

func (r blockingRenewer) RenewQueryLease(ctx context.Context, _ string, _ time.Time) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

type sequenceRenewer struct {
	mu      sync.Mutex
	results []error
	calls   int
}

func (r *sequenceRenewer) RenewQueryLease(context.Context, string, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.results) == 0 {
		return nil
	}
	err := r.results[0]
	r.results = r.results[1:]
	return err
}

func (r *sequenceRenewer) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestReaderHeartbeatRetriesTransientFailureAndClearsHealth(t *testing.T) {
	callback := make(chan error, 2)
	renewer := &sequenceRenewer{results: []error{errors.New("provider timeout"), nil}}
	r := &Reader{
		leaseID:               "lease-transient",
		leaseExpiresAt:        time.Now().UTC().Add(time.Hour),
		heartbeatDone:         make(chan struct{}),
		onLeaseRenewalFailure: func(err error) { callback <- err },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.heartbeat(ctx, renewer, 20*time.Millisecond)

	select {
	case err := <-callback:
		if err != nil {
			t.Fatalf("successful retry callback error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not report successful retry")
	}
	if got := renewer.Calls(); got < 2 {
		t.Fatalf("renewal calls=%d, want transient retry", got)
	}
	if err := r.LeaseRenewalError(); err != nil {
		t.Fatalf("renewal health after successful retry=%v, want nil", err)
	}
	cancel()
	select {
	case <-r.heartbeatDone:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
}

func TestReaderHeartbeatSustainedFailureStopsAtLeaseExpiry(t *testing.T) {
	callback := make(chan error, 1)
	started := time.Now().UTC()
	r := &Reader{
		leaseID:        "lease-expiring",
		leaseExpiresAt: started.Add(15 * time.Millisecond),
		heartbeatDone:  make(chan struct{}),
		onLeaseRenewalFailure: func(err error) {
			select {
			case callback <- err:
			default:
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.heartbeat(ctx, failingRenewer{}, 10*time.Millisecond)

	select {
	case err := <-callback:
		if err == nil || !errors.Is(err, ErrLeaseRenewal) {
			t.Fatalf("expiry callback error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not report sustained renewal failure")
	}
	select {
	case <-r.heartbeatDone:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after lease expiry")
	}
	if elapsed := time.Since(started); elapsed < 10*time.Millisecond {
		t.Fatalf("heartbeat stopped before lease deadline: %v", elapsed)
	}
	if err := r.LeaseRenewalError(); err == nil || !errors.Is(err, ErrLeaseRenewal) {
		t.Fatalf("sustained renewal health=%v, want ErrLeaseRenewal", err)
	}
}

func TestReaderHeartbeatBoundsBlockingRenewalByLeaseExpiry(t *testing.T) {
	started := make(chan struct{}, 1)
	r := &Reader{
		leaseID:        "lease-blocking",
		leaseExpiresAt: time.Now().UTC().Add(35 * time.Millisecond),
		heartbeatDone:  make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	startedAt := time.Now()
	go func() {
		r.heartbeat(ctx, blockingRenewer{started: started}, 20*time.Millisecond)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking renewal did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocking renewal exceeded lease expiry")
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("blocking renewal elapsed=%v, want bounded by expiry", elapsed)
	}
	if err := r.LeaseRenewalError(); err == nil || !errors.Is(err, ErrLeaseRenewal) {
		t.Fatalf("blocking renewal health=%v, want ErrLeaseRenewal", err)
	}
}

func TestReaderHeartbeatFailureIsVisibleBeforeClose(t *testing.T) {
	callback := make(chan error, 1)
	r := &Reader{leaseID: "lease-1", heartbeatDone: make(chan struct{}), onLeaseRenewalFailure: func(err error) { callback <- err }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.heartbeat(ctx, failingRenewer{}, 2*time.Millisecond)
	select {
	case err := <-callback:
		if err == nil || !errors.Is(err, ErrLeaseRenewal) {
			t.Fatalf("callback error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not report renewal failure")
	}
	if err := r.LeaseRenewalError(); err == nil {
		t.Fatal("renewal failure was hidden before Close")
	}
	cancel()
	select {
	case <-r.heartbeatDone:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
}

func (l *testLeases) AcquireQueryLease(_ context.Context, _ LeaseInput) (QueryLease, error) {
	*l.events = append(*l.events, "lease:acquire")
	return l.lease, nil
}

func (l *testLeases) ReleaseQueryLease(_ context.Context, id string) error {
	*l.events = append(*l.events, "lease:release:"+id)
	return nil
}

func testArtifact(t *testing.T, value []byte) Artifact {
	t.Helper()
	digest := sha256.Sum256(value)
	catalogDigest := "sha256:" + hex.EncodeToString(digest[:])
	contract := testPoolContract(t)
	return Artifact{
		ObjectKey: catalogseal.CanonicalObjectKey(catalogDigest), SealID: "seal-1", CatalogDigest: catalogDigest,
		SizeBytes: int64(len(value)), ClosureDigest: testDigest("closure"), QualificationDigest: testDigest("qualification"), PhysicalPoolID: contract.Pool.ID.String(),
		Compatibility: contract.Tuple, PoolContract: contract,
	}
}

func testDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func testLeaseTimes() (time.Time, time.Time) {
	created := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	return created, created.Add(time.Hour)
}

func testPoolContract(t *testing.T) *ducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(t.TempDir(), "pool"), StorageNamespace: "data", EncryptionDomain: "sealed-test", IsolationBoundary: "sealed-test", RetentionAuthority: "sealed-test", Compatibility: tuple,
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]physicalpool.EvidenceCheck, 0, len(ducklake.SharedPoolConformanceChecks))
	for _, id := range ducklake.SharedPoolConformanceChecks {
		digest := sha256.Sum256([]byte(id))
		checks = append(checks, physicalpool.EvidenceCheck{ID: id, Passed: true, ObservationDigest: "sha256:" + hex.EncodeToString(digest[:])})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: tuple, ConformanceVersion: ducklake.SharedPoolConformanceVersion, Checks: checks})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := pool.Admit(evidence)
	if err != nil {
		t.Fatal(err)
	}
	pool, err = pool.ApplyAdmission(admission)
	if err != nil {
		t.Fatal(err)
	}
	return &ducklake.PoolContract{Pool: pool, Tuple: tuple, Admission: admission, Evidence: evidence}
}

func TestVerifyObjectChecksExactKeyDigestSizeAndMetadata(t *testing.T) {
	value := []byte("immutable catalog bytes")
	artifact := testArtifact(t, value)
	store := &testObjectStore{bytes: value}
	store.object.Metadata = map[string]string{catalogseal.MetadataDigest: artifact.CatalogDigest, catalogseal.MetadataSize: "23"}
	store.object.Size = int64(len(value))
	store.object.Body = io.NopCloser(bytes.NewReader(value))
	if err := VerifyObject(t.Context(), store, artifact); err != nil {
		t.Fatalf("VerifyObject() = %v", err)
	}

	store.object.Metadata[catalogseal.MetadataSize] = "22"
	if err := VerifyObject(t.Context(), store, artifact); !errors.Is(err, ErrArtifactMetadata) {
		t.Fatalf("metadata mismatch = %v, want ErrArtifactMetadata", err)
	}

	store.object.Metadata[catalogseal.MetadataSize] = "23"
	store.object.Body = io.NopCloser(bytes.NewReader([]byte("immutable catalog bytEs")))
	if err := VerifyObject(t.Context(), store, artifact); !errors.Is(err, ErrArtifactDigest) {
		t.Fatalf("digest mismatch = %v, want ErrArtifactDigest", err)
	}
}

func TestOpenAcquiresQueryLeaseBeforeExactObjectAttach(t *testing.T) {
	value := []byte("not a DuckLake catalog")
	artifact := testArtifact(t, value)
	events := []string{}
	store := &testObjectStore{bytes: value, events: &events}
	store.object = Object{Body: io.NopCloser(bytes.NewReader(value)), Size: int64(len(value)), Metadata: map[string]string{
		catalogseal.MetadataDigest: artifact.CatalogDigest,
		catalogseal.MetadataSize:   "22",
	}}
	leases := &testLeases{events: &events, lease: QueryLease{ID: "query-1"}}
	request := Request{
		Artifact: artifact, Store: store, Leases: leases,
		Lease:       LeaseInput{ID: "query-1", HolderID: "reader", GenerationID: "generation-1", SealID: artifact.SealID, CatalogDigest: artifact.CatalogDigest, ObjectKey: artifact.ObjectKey, ObjectSize: artifact.SizeBytes, ClosureDigest: artifact.ClosureDigest, QualificationDigest: artifact.QualificationDigest, PhysicalPoolID: artifact.PhysicalPoolID},
		Authorize:   func(context.Context, Artifact, LeaseInput) error { events = append(events, "auth"); return nil },
		StagingRoot: filepath.Join(t.TempDir(), "staging"),
	}
	request.Lease.CreatedAt, request.Lease.ExpiresAt = testLeaseTimes()
	// The bytes are intentionally not a valid catalog. Regardless of whether
	// DuckDB is built with the Arrow tag, the durable lease and exact object
	// read must happen first, and a failed attach must release that lease.
	_, err := Open(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "sealed catalog") {
		t.Fatalf("Open() error = %v, want fail-closed attach error", err)
	}
	if len(events) < 4 || events[0] != "auth" || events[1] != "lease:acquire" || !strings.HasPrefix(events[2], "object:") || events[len(events)-1] != "lease:release:query-1" {
		t.Fatalf("event order = %#v, want lease acquire, object open, release", events)
	}
}

func TestOpenRejectsLeaseArtifactBindingBeforeAcquire(t *testing.T) {
	value := []byte("catalog")
	artifact := testArtifact(t, value)
	events := []string{}
	leases := &testLeases{events: &events, lease: QueryLease{ID: "query-1"}}
	created, expires := testLeaseTimes()
	request := Request{Artifact: artifact, Store: &testObjectStore{bytes: value, events: &events}, Leases: leases,
		Lease: LeaseInput{ID: "query-1", HolderID: "reader", GenerationID: "generation-1", SealID: artifact.SealID, CatalogDigest: "sha256:" + strings.Repeat("0", 64), ObjectKey: artifact.ObjectKey, ObjectSize: artifact.SizeBytes, ClosureDigest: artifact.ClosureDigest, QualificationDigest: artifact.QualificationDigest, PhysicalPoolID: artifact.PhysicalPoolID, CreatedAt: created, ExpiresAt: expires}, Authorize: func(context.Context, Artifact, LeaseInput) error { return nil }}
	if _, err := Open(t.Context(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("binding mismatch = %v, want ErrInvalidRequest", err)
	}
	if !reflect.DeepEqual(events, []string{}) {
		t.Fatalf("events after invalid request = %#v, want no lease or object access", events)
	}
}

func TestOpenRequiresLiveAuthorizationBeforeLease(t *testing.T) {
	value := []byte("catalog")
	artifact := testArtifact(t, value)
	events := []string{}
	leases := &testLeases{events: &events, lease: QueryLease{ID: "query-1"}}
	request := Request{Artifact: artifact, Store: &testObjectStore{bytes: value, events: &events}, Leases: leases,
		Lease: LeaseInput{ID: "query-1", HolderID: "reader", GenerationID: "generation-1", SealID: artifact.SealID, CatalogDigest: artifact.CatalogDigest, ObjectKey: artifact.ObjectKey, ObjectSize: artifact.SizeBytes, ClosureDigest: artifact.ClosureDigest, QualificationDigest: artifact.QualificationDigest, PhysicalPoolID: artifact.PhysicalPoolID, CreatedAt: func() time.Time { t, _ := testLeaseTimes(); return t }(), ExpiresAt: func() time.Time { _, t := testLeaseTimes(); return t }()}}
	if _, err := Open(t.Context(), request); !errors.Is(err, ErrAuthorization) {
		t.Fatalf("missing authorization = %v, want ErrAuthorization", err)
	}
	if len(events) != 0 {
		t.Fatalf("events without authorization = %#v, want no lease or object access", events)
	}
}

func TestCloseRetainsRootWhenDetachIsUncertain(t *testing.T) {
	events := []string{}
	leases := &testLeases{events: &events, lease: QueryLease{ID: "query-1"}}
	staging := filepath.Join(t.TempDir(), "still-live")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	detachErr := errors.New("catalog close uncertain")
	reader := &Reader{detach: func() error { return detachErr }, leases: leases, leaseID: "query-1", staging: staging}
	if err := reader.Close(); !errors.Is(err, detachErr) {
		t.Fatalf("Close() = %v, want detach error", err)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging after uncertain detach: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("lease events after uncertain detach = %#v, want no release", events)
	}
}
