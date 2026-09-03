package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	analyticsl3 "github.com/flidai/leapview/internal/analytics/cache/l3"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/google/uuid"
)

const testL3SecurityDomain = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type l3GCCollectorFunc func(context.Context, string) (analyticsl3.GCResult, error)

func (f l3GCCollectorFunc) GCPage(ctx context.Context, after string) (analyticsl3.GCResult, error) {
	return f(ctx, after)
}

type l3GCTestAuthority struct {
	mu sync.Mutex

	lease       cachepostgres.L3GCLease
	acquireErr  error
	renewErr    error
	releaseErr  error
	advanceErr  error
	acquires    int
	renews      int
	releases    int
	advances    int
	advanceNext string
	complete    bool
}

func (a *l3GCTestAuthority) AcquireL3GCLease(context.Context, string, string, time.Duration) (cachepostgres.L3GCLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acquires++
	return a.lease, a.acquireErr
}

func (a *l3GCTestAuthority) RenewL3GCLease(context.Context, cachepostgres.L3GCLease, time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.renews++
	return a.renewErr
}

func (a *l3GCTestAuthority) ReleaseL3GCLease(context.Context, cachepostgres.L3GCLease) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.releases++
	return a.releaseErr
}

func (a *l3GCTestAuthority) AdvanceL3GCCursor(_ context.Context, _ cachepostgres.L3GCLease, next string, complete bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.advances++
	a.advanceNext = next
	a.complete = complete
	return a.advanceErr
}

func (a *l3GCTestAuthority) snapshot() (acquires, renews, releases, advances int, next string, complete bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.acquires, a.renews, a.releases, a.advances, a.advanceNext, a.complete
}

func testL3GCLease(cursor string) cachepostgres.L3GCLease {
	return cachepostgres.L3GCLease{
		LeaseID: uuid.New(), StorageSecurityDomain: testL3SecurityDomain, OwnerID: "instance-a",
		FencingEpoch: 1, CursorObjectKey: cursor, Cycle: 3,
	}
}

func testL3WorkloadAcquire(released *atomic.Int32) func(context.Context) (workloadmodule.Lease, error) {
	return func(context.Context) (workloadmodule.Lease, error) {
		return &retentionWorkerLease{ctx: context.Background(), released: released}, nil
	}
}

func TestL3GCWorkerCommitsPageCursorAndReleasesLeases(t *testing.T) {
	const cursor = "cache/l3/sd/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/old"
	const next = "cache/l3/sd/sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/next"
	authority := &l3GCTestAuthority{lease: testL3GCLease(cursor)}
	var released atomic.Int32
	worker := newL3GCWorker(l3GCWorkerConfig{
		SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(_ context.Context, after string) (analyticsl3.GCResult, error) {
			if after != cursor {
				t.Fatalf("collector cursor = %q, want %q", after, cursor)
			}
			return analyticsl3.GCResult{Scanned: 2, Deleted: 1, NextCursor: next}, nil
		}),
		Acquire: testL3WorkloadAcquire(&released),
	})

	worker.runPass(context.Background())

	acquires, _, releases, advances, gotNext, complete := authority.snapshot()
	if acquires != 1 || releases != 1 || advances != 1 {
		t.Fatalf("authority calls acquire/release/advance = %d/%d/%d, want 1/1/1", acquires, releases, advances)
	}
	if gotNext != next || complete {
		t.Fatalf("cursor commit = (%q, %t), want (%q, false)", gotNext, complete, next)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("workload lease releases = %d, want 1", got)
	}
}

func TestL3GCWorkerCompletesCycleAtEndOfListing(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease("cursor")}
	worker := newL3GCWorker(l3GCWorkerConfig{
		SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(context.Context, string) (analyticsl3.GCResult, error) {
			return analyticsl3.GCResult{}, nil
		}),
		Acquire: testL3WorkloadAcquire(nil),
	})

	worker.runPass(context.Background())

	_, _, releases, advances, next, complete := authority.snapshot()
	if releases != 1 || advances != 1 || next != "" || !complete {
		t.Fatalf("terminal commit release/advance/next/complete = %d/%d/%q/%t", releases, advances, next, complete)
	}
}

func TestL3GCWorkerDoesNotAdvanceFailedPage(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease("cursor")}
	worker := newL3GCWorker(l3GCWorkerConfig{
		SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(context.Context, string) (analyticsl3.GCResult, error) {
			return analyticsl3.GCResult{}, errors.New("object store unavailable")
		}),
		Acquire: testL3WorkloadAcquire(nil),
	})

	worker.runPass(context.Background())

	_, _, releases, advances, _, _ := authority.snapshot()
	if releases != 1 || advances != 0 {
		t.Fatalf("release/advance calls = %d/%d, want 1/0", releases, advances)
	}
}

func TestL3GCWorkerSkipsWhenWorkloadIsSaturated(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease("")}
	var collectorCalls atomic.Int32
	worker := newL3GCWorker(l3GCWorkerConfig{
		SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(context.Context, string) (analyticsl3.GCResult, error) {
			collectorCalls.Add(1)
			return analyticsl3.GCResult{}, nil
		}),
		Acquire: func(context.Context) (workloadmodule.Lease, error) { return nil, errors.New("saturated") },
	})

	worker.runPass(context.Background())

	acquires, _, _, _, _, _ := authority.snapshot()
	if acquires != 0 || collectorCalls.Load() != 0 {
		t.Fatalf("GC authority/collector calls = %d/%d, want 0/0", acquires, collectorCalls.Load())
	}
}

func TestL3GCWorkerSkipsAlreadyExpiredWorkloadLease(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease("")}
	leaseCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var released atomic.Int32
	var collectorCalls atomic.Int32
	worker := newL3GCWorker(l3GCWorkerConfig{
		SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(context.Context, string) (analyticsl3.GCResult, error) {
			collectorCalls.Add(1)
			return analyticsl3.GCResult{}, nil
		}),
		Acquire: func(context.Context) (workloadmodule.Lease, error) {
			return &retentionWorkerLease{ctx: leaseCtx, released: &released}, nil
		},
	})

	worker.runPass(context.Background())

	acquires, _, _, _, _, _ := authority.snapshot()
	if acquires != 0 || collectorCalls.Load() != 0 {
		t.Fatalf("GC authority/collector calls = %d/%d, want 0/0", acquires, collectorCalls.Load())
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("workload lease releases = %d, want 1", got)
	}
}

func TestL3GCWorkerStopCancelsBlockingPage(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease("")}
	started := make(chan struct{})
	worker := newL3GCWorker(l3GCWorkerConfig{
		Interval: time.Hour, SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(ctx context.Context, _ string) (analyticsl3.GCResult, error) {
			close(started)
			<-ctx.Done()
			return analyticsl3.GCResult{}, ctx.Err()
		}),
		Acquire: testL3WorkloadAcquire(nil),
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial GC page did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	_, _, releases, advances, _, _ := authority.snapshot()
	if releases != 1 || advances != 0 {
		t.Fatalf("release/advance calls = %d/%d, want 1/0", releases, advances)
	}
}

func TestL3GCWorkerCancelsPageWhenLeaseRenewalIsLost(t *testing.T) {
	authority := &l3GCTestAuthority{lease: testL3GCLease(""), renewErr: cachepostgres.ErrStaleFence}
	worker := newL3GCWorker(l3GCWorkerConfig{
		LeaseDuration: 300 * time.Millisecond, SecurityDomain: testL3SecurityDomain, OwnerID: "instance-a", Authority: authority,
		Collector: l3GCCollectorFunc(func(ctx context.Context, _ string) (analyticsl3.GCResult, error) {
			<-ctx.Done()
			return analyticsl3.GCResult{}, ctx.Err()
		}),
		Acquire: testL3WorkloadAcquire(nil),
	})
	done := make(chan struct{})
	go func() {
		worker.runPass(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("GC page was not canceled after durable lease renewal failed")
	}
	_, renews, releases, advances, _, _ := authority.snapshot()
	if renews == 0 || releases != 1 || advances != 0 {
		t.Fatalf("renew/release/advance calls = %d/%d/%d, want >=1/1/0", renews, releases, advances)
	}
}
