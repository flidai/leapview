package l3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/google/uuid"
)

func testDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

const (
	testOriginSnapshotSealID  = "00000000-0000-0000-0000-000000000001"
	testOriginSnapshotSealID2 = "00000000-0000-0000-0000-000000000002"
)

func testNamespace() cachepostgres.Namespace {
	return cachepostgres.Namespace{PartitionKind: cachepostgres.PartitionProduction, TargetID: "target", ProjectID: "project", Environment: "prod"}
}

func testKey() cachepostgres.ManifestKey {
	return cachepostgres.ManifestKey{PartitionKind: cachepostgres.PartitionProduction, TargetID: "target", ProjectID: "project", Environment: "prod", PartitionFormatVersion: 2, DependencyDigest: testDigest('a'), PolicyFingerprint: testDigest('b'), CanonicalQueryDigest: testDigest('c'), KeyFormatVersion: 2}
}

type memoryObject struct {
	info ObjectInfo
	body []byte
}

type memoryStore struct {
	objects       map[string]memoryObject
	putErr        error
	corrupt       bool
	openCount     int
	deleteCount   int
	listCalls     int
	listLimit     int
	listOver      bool
	deleteStarted chan struct{}
	blockDelete   bool
}

func newMemoryStore() *memoryStore { return &memoryStore{objects: make(map[string]memoryObject)} }

func (s *memoryStore) PutImmutable(_ context.Context, key string, body io.Reader, metadata ObjectMetadata) (ObjectInfo, error) {
	if existing, ok := s.objects[key]; ok {
		return existing.info, ErrObjectExists
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return ObjectInfo{}, err
	}
	info := ObjectInfo{Key: key, SecurityDomain: metadata.SecurityDomain, Digest: digestBytes(b), Size: int64(len(b)), Metadata: append([]byte(nil), metadata.Metadata...), MetadataDigest: metadata.MetadataDigest, CreatedAt: time.Now().UTC()}
	s.objects[key] = memoryObject{info: info, body: append([]byte(nil), b...)}
	if s.putErr != nil {
		return info, s.putErr
	}
	return info, nil
}

func (s *memoryStore) Open(_ context.Context, key string) (Object, error) {
	s.openCount++
	entry, ok := s.objects[key]
	if !ok {
		return Object{}, errors.New("not found")
	}
	body := append([]byte(nil), entry.body...)
	info := entry.info
	if s.corrupt {
		body = []byte("tampered")
	}
	return Object{Body: io.NopCloser(bytes.NewReader(body)), Info: info}, nil
}

func (s *memoryStore) DeleteExact(ctx context.Context, object ObjectInfo) error {
	if s.deleteStarted != nil {
		select {
		case <-s.deleteStarted:
		default:
			close(s.deleteStarted)
		}
	}
	if s.blockDelete {
		<-ctx.Done()
		return ctx.Err()
	}
	delete(s.objects, object.Key)
	s.deleteCount++
	return nil
}

func (s *memoryStore) List(_ context.Context, prefix, after string, limit int) ([]ObjectInfo, string, error) {
	s.listCalls++
	s.listLimit = limit
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	next := ""
	if !s.listOver && len(keys) > limit {
		keys = keys[:limit]
		next = keys[len(keys)-1]
	}
	out := make([]ObjectInfo, 0, len(keys))
	for _, key := range keys {
		out = append(out, s.objects[key].info)
	}
	return out, next, nil
}

type fakeAuthority struct {
	mu                   sync.Mutex
	manifest             cachepostgres.Manifest
	publishInputs        []cachepostgres.PublishInput
	publishCalls         int
	lookupCalls          int
	retireCalls          int
	reachable            map[string]bool
	activeFills          map[string]bool
	activeObjectFences   map[string]bool
	reachStarted         chan struct{}
	allowReach           chan struct{}
	renewErr             error
	renewCalls           int
	renewFailAfter       int
	renewWaitForDelete   <-chan struct{}
	renewFailAfterDelete bool
	renewBlock           bool
	renewStarted         chan struct{}
	renewSignaled        bool
}

func (a *fakeAuthority) AcquireFill(_ context.Context, in cachepostgres.AcquireFillInput) (cachepostgres.FillLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeFills == nil {
		a.activeFills = make(map[string]bool)
	}
	if a.activeFills[in.CacheKey] {
		return cachepostgres.FillLease{}, cachepostgres.ErrBusy
	}
	a.activeFills[in.CacheKey] = true
	return cachepostgres.FillLease{LeaseID: uuid.New(), CacheKey: in.CacheKey, Namespace: in.Namespace, NamespaceEpoch: 1, OwnerID: in.OwnerID, FencingEpoch: 1, ExpiresAt: time.Now().Add(in.Lease)}, nil
}
func (a *fakeAuthority) RenewFill(_ context.Context, _ cachepostgres.FillLease, _ time.Duration) error {
	a.mu.Lock()
	a.renewCalls++
	call := a.renewCalls
	err := a.renewErr
	failAfter := a.renewFailAfter
	waitForDelete := a.renewWaitForDelete
	failAfterDelete := a.renewFailAfterDelete
	a.mu.Unlock()
	if err != nil && failAfterDelete {
		select {
		case <-waitForDelete:
			return err
		default:
			return nil
		}
	}
	if err != nil && failAfter > 0 && call > failAfter && waitForDelete != nil {
		<-waitForDelete
	}
	if err != nil && (failAfter == 0 || call > failAfter) {
		return err
	}
	return nil
}
func (a *fakeAuthority) ReleaseFill(_ context.Context, lease cachepostgres.FillLease) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activeFills, lease.CacheKey)
	return nil
}
func (a *fakeAuthority) Publish(_ context.Context, in cachepostgres.PublishInput) (cachepostgres.Manifest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.publishCalls++
	a.publishInputs = append(a.publishInputs, in)
	a.manifest = cachepostgres.Manifest{ManifestID: uuid.New(), Key: in.Key, OriginSnapshotSealID: in.OriginSnapshotSealID, StorageSecurityDomain: in.StorageSecurityDomain, ObjectDigest: in.ObjectDigest, ObjectKey: in.ObjectKey, ByteSize: in.ByteSize, Metadata: append([]byte(nil), in.Metadata...), State: cachepostgres.StateAdmitted}
	return a.manifest, nil
}
func (a *fakeAuthority) Lookup(context.Context, cachepostgres.LookupInput) (cachepostgres.Manifest, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lookupCalls++
	if a.manifest.ManifestID == uuid.Nil {
		return cachepostgres.Manifest{}, false, nil
	}
	return a.manifest, true, nil
}
func (a *fakeAuthority) RetireManifest(_ context.Context, manifestID uuid.UUID, _ json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retireCalls++
	if a.manifest.ManifestID != manifestID {
		return cachepostgres.ErrNotFound
	}
	a.manifest.State = cachepostgres.StateRetiring
	return nil
}
func (a *fakeAuthority) AcquireL3ObjectFence(_ context.Context, in cachepostgres.AcquireL3ObjectFenceInput) (cachepostgres.L3ObjectFence, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeObjectFences == nil {
		a.activeObjectFences = make(map[string]bool)
	}
	key := in.StorageSecurityDomain + "|" + in.ObjectKey
	if a.activeObjectFences[key] {
		return cachepostgres.L3ObjectFence{}, cachepostgres.ErrBusy
	}
	a.activeObjectFences[key] = true
	return cachepostgres.L3ObjectFence{LeaseID: uuid.New(), StorageSecurityDomain: in.StorageSecurityDomain, ObjectKey: in.ObjectKey, OwnerID: in.OwnerID, FencingEpoch: 1, ExpiresAt: time.Now().Add(in.Lease), AcquiredAt: time.Now()}, nil
}
func (a *fakeAuthority) RenewL3ObjectFence(ctx context.Context, _ cachepostgres.L3ObjectFence, _ time.Duration) error {
	a.mu.Lock()
	a.renewCalls++
	call := a.renewCalls
	err := a.renewErr
	failAfter := a.renewFailAfter
	waitForDelete := a.renewWaitForDelete
	failAfterDelete := a.renewFailAfterDelete
	block := a.renewBlock
	started := a.renewStarted
	signal := block && started != nil && !a.renewSignaled
	if signal {
		a.renewSignaled = true
	}
	a.mu.Unlock()
	if signal {
		close(started)
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err != nil && failAfterDelete {
		select {
		case <-waitForDelete:
			return err
		default:
			return nil
		}
	}
	if err != nil && failAfter > 0 && call > failAfter && waitForDelete != nil {
		<-waitForDelete
	}
	if err != nil && (failAfter == 0 || call > failAfter) {
		return err
	}
	return nil
}
func (a *fakeAuthority) ReleaseL3ObjectFence(_ context.Context, fence cachepostgres.L3ObjectFence) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.activeObjectFences, fence.StorageSecurityDomain+"|"+fence.ObjectKey)
	return nil
}
func (a *fakeAuthority) PrepareL3ObjectGC(_ context.Context, fence cachepostgres.L3ObjectFence) (bool, error) {
	if a.reachStarted != nil {
		select {
		case <-a.reachStarted:
		default:
			close(a.reachStarted)
		}
		<-a.allowReach
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.reachable[fence.ObjectKey], nil
}

func newTestCache(t *testing.T) (*Cache, *fakeAuthority, *memoryStore, cachepostgres.ManifestKey, cachepostgres.FillLease) {
	t.Helper()
	authority := &fakeAuthority{reachable: make(map[string]bool)}
	store := newMemoryStore()
	ns := testNamespace()
	c, err := New(Config{Authority: authority, Store: store, Namespace: ns, SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID, Prefix: "objects", Enabled: true, Now: func() time.Time { return time.Unix(1000, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	digest, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease := cachepostgres.FillLease{LeaseID: uuid.New(), CacheKey: digest, Namespace: ns, NamespaceEpoch: 1, OwnerID: "owner", FencingEpoch: 1, ExpiresAt: time.Unix(2000, 0)}
	return c, authority, store, key, lease
}

func newTestCollector(t *testing.T, authority *fakeAuthority, store *memoryStore) *Collector {
	t.Helper()
	collector, err := NewCollector(CollectorConfig{
		Authority: authority, Store: store, SecurityDomain: testDigest('d'), Prefix: "objects",
		GracePeriod: time.Hour, Now: func() time.Time { return time.Unix(1000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return collector
}

func TestPublishObjectFirstAndLostAcknowledgementConverges(t *testing.T) {
	c, authority, store, key, lease := newTestCache(t)
	store.putErr = ErrObjectAmbiguous
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result"), Metadata: []byte(`{"z":1,"a":"x"}`)})
	if err != nil {
		t.Fatalf("publish lost acknowledgement: %v", err)
	}
	if authority.publishCalls != 1 || manifest.ObjectDigest != digestBytes([]byte("result")) {
		t.Fatalf("publish calls/digest = %d/%s", authority.publishCalls, manifest.ObjectDigest)
	}
	if len(store.objects) != 1 || store.openCount == 0 {
		t.Fatalf("object-first reconciliation did not reopen exact object: %+v", store)
	}
	// A retry observes the immutable object and still reaches authority publish.
	store.putErr = nil
	if _, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result"), Metadata: []byte(`{"a":"x","z":1}`)}); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
}

func TestOriginSnapshotSealIsProvenanceOnly(t *testing.T) {
	ns := testNamespace()
	store := newMemoryStore()
	authorityA := &fakeAuthority{reachable: make(map[string]bool)}
	authorityB := &fakeAuthority{reachable: make(map[string]bool)}
	cacheA, err := New(Config{Authority: authorityA, Store: store, Namespace: ns, SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID, Prefix: "objects", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cacheB, err := New(Config{Authority: authorityB, Store: store, Namespace: ns, SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID2, Prefix: "objects", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	cacheKeyA, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	cacheKeyB, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	if cacheKeyA != cacheKeyB {
		t.Fatalf("cache keys differ by origin seal: %q vs %q", cacheKeyA, cacheKeyB)
	}
	digest := digestBytes([]byte("same-result"))
	objectKeyA, err := cacheA.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	objectKeyB, err := cacheB.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	if objectKeyA != objectKeyB {
		t.Fatalf("object keys differ by origin seal: %q vs %q", objectKeyA, objectKeyB)
	}
	lease := func(cache *Cache) cachepostgres.FillLease {
		return cachepostgres.FillLease{LeaseID: uuid.New(), CacheKey: cacheKeyA, Namespace: ns, NamespaceEpoch: 1, OwnerID: "owner", FencingEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}
	}
	if _, err := cacheA.Publish(t.Context(), PublishInput{Key: key, Lease: lease(cacheA), Body: strings.NewReader("same-result")}); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheB.Publish(t.Context(), PublishInput{Key: key, Lease: lease(cacheB), Body: strings.NewReader("same-result")}); err != nil {
		t.Fatal(err)
	}
	if len(authorityA.publishInputs) != 1 || authorityA.publishInputs[0].OriginSnapshotSealID != testOriginSnapshotSealID {
		t.Fatalf("authority A origin seal = %+v", authorityA.publishInputs)
	}
	if len(authorityB.publishInputs) != 1 || authorityB.publishInputs[0].OriginSnapshotSealID != testOriginSnapshotSealID2 {
		t.Fatalf("authority B origin seal = %+v", authorityB.publishInputs)
	}
}

func TestReadCorruptionSignalsDurableReconciliation(t *testing.T) {
	c, authority, store, key, lease := newTestCache(t)
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result")})
	if err != nil {
		t.Fatal(err)
	}
	store.corrupt = true
	result, err := c.Read(t.Context(), manifest, key)
	if err != nil {
		t.Fatalf("corrupt read: %v", err)
	}
	if result.Hit || !result.Reconciled || authority.retireCalls != 1 {
		t.Fatalf("corrupt read result = %+v retirements=%d", result, authority.retireCalls)
	}
}

func TestReadUsesSuppliedSnapshotWithoutLookup(t *testing.T) {
	c, authority, _, key, lease := newTestCache(t)
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Read(t.Context(), manifest, key)
	if err != nil || !result.Hit || string(result.Body) != "result" {
		t.Fatalf("read result = %+v err=%v", result, err)
	}
	if authority.lookupCalls != 0 {
		t.Fatalf("snapshot read performed %d authority lookups", authority.lookupCalls)
	}
}

func TestReadSnapshotRejectsMismatchedLookupKey(t *testing.T) {
	c, _, _, key, lease := newTestCache(t)
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result")})
	if err != nil {
		t.Fatal(err)
	}
	wrong := key
	wrong.CanonicalQueryDigest = testDigest('0')
	if _, err := c.ReadSnapshot(t.Context(), AdmissionSnapshot{Key: wrong, Manifest: manifest}); !errors.Is(err, ErrSecurityDomain) {
		t.Fatalf("snapshot mismatch error = %v", err)
	}
}

func TestGCRequiresGraceAndAuthorityReachability(t *testing.T) {
	c, authority, store, key, lease := newTestCache(t)
	collector := newTestCollector(t, authority, store)
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: lease, Body: strings.NewReader("result")})
	if err != nil {
		t.Fatal(err)
	}
	// Make the admitted object old enough, and add a second aged orphan.
	objectKey, err := c.ObjectKey(key, manifest.ObjectDigest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: manifest.ObjectDigest, Size: manifest.ByteSize, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("result")}
	orphanKey := c.objectPrefix + testDigest('e') + "/" + testDigest('f')
	store.objects[orphanKey] = memoryObject{info: ObjectInfo{Key: orphanKey, SecurityDomain: testDigest('d'), Digest: testDigest('f'), Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	authority.reachable[objectKey] = true
	result, err := collector.GC(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || store.deleteCount != 1 {
		t.Fatalf("gc result = %+v delete count=%d", result, store.deleteCount)
	}
	if _, ok := store.objects[objectKey]; !ok {
		t.Fatal("reachable admitted object was deleted")
	}
}

func TestGCFencePreventsConcurrentProducerDeleteRace(t *testing.T) {
	c, authority, store, key, _ := newTestCache(t)
	collector := newTestCollector(t, authority, store)
	digest := testDigest('e')
	objectKey, err := c.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: digest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	authority.reachable[objectKey] = false
	authority.reachStarted = make(chan struct{})
	authority.allowReach = make(chan struct{})

	gcDone := make(chan GCResult, 1)
	gcErr := make(chan error, 1)
	go func() {
		result, gcError := collector.GC(t.Context())
		gcDone <- result
		gcErr <- gcError
	}()
	<-authority.reachStarted
	if _, err := authority.AcquireL3ObjectFence(t.Context(), cachepostgres.AcquireL3ObjectFenceInput{StorageSecurityDomain: testDigest('d'), ObjectKey: objectKey, OwnerID: "producer", Lease: time.Minute}); !errors.Is(err, cachepostgres.ErrBusy) {
		t.Fatalf("producer acquired object fence while GC fence held: %v", err)
	}
	close(authority.allowReach)
	if err := <-gcErr; err != nil {
		t.Fatal(err)
	}
	if result := <-gcDone; result.Deleted != 1 {
		t.Fatalf("GC result = %+v", result)
	}
	if fence, err := authority.AcquireL3ObjectFence(t.Context(), cachepostgres.AcquireL3ObjectFenceInput{StorageSecurityDomain: testDigest('d'), ObjectKey: objectKey, OwnerID: "producer", Lease: time.Minute}); err != nil {
		t.Fatalf("producer could not acquire object fence after GC release: %v", err)
	} else if err := authority.ReleaseL3ObjectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
}

func TestGCFailsClosedWhenLeaseRenewalIsLost(t *testing.T) {
	c, authority, store, key, _ := newTestCache(t)
	collector := newTestCollector(t, authority, store)
	collector.fenceLease = MinGCLeaseDuration
	authority.renewErr = errors.New("renewal lost")
	digest := testDigest('e')
	objectKey, err := c.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: digest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	result, err := collector.GC(t.Context())
	if err == nil || store.deleteCount != 0 {
		t.Fatalf("lease-loss GC result=%+v err=%v deletes=%d", result, err, store.deleteCount)
	}
}

func TestGCHeartbeatCancelsBlockedDeleteOnRenewalLoss(t *testing.T) {
	c, authority, store, key, _ := newTestCache(t)
	collector := newTestCollector(t, authority, store)
	collector.fenceLease = MinGCLeaseDuration
	collector.operationTimeout = 5 * time.Second
	store.deleteStarted = make(chan struct{})
	store.blockDelete = true
	authority.renewErr = errors.New("heartbeat renewal lost")
	authority.renewFailAfterDelete = true // renewals succeed until Delete starts
	authority.renewWaitForDelete = store.deleteStarted
	digest := testDigest('e')
	objectKey, err := c.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: digest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	resultCh := make(chan struct {
		result GCResult
		err    error
	}, 1)
	go func() {
		result, gcErr := collector.GC(t.Context())
		resultCh <- struct {
			result GCResult
			err    error
		}{result: result, err: gcErr}
	}()
	select {
	case <-store.deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("GC did not reach blocked delete")
	}
	select {
	case outcome := <-resultCh:
		result, err := outcome.result, outcome.err
		if err == nil || result.Deleted != 0 || store.deleteCount != 0 {
			t.Fatalf("heartbeat-loss GC result=%+v err=%v deletes=%d", result, err, store.deleteCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("GC did not cancel blocked delete after heartbeat loss")
	}
	authority.mu.Lock()
	active := len(authority.activeObjectFences)
	authority.mu.Unlock()
	if active != 0 {
		t.Fatalf("GC object fence remained active after cancellation: %d", active)
	}
}

func TestGCFailsClosedBeforeFenceExpiryWhenRenewalBlocks(t *testing.T) {
	c, authority, store, key, _ := newTestCache(t)
	collector := newTestCollector(t, authority, store)
	collector.fenceLease = MinGCLeaseDuration
	authority.renewBlock = true
	authority.renewStarted = make(chan struct{})
	digest := testDigest('e')
	objectKey, err := c.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: digest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	done := make(chan error, 1)
	go func() {
		_, gcErr := collector.GC(t.Context())
		done <- gcErr
	}()
	select {
	case <-authority.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("GC did not attempt bounded object-fence renewal")
	}
	select {
	case err := <-done:
		if err == nil || store.deleteCount != 0 {
			t.Fatalf("blocked renewal error=%v deletes=%d", err, store.deleteCount)
		}
	case <-time.After(900 * time.Millisecond):
		t.Fatal("blocked fence renewal was not canceled before lease expiry")
	}
}

func TestDisabledCacheIsSafeAndCrossDomainIsRejected(t *testing.T) {
	disabled, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.AcquireFill(t.Context(), testKey(), "owner", time.Minute); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled acquire error = %v", err)
	}
	if result, err := disabled.Read(t.Context(), cachepostgres.Manifest{}, cachepostgres.ManifestKey{}); err != nil || result.Hit {
		t.Fatalf("disabled read = %+v err=%v", result, err)
	}
	c, _, _, key, lease := newTestCache(t)
	other := key
	other.Environment = "other"
	if _, err := c.Publish(t.Context(), PublishInput{Key: other, Lease: lease, Body: strings.NewReader("x")}); !errors.Is(err, ErrSecurityDomain) {
		t.Fatalf("cross-domain publish error = %v", err)
	}
}

func TestConfigBoundsObjectSizeAndPortablePrefix(t *testing.T) {
	base := Config{Authority: &fakeAuthority{reachable: make(map[string]bool)}, Store: newMemoryStore(), Namespace: testNamespace(), SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID, Enabled: true}
	for _, prefix := range []string{"..", "a/../b", "a\\b", "a//b", "a b"} {
		base.Prefix = prefix
		if _, err := New(base); !errors.Is(err, ErrInvalid) {
			t.Fatalf("prefix %q error = %v", prefix, err)
		}
	}
	base.Prefix = "safe/l3"
	base.MaxObjectBytes = MaxObjectBytesLimit + 1
	if _, err := New(base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized max bytes error = %v", err)
	}
	base.MaxObjectBytes = 0
	base.ObjectFenceLease = MinObjectFenceLease - time.Nanosecond
	if _, err := New(base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short object-fence lease error = %v", err)
	}
}

func TestConfigRequiresCanonicalOriginSnapshotSealID(t *testing.T) {
	base := Config{Authority: &fakeAuthority{reachable: make(map[string]bool)}, Store: newMemoryStore(), Namespace: testNamespace(), SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID, Enabled: true}
	for _, seal := range []string{"", "00000000-0000-0000-0000-00000000000A", "{00000000-0000-0000-0000-000000000001}"} {
		base.OriginSnapshotSealID = seal
		if _, err := New(base); !errors.Is(err, ErrInvalid) {
			t.Fatalf("origin seal %q error = %v", seal, err)
		}
	}
}

func TestGCRejectsProviderOverReturn(t *testing.T) {
	c, _, store, key, _ := newTestCache(t)
	authority := &fakeAuthority{reachable: make(map[string]bool)}
	collector := newTestCollector(t, authority, store)
	collector.batchSize = 1
	store.listOver = true
	digest := testDigest('e')
	objectKey, err := c.ObjectKey(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[objectKey] = memoryObject{info: ObjectInfo{Key: objectKey, SecurityDomain: testDigest('d'), Digest: digest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("x")}
	secondKey := c.objectPrefix + testDigest('0') + "/" + testDigest('1')
	store.objects[secondKey] = memoryObject{info: ObjectInfo{Key: secondKey, SecurityDomain: testDigest('d'), Digest: testDigest('1'), Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(-10000, 0)}, body: []byte("y")}
	result, err := collector.GC(t.Context())
	if err == nil || result.Deleted != 0 || store.deleteCount != 0 {
		t.Fatalf("over-return GC result=%+v err=%v deletes=%d", result, err, store.deleteCount)
	}
}
