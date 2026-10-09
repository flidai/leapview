package materialize

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

// The owner finishes before revocation, but its already-joined waiter must
// recheck durable authority before releasing the shared result. Blocking in
// preflight would not exercise that boundary.
func TestADR0026PostgreSQLRevocationDeniesAlreadyJoinedArrowWaiter(t *testing.T) {
	fixture := newADR0026PostgresAccessFixture(t)
	cache := newQueryResultCache(4)
	t.Cleanup(func() { _ = cache.close() })
	partition := materializeTestPartition(t, resultidentity.PartitionProduction, "")
	dependency := protectedCacheTestDependency(t, semanticCacheTestIdentity())
	request := semanticCacheTestRequest()
	initial := fixture.controlState(t)
	guard := fixture.controlGuard(initial)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	postflight, allowPostflight := make(chan struct{}), make(chan struct{})
	var postflightOnce sync.Once
	t.Cleanup(func() { postflightOnce.Do(func() { close(allowPostflight) }) })
	type response struct {
		result dataquery.Result
		err    error
	}
	ownerDone, waiterDone := make(chan response, 1), make(chan response, 1)
	awaitResponse := func(done <-chan response, phase string) response {
		t.Helper()
		select {
		case got := <-done:
			return got
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", phase)
			return response{}
		}
	}
	var executions atomic.Int32
	go func() {
		result, err := cache.executeArrow(t.Context(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
			executions.Add(1)
			close(started)
			<-release
			return newProtectedCacheArrowExecution()
		}, guard)
		ownerDone <- response{result, err}
	}()
	waitADR0026Boundary(t, started, "owner physical execution")
	joined := &adr0026JoinedContext{Context: t.Context(), joined: make(chan struct{})}
	var waiterChecks atomic.Int32
	go func() {
		result, err := cache.executeArrow(joined, request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
			executions.Add(1)
			return arrowQueryExecution{}, errors.New("waiter became a second owner")
		}, func(ctx context.Context) error {
			if waiterChecks.Add(1) == 2 {
				close(postflight)
				<-allowPostflight
			}
			err := guard(ctx)
			// After successful preflight, no remaining cache lookup reads Done;
			// CoalesceArrow next evaluates it only after joinArrowFlight returns.
			joined.armed.Store(err == nil)
			return err
		})
		waiterDone <- response{result, err}
	}()
	waitADR0026Boundary(t, joined.joined, "waiter joining the existing Arrow flight")
	releaseOnce.Do(func() { close(release) })
	select {
	case <-postflight:
	case got := <-waiterDone:
		t.Fatalf("joined waiter bypassed its output authority check: rows=%d error=%v", len(got.result.Rows), got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("joined waiter did not reach its output authority check")
	}
	owner := awaitResponse(ownerDone, "owner result before revocation")
	if owner.err != nil || len(owner.result.Rows) != 1 {
		t.Fatalf("owner completed before revocation: rows=%d error=%v", len(owner.result.Rows), owner.err)
	}
	fixture.narrowToEMEA(t)
	postflightOnce.Do(func() { close(allowPostflight) })
	waiter := awaitResponse(waiterDone, "revoked joined waiter result")
	if !errors.Is(waiter.err, errADR0026AuthorityChanged) || waiter.result.Rows != nil {
		t.Fatalf("revoked joined waiter received rows=%d error=%v", len(waiter.result.Rows), waiter.err)
	}
	if executions.Load() != 1 {
		t.Fatalf("physical executions=%d, want one shared execution", executions.Load())
	}
	fixture.restoreAllRegions(t)
	fresh, err := cache.executeArrow(t.Context(), request, partition, dependency, "SELECT 1", nowForCacheTest(), func(context.Context) (arrowQueryExecution, error) {
		return newProtectedCacheArrowExecution()
	}, fixture.controlGuard(fixture.controlState(t)))
	if err != nil || fresh.CacheOutcome != dataquery.CacheMiss || len(fresh.Rows) != 1 {
		t.Fatalf("fresh authority after denied waiter: outcome=%q rows=%d error=%v", fresh.CacheOutcome, len(fresh.Rows), err)
	}
}

type adr0026JoinedContext struct {
	context.Context
	armed  atomic.Bool
	once   sync.Once
	joined chan struct{}
}

func (c *adr0026JoinedContext) Done() <-chan struct{} {
	if c.armed.Load() {
		c.once.Do(func() { close(c.joined) })
	}
	return c.Context.Done()
}

func waitADR0026Boundary(t *testing.T, boundary <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-boundary:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
