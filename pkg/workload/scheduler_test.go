package workload

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func schedulerConfig(maxRunning, maxQueued int) Config {
	secondReservation := 0
	if maxRunning > 1 {
		secondReservation = 1
	}
	return Config{
		Classes: []Class{"first", "second"},
		Policies: map[Class]Policy{
			"first":  {ReservedRunning: 1, MaximumRunning: maxRunning, MaximumQueued: maxQueued},
			"second": {ReservedRunning: secondReservation, MaximumRunning: maxRunning, MaximumQueued: maxQueued},
		},
		MaximumRunning: maxRunning,
		MaximumQueued:  maxQueued * 2,
	}
}

func schedulerRequest(class Class, principal string) Request {
	return Request{Class: class, PrincipalID: principal, Operation: "test", EstimatedMemoryBytes: 1}
}

func TestAcquireRejectsAlreadyCanceledParentBeforeGrant(t *testing.T) {
	c, err := New(schedulerConfig(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Acquire(ctx, schedulerRequest("first", "p")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire error = %v, want context canceled", err)
	}
	if got := c.Stats(); got.Running != 0 || got.Queued != 0 {
		t.Fatalf("counters changed for canceled parent: %+v", got)
	}
}

func TestNestedLeaseReferenceKeepsAdmissionAlive(t *testing.T) {
	c, err := New(Config{Classes: []Class{"class"}, Policies: map[Class]Policy{"class": {MaximumRunning: 1}}, MaximumRunning: 1})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := c.Acquire(context.Background(), Request{Class: "class", PrincipalID: "p", Operation: "outer", EstimatedMemoryBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := c.Acquire(outer.Context(), Request{Class: "class", PrincipalID: "p", Operation: "nested", EstimatedMemoryBytes: 99})
	if err != nil {
		t.Fatal(err)
	}
	outer.Release()
	if nested.Context().Err() != nil {
		t.Fatalf("nested context canceled after outer release: %v", nested.Context().Err())
	}
	if got := c.Stats().Running; got != 1 {
		t.Fatalf("running after outer release = %d, want 1", got)
	}
	nested.Release()
	if got := c.Stats().Running; got != 0 {
		t.Fatalf("running after final nested release = %d, want 0", got)
	}
}

func TestActorRotationAndFIFO(t *testing.T) {
	c, err := New(Config{Classes: []Class{"class"}, Policies: map[Class]Policy{"class": {MaximumRunning: 1, MaximumQueued: 8}}, MaximumRunning: 1, MaximumQueued: 8})
	if err != nil {
		t.Fatal(err)
	}
	holder, err := c.Acquire(context.Background(), schedulerRequest("class", "holder"))
	if err != nil {
		t.Fatal(err)
	}
	a1 := make(chan Lease, 1)
	a2 := make(chan Lease, 1)
	b1 := make(chan Lease, 1)
	go func() { l, _ := c.Acquire(context.Background(), schedulerRequest("class", "a")); a1 <- l }()
	for c.Stats().Queued != 1 {
		time.Sleep(time.Millisecond)
	}
	go func() { l, _ := c.Acquire(context.Background(), schedulerRequest("class", "a")); a2 <- l }()
	for c.Stats().Queued != 2 {
		time.Sleep(time.Millisecond)
	}
	go func() { l, _ := c.Acquire(context.Background(), schedulerRequest("class", "b")); b1 <- l }()
	for c.Stats().Queued != 3 {
		time.Sleep(time.Millisecond)
	}
	holder.Release()
	first := <-a1
	first.Release()
	second := <-b1
	second.Release()
	third := <-a2
	third.Release()
}

type panicObserver struct{}

func (panicObserver) ObserveWorkload(Stats)           { panic("observer") }
func (panicObserver) ObserveAdmission(AdmissionEvent) { panic("observer") }

func TestObserverPanicsAreIsolated(t *testing.T) {
	c, err := New(Config{Classes: []Class{"class"}, Policies: map[Class]Policy{"class": {MaximumRunning: 1}}, MaximumRunning: 1}, WithObserver(panicObserver{}))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(context.Background(), schedulerRequest("class", "p"))
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	c.Close()
}

type testTimer struct {
	c    chan time.Time
	once sync.Once
}

func (t *testTimer) C() <-chan time.Time { return t.c }
func (t *testTimer) Stop() bool {
	t.once.Do(func() { close(t.c) })
	return true
}

type testClock struct {
	mu    sync.Mutex
	now   time.Time
	timer *testTimer
}

type nilTimerClock struct{}

func (nilTimerClock) Now() time.Time               { return time.Unix(0, 0) }
func (nilTimerClock) NewTimer(time.Duration) Timer { return nil }

func TestNilQueueTimerFailsClosed(t *testing.T) {
	config := Config{
		Classes: []Class{"class"},
		Policies: map[Class]Policy{"class": {
			MaximumRunning: 1,
			MaximumQueued:  1,
			QueueTimeout:   time.Hour,
		}},
		MaximumRunning: 1,
		MaximumQueued:  1,
	}
	controller, err := New(config, WithClock(nilTimerClock{}))
	if err != nil {
		t.Fatal(err)
	}
	holder, err := controller.Acquire(context.Background(), schedulerRequest("class", "holder"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Acquire(context.Background(), schedulerRequest("class", "queued"))
	if !IsReason(err, AdmissionUnavailable) {
		t.Fatalf("Acquire error = %v, want admission unavailable", err)
	}
	if got := controller.Stats(); got.Queued != 0 || got.Running != 1 {
		t.Fatalf("nil timer leaked accounting: %+v", got)
	}
	holder.Release()
}

type statsObserver struct {
	controller *Controller
	called     chan struct{}
}

func (o *statsObserver) ObserveWorkload(Stats) {
	_ = o.controller.Stats()
	select {
	case o.called <- struct{}{}:
	default:
	}
}
func (*statsObserver) ObserveAdmission(AdmissionEvent) {}

func TestObserverMayQueryController(t *testing.T) {
	controller, err := New(Config{Classes: []Class{"class"}, Policies: map[Class]Policy{"class": {MaximumRunning: 1}}, MaximumRunning: 1})
	if err != nil {
		t.Fatal(err)
	}
	observer := &statsObserver{controller: controller, called: make(chan struct{}, 1)}
	controller.SetObserver(observer)
	select {
	case <-observer.called:
	case <-time.After(time.Second):
		t.Fatal("reentrant observer deadlocked")
	}
}

func TestZeroRuntimeLimitsAndBorrowedStats(t *testing.T) {
	disabled, err := New(Config{
		Classes:        []Class{"disabled"},
		Policies:       map[Class]Policy{"disabled": {MaximumQueued: 1}},
		MaximumRunning: 1,
		MaximumQueued:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := disabled.Acquire(ctx, schedulerRequest("disabled", "actor"))
		result <- acquireErr
	}()
	for disabled.Stats().Queued != 1 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("disabled class error = %v, want cancellation", err)
	}

	noQueue, err := New(Config{
		Classes:        []Class{"class"},
		Policies:       map[Class]Policy{"class": {MaximumRunning: 1}},
		MaximumRunning: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	holder, err := noQueue.Acquire(context.Background(), schedulerRequest("class", "holder"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noQueue.Acquire(context.Background(), schedulerRequest("class", "queued")); !IsReason(err, InstanceQueueFull) {
		t.Fatalf("zero queue error = %v, want instance queue full", err)
	}
	holder.Release()

	borrowed, err := New(Config{
		Classes: []Class{"reserved", "borrower"},
		Policies: map[Class]Policy{
			"reserved": {ReservedRunning: 1, MaximumRunning: 1},
			"borrower": {MaximumRunning: 2},
		},
		MaximumRunning: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := borrowed.Acquire(context.Background(), schedulerRequest("borrower", "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := borrowed.Acquire(context.Background(), schedulerRequest("borrower", "two"))
	if err != nil {
		t.Fatal(err)
	}
	if got := borrowed.Stats().Classes["borrower"].Borrowed; got != 2 {
		t.Fatalf("borrowed stats = %d, want 2", got)
	}
	first.Release()
	second.Release()
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *testClock) NewTimer(time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer = &testTimer{c: make(chan time.Time, 1)}
	return c.timer
}

func TestExecutionDeadlineUsesInjectedClock(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	observer := &qualificationObserver{}
	c, err := New(Config{Classes: []Class{"class"}, Policies: map[Class]Policy{"class": {MaximumRunning: 1, ExecutionTimeout: time.Hour}}, MaximumRunning: 1}, WithClock(clock), WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(context.Background(), schedulerRequest("class", "p"))
	if err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.timer.c <- clock.now.Add(time.Hour)
	clock.mu.Unlock()
	select {
	case <-lease.Context().Done():
		if !errors.Is(lease.Context().Err(), context.DeadlineExceeded) {
			t.Fatalf("context error = %v", lease.Context().Err())
		}
	case <-time.After(time.Second):
		t.Fatal("injected execution timer did not expire")
	}
	lease.Release()
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if got := observer.events[len(observer.events)-1].Outcome; got != OutcomeTimedOut {
		t.Fatalf("terminal outcome = %q, want %q", got, OutcomeTimedOut)
	}
}
