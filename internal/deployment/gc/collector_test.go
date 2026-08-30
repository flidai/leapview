package gc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

type fakeControl struct {
	fence       deployment.GCFence
	rootSet     deployment.RootSet
	cycle       deployment.DeliveryGCCycle
	intents     []deployment.DeliveryGCDeleteIntent
	stale       bool
	quarantined int
}

func (f *fakeControl) AcquireGCFence(context.Context, deployment.GCFenceRequest) (deployment.GCFence, error) {
	f.fence = deployment.GCFence{ID: "fence", PhysicalPoolID: "pool", HolderID: "holder", Epoch: 1, RootRevision: f.rootSet.Revision, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	return f.fence, nil
}
func (f *fakeControl) ReleaseGCFence(context.Context, deployment.GCFence, time.Time) error {
	return nil
}
func (f *fakeControl) IsCurrentGCFence(context.Context, deployment.GCFence, time.Time) (bool, error) {
	return !f.stale, nil
}
func (f *fakeControl) EnumerateRoots(context.Context, string, time.Time) (deployment.RootSet, error) {
	return f.rootSet, nil
}
func (f *fakeControl) CreateGCCycle(_ context.Context, c deployment.DeliveryGCCycle) (deployment.DeliveryGCCycle, error) {
	if f.cycle.ID != "" {
		return f.cycle, nil
	}
	c.Status = deployment.DeliveryGCRunning
	f.cycle = c
	return c, nil
}
func (f *fakeControl) MarkGCCycle(_ context.Context, id, d string) (deployment.DeliveryGCCycle, error) {
	f.cycle.Status = deployment.DeliveryGCMarked
	f.cycle.MarkDigest = d
	return f.cycle, nil
}
func (f *fakeControl) BeginGCDelete(context.Context, string) (deployment.DeliveryGCCycle, error) {
	f.cycle.Status = deployment.DeliveryGCDeleting
	return f.cycle, nil
}
func (f *fakeControl) AbortGCCycle(_ context.Context, _ string, reason string, now time.Time) (deployment.DeliveryGCCycle, error) {
	f.cycle.Status = deployment.DeliveryGCAborted
	f.cycle.AbortReason = reason
	f.cycle.CompletedAt = now
	return f.cycle, nil
}
func (f *fakeControl) CompleteGCCycle(_ context.Context, _ string, now time.Time) (deployment.DeliveryGCCycle, error) {
	f.cycle.Status = deployment.DeliveryGCComplete
	f.cycle.CompletedAt = now
	return f.cycle, nil
}
func (f *fakeControl) CreateGCDeleteIntent(_ context.Context, i deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error) {
	i.Status = deployment.DeliveryGCDeletePending
	f.intents = append(f.intents, i)
	return i, nil
}
func (f *fakeControl) CompleteGCDeleteIntent(_ context.Context, id string, s deployment.DeliveryGCDeleteIntentStatus, now time.Time) (deployment.DeliveryGCDeleteIntent, error) {
	for n := range f.intents {
		if f.intents[n].ID == id {
			f.intents[n].Status = s
			f.intents[n].CompletedAt = now
			return f.intents[n], nil
		}
	}
	return deployment.DeliveryGCDeleteIntent{}, os.ErrNotExist
}
func (f *fakeControl) ListGCDeleteIntents(context.Context, string) ([]deployment.DeliveryGCDeleteIntent, error) {
	return append([]deployment.DeliveryGCDeleteIntent(nil), f.intents...), nil
}
func (f *fakeControl) QuarantineRoot(context.Context, deployment.DeliveryRoot, string, time.Time) error {
	f.quarantined++
	return nil
}

type fakeStore struct {
	objects         map[string]Object
	failDelete      bool
	dropOnFailure   bool
	replaceOnDelete bool
	onDelete        func()
	lastVersion     string
	deleted         []string
}

func (s *fakeStore) Open(_ context.Context, key string) (CatalogObject, error) {
	_, ok := s.objects[key]
	if !ok {
		return CatalogObject{}, os.ErrNotExist
	}
	b := []byte(key)
	return CatalogObject{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), Metadata: map[string]string{}}, nil
}
func (s *fakeStore) ListPoolObjects(context.Context, string) ([]Object, error) {
	r := make([]Object, 0, len(s.objects))
	for _, o := range s.objects {
		r = append(r, o)
	}
	return r, nil
}
func (s *fakeStore) DeleteConditional(_ context.Context, r DeleteRequest) (DeleteResponse, error) {
	if s.replaceOnDelete {
		if object, ok := s.objects[r.Key]; ok {
			object.Version = "v2"
			s.objects[r.Key] = object
		}
		s.replaceOnDelete = false
	}
	if s.onDelete != nil {
		s.onDelete()
		s.onDelete = nil
	}
	if s.failDelete {
		s.failDelete = false
		if s.dropOnFailure {
			delete(s.objects, r.Key)
		}
		return DeleteResponse{}, errors.New("lost acknowledgement")
	}
	o, ok := s.objects[r.Key]
	if !ok {
		return DeleteResponse{NotFound: true}, nil
	}
	if o.Digest != r.Digest {
		return DeleteResponse{}, errors.New("version mismatch")
	}
	if r.Version != "" && o.Version != r.Version {
		return DeleteResponse{}, errors.New("version replaced")
	}
	s.lastVersion = r.Version
	delete(s.objects, r.Key)
	s.deleted = append(s.deleted, r.Key)
	return DeleteResponse{Deleted: true}, nil
}
func (s *fakeStore) Stat(_ context.Context, _ string, key string) (Object, error) {
	o, ok := s.objects[key]
	if !ok {
		return Object{}, os.ErrNotExist
	}
	return o, nil
}

type fakeInspector struct {
	roots map[string]CatalogReachability
	err   error
}

func (i fakeInspector) Inspect(_ context.Context, r deployment.DeliveryRoot) (CatalogReachability, error) {
	if i.err != nil {
		return CatalogReachability{}, i.err
	}
	return i.roots[r.ObjectKey], nil
}

func digest(seed string) string { return "sha256:" + fmt.Sprintf("%064x", len(seed)) }

func TestCollectorMarksCrossCatalogDataAndDeleteFiles(t *testing.T) {
	now := time.Now().UTC()
	live := digest("live")
	old := now.Add(-time.Hour)
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1, Roots: []deployment.DeliveryRoot{{PhysicalPoolID: "pool", Kind: "published", SourceID: "g1", CatalogDigest: digest("c1"), ObjectKey: "catalog-1"}, {PhysicalPoolID: "pool", Kind: "candidate", SourceID: "c2", CatalogDigest: digest("c2"), ObjectKey: "catalog-2"}}}}
	store := &fakeStore{objects: map[string]Object{"catalog-1": {Key: "catalog-1", Digest: digest("c1"), CreatedAt: old}, "catalog-2": {Key: "catalog-2", Digest: digest("c2"), CreatedAt: old}, "data.parquet": {Key: "data.parquet", Digest: live, CreatedAt: old}, "deletes.puffin": {Key: "deletes.puffin", Digest: digest("del"), CreatedAt: old}, "orphan.parquet": {Key: "orphan.parquet", Digest: digest("orphan"), CreatedAt: old}}}
	inspector := fakeInspector{roots: map[string]CatalogReachability{"catalog-1": {CatalogKey: "catalog-1", CatalogDigest: digest("c1"), DataFiles: []string{"data.parquet"}, DeleteFiles: []string{"deletes.puffin"}}, "catalog-2": {CatalogKey: "catalog-2", CatalogDigest: digest("c2"), DataFiles: []string{"data.parquet"}}}}
	collector := Collector{Control: control, Store: store, Inspector: inspector, Quarantiner: control, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }, OrphanGrace: time.Minute}}
	result, err := collector.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 {
		t.Fatalf("deleted=%d want 1", result.Deleted)
	}
	if _, ok := store.objects["data.parquet"]; !ok {
		t.Fatal("live data file deleted")
	}
	if _, ok := store.objects["deletes.puffin"]; !ok {
		t.Fatal("live delete file deleted")
	}
	if _, ok := store.objects["orphan.parquet"]; ok {
		t.Fatal("orphan retained")
	}
}

func TestCollectorCorruptRootFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1, Roots: []deployment.DeliveryRoot{{PhysicalPoolID: "pool", Kind: "published", SourceID: "g1", CatalogDigest: digest("c"), ObjectKey: "catalog"}}}}
	store := &fakeStore{objects: map[string]Object{"catalog": {Key: "catalog", Digest: digest("c"), CreatedAt: now.Add(-time.Hour)}, "orphan": {Key: "orphan", Digest: digest("o"), CreatedAt: now.Add(-time.Hour)}}}
	_, err := (Collector{Control: control, Store: store, Inspector: fakeInspector{err: errors.New("corrupt")}, Quarantiner: control, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }}}).Run(context.Background())
	if !errors.Is(err, ErrCatalogQuarantined) {
		t.Fatalf("err=%v", err)
	}
	if len(store.deleted) != 0 || control.quarantined != 1 {
		t.Fatalf("deletion/quarantine=%d/%d", len(store.deleted), control.quarantined)
	}
}

func TestCollectorRevalidatesBeforeBatch(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("o"), CreatedAt: now.Add(-time.Hour)}}}
	c := Collector{Control: control, Store: store, Inspector: fakeInspector{roots: map[string]CatalogReachability{}}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }}}
	control.stale = true
	if _, err := c.Run(context.Background()); !errors.Is(err, ErrGCStale) {
		t.Fatalf("err=%v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatal("stale collector deleted object")
	}
}

func TestCollectorResumesPendingIntentAndUsesProviderVersion(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("o"), Version: "v1", CreatedAt: now.Add(-time.Hour)}}, failDelete: true}
	c := Collector{Control: control, Store: store, Inspector: fakeInspector{}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", CycleID: testUUIDv7, Now: func() time.Time { return now }}}
	if _, err := c.Run(context.Background()); !errors.Is(err, ErrDeleteUncertain) {
		t.Fatalf("first run err=%v", err)
	}
	if len(control.intents) != 1 || control.intents[0].Status != deployment.DeliveryGCDeletePending {
		t.Fatalf("pending intents=%#v", control.intents)
	}
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "orphan" {
		t.Fatalf("deleted=%v", store.deleted)
	}
	if store.lastVersion != "v1" {
		t.Fatalf("conditional version=%q", store.lastVersion)
	}
}

func TestCollectorSameVersionLostAckStaysPending(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("o"), Version: "same", CreatedAt: now.Add(-time.Hour)}}, failDelete: true}
	_, err := (Collector{Control: control, Store: store, Inspector: fakeInspector{}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }}}).Run(context.Background())
	if !errors.Is(err, ErrDeleteUncertain) {
		t.Fatalf("err=%v", err)
	}
	if len(control.intents) != 1 || control.intents[0].Status != deployment.DeliveryGCDeletePending {
		t.Fatalf("intent=%#v", control.intents)
	}
}

func TestCollectorLostDeleteAckReconcilesNotFound(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("o"), Version: "v1", CreatedAt: now.Add(-time.Hour)}}, failDelete: true, dropOnFailure: true}
	c := Collector{Control: control, Store: store, Inspector: fakeInspector{}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }}}
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.intents[0].Status != deployment.DeliveryGCDeleteDeleted {
		t.Fatalf("intent status=%s", control.intents[0].Status)
	}
}

func TestCollectorProtectsWriterCrashGraceAndReaderRoot(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1, Roots: []deployment.DeliveryRoot{{PhysicalPoolID: "pool", Kind: "lease", SourceID: "reader", CatalogDigest: digest("catalog"), ObjectKey: "catalog"}}}}
	store := &fakeStore{objects: map[string]Object{
		"catalog":                {Key: "catalog", Digest: digest("catalog"), CreatedAt: now.Add(-time.Hour)},
		"reader/data.parquet":    {Key: "reader/data.parquet", Digest: digest("reader"), CreatedAt: now.Add(-time.Hour)},
		"build/inflight.parquet": {Key: "build/inflight.parquet", Digest: digest("build"), CreatedAt: now.Add(-time.Hour)},
		"fresh.parquet":          {Key: "fresh.parquet", Digest: digest("fresh"), CreatedAt: now.Add(-time.Minute)},
	}}
	inspector := fakeInspector{roots: map[string]CatalogReachability{"catalog": {CatalogKey: "catalog", CatalogDigest: digest("catalog"), DataFiles: []string{"reader/data.parquet"}}}}
	result, err := (Collector{Control: control, Store: store, Inspector: inspector, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }, BuildGrace: time.Hour, OrphanGrace: time.Hour, ReaderGrace: time.Hour}}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 0 {
		t.Fatalf("deleted=%d, expected crash/grace candidates retained", result.Deleted)
	}
}

func TestCollectorRootChangeAbortsNextBatch(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"a": {Key: "a", Digest: digest("a"), CreatedAt: now.Add(-time.Hour)}, "b": {Key: "b", Digest: digest("b"), CreatedAt: now.Add(-time.Hour)}}}
	store.onDelete = func() { control.stale = true }
	result, err := (Collector{Control: control, Store: store, Inspector: fakeInspector{}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", BatchSize: 1, Now: func() time.Time { return now }}}).Run(context.Background())
	if !errors.Is(err, ErrGCStale) {
		t.Fatalf("err=%v", err)
	}
	if result.Deleted != 1 || len(store.deleted) != 1 {
		t.Fatalf("deleted=%d keys=%v", result.Deleted, store.deleted)
	}
}

func TestCollectorRejectsProviderVersionReplacement(t *testing.T) {
	now := time.Now().UTC()
	control := &fakeControl{rootSet: deployment.RootSet{PhysicalPoolID: "pool", Revision: 1}}
	store := &fakeStore{objects: map[string]Object{"orphan": {Key: "orphan", Digest: digest("o"), Version: "v1", CreatedAt: now.Add(-time.Hour)}}, replaceOnDelete: true}
	_, err := (Collector{Control: control, Store: store, Inspector: fakeInspector{}, Config: Config{PhysicalPoolID: "pool", HolderID: "holder", Now: func() time.Time { return now }}}).Run(context.Background())
	if !errors.Is(err, ErrDeleteUncertain) {
		t.Fatalf("err=%v", err)
	}
	if len(store.deleted) != 0 || len(control.intents) != 1 || control.intents[0].Status != deployment.DeliveryGCDeletePending {
		t.Fatalf("replacement delete=%v intents=%#v", store.deleted, control.intents)
	}
}
