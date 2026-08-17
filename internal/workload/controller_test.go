package workload

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestConfigValidationAndFiniteDefaults(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.MaximumMemoryBytes <= 0 || defaults.MaximumQueued <= 0 || defaults.MaximumRunningPerPrincipal <= 0 || defaults.MaximumQueuedPerPrincipal <= 0 {
		t.Fatal("default workload limits must be finite")
	}
	if _, err := New(Config{}); err != nil {
		t.Fatal(err)
	}
	invalid := defaults
	invalid.MaxRunning = -1
	if _, err := New(invalid); err == nil {
		t.Fatal("negative instance limit accepted")
	}
	invalid = defaults
	invalid.Classes[Interactive] = Policy{ReservedRunning: 2, MaximumRunning: 1}
	if _, err := New(invalid); err == nil {
		t.Fatal("reservation above class maximum accepted")
	}
}

func TestReservedDemandReceivesCapacityBeforeBorrowing(t *testing.T) {
	c := testController(t, Config{MaxRunning: 2, MaximumQueued: 8, Classes: map[Class]Policy{
		Interactive: {ReservedRunning: 1, MaximumRunning: 2, MaximumQueued: 8},
		Refresh:     {ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 8},
	}})
	one := acquire(t, c, Interactive, "one", nil)
	two := acquire(t, c, Interactive, "two", nil)
	i3 := acquireAsync(c, Interactive, "three", nil)
	r1 := acquireAsync(c, Refresh, "refresh", nil)
	waitQueued(t, c, 2)
	one.Release()
	refresh := receiveLease(t, r1)
	assertPending(t, i3)
	refresh.Release()
	two.Release()
	receiveLease(t, i3).Release()
}

func TestActorRoundRobinAndFIFO(t *testing.T) {
	c := testController(t, Config{MaxRunning: 1, MaximumQueued: 8, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 1, MaximumQueued: 8},
	}})
	running := acquire(t, c, Interactive, "holder", nil)
	a1 := acquireAsync(c, Interactive, "a", nil)
	waitQueued(t, c, 1)
	a2 := acquireAsync(c, Interactive, "a", nil)
	waitQueued(t, c, 2)
	b1 := acquireAsync(c, Interactive, "b", nil)
	waitQueued(t, c, 3)
	running.Release()
	first := receiveLease(t, a1)
	first.Release()
	second := receiveLease(t, b1)
	assertPending(t, a2)
	second.Release()
	receiveLease(t, a2).Release()
}

func TestBlockedActorDoesNotCauseHeadOfLineBlocking(t *testing.T) {
	c := testController(t, Config{MaxRunning: 2, MaximumQueued: 8, MaximumRunningPerPrincipal: 1, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 2, MaximumQueued: 8},
	}})
	p1 := acquire(t, c, Interactive, "p1", nil)
	holder := acquire(t, c, Interactive, "holder", nil)
	blocked := acquireAsync(c, Interactive, "p1", nil)
	waitQueued(t, c, 1)
	eligible := acquireAsync(c, Interactive, "p2", nil)
	waitQueued(t, c, 2)
	holder.Release()
	p2 := receiveLease(t, eligible)
	assertPending(t, blocked)
	p2.Release()
	p1.Release()
	receiveLease(t, blocked).Release()
}

func TestBlockedGroupDoesNotCauseHeadOfLineBlocking(t *testing.T) {
	c := testController(t, Config{MaxRunning: 2, MaximumQueued: 8, MaximumRunningPerGroup: 1, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 2, MaximumQueued: 8},
	}})
	groupLease := acquire(t, c, Interactive, "group-owner", []string{"team"})
	holder := acquire(t, c, Interactive, "holder", nil)
	blocked := acquireAsync(c, Interactive, "group-waiter", []string{"team"})
	waitQueued(t, c, 1)
	eligible := acquireAsync(c, Interactive, "other", nil)
	waitQueued(t, c, 2)
	holder.Release()
	other := receiveLease(t, eligible)
	assertPending(t, blocked)
	other.Release()
	groupLease.Release()
	receiveLease(t, blocked).Release()
}

func TestMemoryAccountingAndExactRelease(t *testing.T) {
	c := testController(t, Config{MaxRunning: 2, MaximumQueued: 4, MaximumMemoryBytes: 100, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 2, MaximumQueued: 4, MaximumMemoryBytes: 100},
	}})
	first := acquireMemory(t, c, Interactive, "a", 60)
	second := acquireAsyncMemory(c, Interactive, "b", 50)
	waitQueued(t, c, 1)
	if got := c.Stats().MemoryBytes; got != 60 {
		t.Fatalf("memory while queued = %d, want 60", got)
	}
	first.Release()
	lease := receiveLease(t, second)
	if got := c.Stats().MemoryBytes; got != 50 {
		t.Fatalf("memory after grant = %d, want 50", got)
	}
	lease.Release()
	stats := c.Stats()
	if stats.Running != 0 || stats.Queued != 0 || stats.MemoryBytes != 0 {
		t.Fatalf("unbalanced release: %#v", stats)
	}
}

func TestQueueAndMemoryRejections(t *testing.T) {
	c := testController(t, Config{MaxRunning: 1, MaximumQueued: 1, MaximumMemoryBytes: 100, MaximumQueuedPerGroup: 1, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 1, MaximumQueued: 1, MaximumMemoryBytes: 100},
	}})
	running := acquire(t, c, Interactive, "holder", nil)
	queued := acquireAsync(c, Interactive, "p1", []string{"team"})
	waitQueued(t, c, 1)
	_, err := c.Acquire(context.Background(), Request{Class: Interactive, PrincipalID: "p2", GroupIDs: []string{"team"}, Operation: "query", EstimatedMemoryBytes: 1})
	assertReason(t, err, InstanceQueueFull)
	_, err = c.Acquire(context.Background(), Request{Class: Interactive, PrincipalID: "p2", Operation: "query", EstimatedMemoryBytes: 101})
	assertReason(t, err, InstanceMemoryLimit)
	running.Release()
	receiveLease(t, queued).Release()
}

func TestPrincipalAndGroupQueueCaps(t *testing.T) {
	t.Run("principal", func(t *testing.T) {
		c := testController(t, Config{MaxRunning: 1, MaximumQueued: 4, MaximumQueuedPerPrincipal: 1, Classes: map[Class]Policy{
			Interactive: {MaximumRunning: 1, MaximumQueued: 4},
		}})
		running := acquire(t, c, Interactive, "holder", nil)
		queued := acquireAsync(c, Interactive, "same", nil)
		waitQueued(t, c, 1)
		_, err := c.Acquire(context.Background(), request(Interactive, "same", nil))
		assertReason(t, err, PrincipalQueueFull)
		running.Release()
		receiveLease(t, queued).Release()
	})
	t.Run("group", func(t *testing.T) {
		c := testController(t, Config{MaxRunning: 1, MaximumQueued: 4, MaximumQueuedPerGroup: 1, Classes: map[Class]Policy{
			Interactive: {MaximumRunning: 1, MaximumQueued: 4},
		}})
		running := acquire(t, c, Interactive, "holder", nil)
		queued := acquireAsync(c, Interactive, "first", []string{"team"})
		waitQueued(t, c, 1)
		_, err := c.Acquire(context.Background(), request(Interactive, "second", []string{"team"}))
		assertReason(t, err, GroupQueueFull)
		running.Release()
		receiveLease(t, queued).Release()
	})
}

func TestGroupNormalizationAndNestedReuse(t *testing.T) {
	c := testController(t, Config{MaxRunning: 1, Classes: map[Class]Policy{Interactive: {MaximumRunning: 1}}})
	outer := acquireMemory(t, c, Interactive, "actor", 7, "z", "a", "a")
	if got := outer.Context(); got == nil {
		t.Fatal("nil execution context")
	}
	class, principal, ok := Current(outer.Context())
	if !ok || class != Interactive || principal != "actor" {
		t.Fatalf("Current() = %s/%s/%v", class, principal, ok)
	}
	nested, err := c.Acquire(outer.Context(), Request{Class: Interactive, PrincipalID: "actor", GroupIDs: []string{"a", "z"}, Operation: "nested", EstimatedMemoryBytes: 7})
	if err != nil {
		t.Fatal(err)
	}
	if nested.Context() != outer.Context() {
		t.Fatal("equivalent nested admission did not reuse context")
	}
	nested.Release()
	_, err = c.Acquire(outer.Context(), Request{Class: Interactive, PrincipalID: "actor", GroupIDs: []string{"other"}, Operation: "conflict", EstimatedMemoryBytes: 7})
	assertReason(t, err, ConflictingNestedAdmission)
	outer.Release()
}

func TestCallerGroupSliceCannotMutateAccountingOrEvents(t *testing.T) {
	observer := &captureObserver{}
	c, err := New(Config{MaxRunning: 1, Classes: map[Class]Policy{Interactive: {MaximumRunning: 1}}}, WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	groups := []string{"z", "a", "a"}
	lease, err := c.Acquire(context.Background(), Request{Class: Interactive, PrincipalID: "actor", GroupIDs: groups, Operation: "test", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	groups[0], groups[1], groups[2] = "mutated", "other", "changed"
	stats := c.Stats()
	if _, ok := stats.Groups["z"]; !ok {
		t.Fatalf("normalized group missing from stats: %#v", stats.Groups)
	}
	if _, ok := stats.Groups["mutated"]; ok {
		t.Fatalf("caller mutation changed accounting: %#v", stats.Groups)
	}
	events := observer.Events()
	if len(events) == 0 || !reflect.DeepEqual(events[0].GroupIDs, []string{"a", "z"}) {
		t.Fatalf("admission event groups = %#v", events)
	}
	lease.Release()
}

func TestQueueTimeoutCancellationAndShutdown(t *testing.T) {
	c := testController(t, Config{MaxRunning: 1, MaximumQueued: 4, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 1, MaximumQueued: 4, QueueTimeout: 20 * time.Millisecond},
	}})
	running := acquire(t, c, Interactive, "holder", nil)
	_, err := c.Acquire(context.Background(), Request{Class: Interactive, PrincipalID: "timeout", Operation: "query", EstimatedMemoryBytes: 1})
	assertReason(t, err, QueueTimeout)
	ctx, cancel := context.WithCancel(context.Background())
	canceled := acquireAsyncContext(c, ctx, Interactive, "cancel", nil)
	waitQueued(t, c, 1)
	cancel()
	if err := receiveError(t, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	waitQueued(t, c, 0)
	shutdown := acquireAsync(c, Interactive, "shutdown", nil)
	waitQueued(t, c, 1)
	c.Close()
	assertReason(t, receiveError(t, shutdown), ControllerShutdown)
	select {
	case <-running.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel running context")
	}
	running.Release()
}

func TestExecutionDeadlineAndIdempotentRelease(t *testing.T) {
	c := testController(t, Config{MaxRunning: 1, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 1, ExecutionTimeout: 20 * time.Millisecond},
	}})
	lease := acquire(t, c, Interactive, "actor", nil)
	select {
	case <-lease.Context().Done():
		if !errors.Is(lease.Context().Err(), context.DeadlineExceeded) {
			t.Fatalf("context error = %v", lease.Context().Err())
		}
	case <-time.After(time.Second):
		t.Fatal("execution context did not expire")
	}
	lease.Release()
	lease.Release()
	if got := c.Stats().Running; got != 0 {
		t.Fatalf("running = %d", got)
	}
}

func TestObserverCallbacksRunOutsideControllerLock(t *testing.T) {
	c, err := New(Config{MaxRunning: 1, Classes: map[Class]Policy{Interactive: {MaximumRunning: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	observer := &reentrantObserver{controller: c, observed: make(chan struct{}, 1)}
	c.SetObserver(observer)
	select {
	case <-observer.observed:
	case <-time.After(time.Second):
		t.Fatal("observer callback did not complete")
	}
	c.Close()
}

func TestConcurrentStatsReleaseAndCancellation(t *testing.T) {
	c := testController(t, Config{MaxRunning: 4, MaximumQueued: 64, Classes: map[Class]Policy{
		Interactive: {MaximumRunning: 4, MaximumQueued: 64},
	}})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lease, err := c.Acquire(ctx, Request{Class: Interactive, PrincipalID: "race", Operation: "race", EstimatedMemoryBytes: 1})
			if err == nil {
				_ = c.Stats()
				lease.Release()
				lease.Release()
			}
		}()
	}
	wg.Wait()
	stats := c.Stats()
	if stats.Running != 0 || stats.Queued != 0 || stats.MemoryBytes != 0 {
		t.Fatalf("unbalanced stats: %#v", stats)
	}
}

type reentrantObserver struct {
	controller *Controller
	observed   chan struct{}
}

func (o *reentrantObserver) ObserveWorkload(Stats) {
	_ = o.controller.Stats()
	select {
	case o.observed <- struct{}{}:
	default:
	}
}
func (*reentrantObserver) ObserveAdmission(AdmissionEvent) {}

type captureObserver struct {
	mu     sync.Mutex
	events []AdmissionEvent
}

func (*captureObserver) ObserveWorkload(Stats) {}
func (o *captureObserver) ObserveAdmission(event AdmissionEvent) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}
func (o *captureObserver) Events() []AdmissionEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]AdmissionEvent(nil), o.events...)
}

type asyncResult struct {
	lease Lease
	err   error
}

func testController(t *testing.T, cfg Config) *Controller {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func request(class Class, principal string, groups []string) Request {
	return Request{Class: class, PrincipalID: principal, GroupIDs: groups, Operation: "test", EstimatedMemoryBytes: 1}
}

func acquire(t *testing.T, c *Controller, class Class, principal string, groups []string) Lease {
	t.Helper()
	lease, err := c.Acquire(context.Background(), request(class, principal, groups))
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func acquireMemory(t *testing.T, c *Controller, class Class, principal string, memory int64, groups ...string) Lease {
	t.Helper()
	r := request(class, principal, groups)
	r.EstimatedMemoryBytes = memory
	lease, err := c.Acquire(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func acquireAsync(c *Controller, class Class, principal string, groups []string) <-chan asyncResult {
	return acquireAsyncContext(c, context.Background(), class, principal, groups)
}

func acquireAsyncMemory(c *Controller, class Class, principal string, memory int64) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		r := request(class, principal, nil)
		r.EstimatedMemoryBytes = memory
		lease, err := c.Acquire(context.Background(), r)
		ch <- asyncResult{lease: lease, err: err}
	}()
	return ch
}

func acquireAsyncContext(c *Controller, ctx context.Context, class Class, principal string, groups []string) <-chan asyncResult {
	ch := make(chan asyncResult, 1)
	go func() {
		lease, err := c.Acquire(ctx, request(class, principal, groups))
		ch <- asyncResult{lease: lease, err: err}
	}()
	return ch
}

func receiveLease(t *testing.T, ch <-chan asyncResult) Lease {
	t.Helper()
	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.lease
	case <-time.After(time.Second):
		t.Fatal("admission was not granted")
		return nil
	}
}

func receiveError(t *testing.T, ch <-chan asyncResult) error {
	t.Helper()
	select {
	case result := <-ch:
		return result.err
	case <-time.After(time.Second):
		t.Fatal("admission did not finish")
		return nil
	}
}

func assertPending(t *testing.T, ch <-chan asyncResult) {
	t.Helper()
	select {
	case result := <-ch:
		if result.err == nil && result.lease != nil {
			result.lease.Release()
		}
		t.Fatal("admission granted out of order")
	case <-time.After(20 * time.Millisecond):
	}
}

func waitQueued(t *testing.T, c *Controller, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c.Stats().Queued == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued = %d, want %d", c.Stats().Queued, want)
}

func assertReason(t *testing.T, err error, want RejectionReason) {
	t.Helper()
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Reason != want {
		t.Fatalf("error = %v, want rejection %s", err, want)
	}
}
