package workload

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func qualificationConfig() Config {
	return Config{
		Classes: []Class{"interactive", "batch"},
		Policies: map[Class]Policy{
			"interactive": {ReservedRunning: 1, MaximumRunning: 2, MaximumQueued: 8, MaximumMemoryBytes: 32},
			"batch":       {MaximumRunning: 2, MaximumQueued: 8, MaximumMemoryBytes: 32},
		},
		MaximumRunning:                 2,
		MaximumQueued:                  16,
		MaximumMemoryBytes:             64,
		MaximumRunningPerPrincipal:     2,
		MaximumQueuedPerPrincipal:      8,
		MaximumMemoryBytesPerPrincipal: 48,
		MaximumRunningPerGroup:         2,
		MaximumQueuedPerGroup:          8,
		MaximumMemoryBytesPerGroup:     48,
	}
}

func qualificationRequest(class Class, principal string) Request {
	return Request{Class: class, PrincipalID: principal, Operation: "qualification.run", EstimatedMemoryBytes: 1}
}

func qualificationAcquire(t *testing.T, c *Controller, request Request) Lease {
	t.Helper()
	lease, err := c.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("Acquire(%+v): %v", request, err)
	}
	return lease
}

func qualificationWaitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("condition did not become true")
		case <-ticker.C:
		}
	}
}

func qualificationWaitQueued(t *testing.T, c *Controller, n int) {
	qualificationWaitFor(t, func() bool { return c.Stats().Queued >= n })
}

func TestQualificationDeterministicAdmissionAndAccounting(t *testing.T) {
	config := qualificationConfig()
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Acquire(context.Background(), Request{Class: "unknown", PrincipalID: "p", Operation: "run", EstimatedMemoryBytes: 1}); !IsReason(err, InvalidClass) {
		t.Fatalf("unknown class error = %v, want invalid class", err)
	}
	if _, err := controller.Acquire(context.Background(), Request{Class: "batch", PrincipalID: "p", Operation: "run", EstimatedMemoryBytes: 33}); !IsReason(err, ClassMemoryLimit) {
		t.Fatalf("class memory error = %v, want class memory limit", err)
	}
	if got := controller.Stats(); got.Running != 0 || got.Queued != 0 || got.MemoryBytes != 0 {
		t.Fatalf("rejected requests changed accounting: %+v", got)
	}

	first := qualificationAcquire(t, controller, qualificationRequest("interactive", "p1"))
	second := qualificationAcquire(t, controller, qualificationRequest("batch", "p2"))
	stats := controller.Stats()
	if stats.Running != 2 || stats.MemoryBytes != 2 || stats.Classes["interactive"].Running != 1 || stats.Classes["batch"].Running != 1 {
		t.Fatalf("running accounting = %+v", stats)
	}
	if stats.Principals["p1"].Running != 1 || stats.Principals["p2"].Running != 1 {
		t.Fatalf("principal accounting = %+v", stats.Principals)
	}
	first.Release()
	first.Release()
	second.Release()
	second.Release()
	if got := controller.Stats(); got.Running != 0 || got.MemoryBytes != 0 || got.Queued != 0 {
		t.Fatalf("release accounting = %+v", got)
	}
}

func TestQualificationClassOrderAndReservedCapacity(t *testing.T) {
	config := qualificationConfig()
	config.MaximumRunning = 1
	config.MaximumRunningPerPrincipal = 1
	config.MaximumRunningPerGroup = 1
	config.Policies["interactive"] = Policy{ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 8}
	config.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 8}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	held := qualificationAcquire(t, controller, qualificationRequest("batch", "holder"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	interactiveResult := make(chan struct {
		lease Lease
		err   error
	}, 1)
	batchResult := make(chan struct {
		lease Lease
		err   error
	}, 1)
	go func() {
		lease, err := controller.Acquire(ctx, qualificationRequest("interactive", "interactive"))
		interactiveResult <- struct {
			lease Lease
			err   error
		}{lease, err}
	}()
	qualificationWaitQueued(t, controller, 1)
	go func() {
		lease, err := controller.Acquire(ctx, qualificationRequest("batch", "batch"))
		batchResult <- struct {
			lease Lease
			err   error
		}{lease, err}
	}()
	qualificationWaitQueued(t, controller, 2)
	held.Release()

	select {
	case result := <-interactiveResult:
		if result.err != nil {
			t.Fatalf("reserved interactive request: %v", result.err)
		}
		result.lease.Release()
	case result := <-batchResult:
		if result.err == nil {
			result.lease.Release()
		}
		t.Fatalf("class reservation/order granted batch first: err=%v", result.err)
	case <-ctx.Done():
		t.Fatal("reserved class did not make progress")
	}
	select {
	case result := <-batchResult:
		if result.err != nil {
			t.Fatalf("batch request after reservation: %v", result.err)
		}
		result.lease.Release()
	case result := <-interactiveResult:
		if result.lease != nil {
			result.lease.Release()
		}
		t.Fatal("unexpected second interactive grant")
	case <-ctx.Done():
		t.Fatal("batch class did not make progress")
	}
}

func TestQualificationActorRotationAndHeadSkipping(t *testing.T) {
	config := qualificationConfig()
	config.MaximumRunning = 1
	config.MaximumRunningPerPrincipal = 1
	config.MaximumRunningPerGroup = 1
	config.Policies["interactive"] = Policy{MaximumRunning: 1, MaximumQueued: 8}
	config.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 8}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	held := qualificationAcquire(t, controller, qualificationRequest("interactive", "holder"))
	type result struct {
		id    string
		lease Lease
		err   error
	}
	results := make(chan result, 3)
	for i, id := range []string{"actor-a-1", "actor-a-2", "actor-b"} {
		id := id
		go func() {
			lease, err := controller.Acquire(context.Background(), qualificationRequest("interactive", map[string]string{"actor-a-1": "actor-a", "actor-a-2": "actor-a", "actor-b": "actor-b"}[id]))
			results <- result{id: id, lease: lease, err: err}
		}()
		qualificationWaitQueued(t, controller, i+1)
	}
	held.Release()
	order := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("queued request %s: %v", got.id, got.err)
			}
			order = append(order, got.id)
			got.lease.Release()
		case <-time.After(time.Second):
			t.Fatal("actor queue made no progress")
		}
	}
	want := []string{"actor-a-1", "actor-b", "actor-a-2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("actor grant order = %v, want %v", order, want)
		}
	}
}

func TestQualificationQueueLimitsAreTypedAndDoNotLeak(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		reason RejectionReason
		group  bool
	}{
		{"instance", func(c *Config) { c.MaximumQueued = 1 }, InstanceQueueFull, false},
		{"class", func(c *Config) { c.Policies["interactive"] = Policy{MaximumRunning: 1, MaximumQueued: 1} }, ClassQueueFull, false},
		{"principal", func(c *Config) { c.MaximumQueuedPerPrincipal = 1 }, PrincipalQueueFull, false},
		{"group", func(c *Config) { c.MaximumQueuedPerGroup = 1 }, GroupQueueFull, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := qualificationConfig()
			config.MaximumRunning = 1
			config.MaximumRunningPerPrincipal = 1
			config.MaximumRunningPerGroup = 1
			config.Policies["interactive"] = Policy{MaximumRunning: 1, MaximumQueued: 8}
			config.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 8}
			test.mutate(&config)
			if test.name == "instance" {
				config.MaximumQueuedPerPrincipal = 1
				config.MaximumQueuedPerGroup = 1
				config.Policies["interactive"] = Policy{MaximumRunning: 1, MaximumQueued: 1}
				config.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 1}
			}
			controller, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			held := qualificationAcquire(t, controller, qualificationRequest("interactive", "holder"))
			request := qualificationRequest("interactive", "p")
			if test.group {
				request.GroupIDs = []string{"g"}
			}
			firstDone := make(chan error, 1)
			go func() {
				lease, err := controller.Acquire(context.Background(), request)
				if lease != nil {
					lease.Release()
				}
				firstDone <- err
			}()
			qualificationWaitQueued(t, controller, 1)
			_, err = controller.Acquire(context.Background(), request)
			if !IsReason(err, test.reason) {
				t.Fatalf("second queue error = %v, want %s", err, test.reason)
			}
			if got := controller.Stats().Queued; got != 1 {
				t.Fatalf("rejected request changed queued count: %d", got)
			}
			held.Release()
			select {
			case err := <-firstDone:
				if err != nil {
					t.Fatalf("first queued request: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("first queued request did not complete")
			}
			if got := controller.Stats(); got.Queued != 0 || got.Running != 0 {
				t.Fatalf("queue terminal accounting = %+v", got)
			}
		})
	}
}

func TestQualificationNestedAdmissionAndConflicts(t *testing.T) {
	controller, err := New(qualificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := qualificationRequest("interactive", "nested-principal")
	parent := qualificationAcquire(t, controller, request)
	nested, err := controller.Acquire(parent.Context(), request)
	if err != nil {
		t.Fatalf("nested Acquire: %v", err)
	}
	if nested.Context() != parent.Context() {
		t.Fatal("nested lease did not preserve parent execution context")
	}
	if got := controller.Stats().Running; got != 1 {
		t.Fatalf("nested admission double-counted running work: %d", got)
	}
	nested.Release()
	if got := controller.Stats().Running; got != 1 {
		t.Fatalf("nested release changed parent accounting: %d", got)
	}
	if _, err := controller.Acquire(parent.Context(), qualificationRequest("batch", "nested-principal")); !IsReason(err, ConflictingNestedAdmission) {
		t.Fatalf("different nested request error = %v, want conflict", err)
	}
	parent.Release()
	if got := controller.Stats().Running; got != 0 {
		t.Fatalf("parent release accounting = %d", got)
	}
}

func TestQualificationCancellationTimeoutAndShutdown(t *testing.T) {
	config := qualificationConfig()
	config.MaximumRunning = 1
	config.MaximumRunningPerPrincipal = 1
	config.MaximumRunningPerGroup = 1
	config.MaximumQueued = 8
	config.Policies["interactive"] = Policy{MaximumRunning: 1, MaximumQueued: 8, QueueTimeout: 20 * time.Millisecond}
	config.Policies["batch"] = Policy{MaximumRunning: 1, MaximumQueued: 8, ExecutionTimeout: 20 * time.Millisecond}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	held := qualificationAcquire(t, controller, qualificationRequest("interactive", "holder"))
	requestCtx, cancel := context.WithCancel(context.Background())
	canceled := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(requestCtx, qualificationRequest("interactive", "cancel"))
		canceled <- err
	}()
	qualificationWaitQueued(t, controller, 1)
	cancel()
	select {
	case got := <-canceled:
		if !errors.Is(got, context.Canceled) {
			t.Fatalf("canceled waiter error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	if got := controller.Stats().Queued; got != 0 {
		t.Fatalf("canceled waiter leaked queue count: %d", got)
	}

	timeoutDone := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(context.Background(), qualificationRequest("interactive", "timeout"))
		timeoutDone <- err
	}()
	qualificationWaitQueued(t, controller, 1)
	select {
	case got := <-timeoutDone:
		if !IsReason(got, QueueTimeout) {
			t.Fatalf("queue timeout error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("queue timeout did not return")
	}
	held.Release()

	execution := qualificationAcquire(t, controller, qualificationRequest("batch", "deadline"))
	if _, ok := execution.Context().Deadline(); !ok {
		t.Fatal("execution timeout was not reflected in lease context")
	}
	select {
	case <-execution.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("execution timeout did not cancel lease context")
	}
	execution.Release()

	active := qualificationAcquire(t, controller, qualificationRequest("interactive", "active"))
	queuedDone := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(context.Background(), qualificationRequest("interactive", "queued"))
		queuedDone <- err
	}()
	qualificationWaitQueued(t, controller, 1)
	controller.Close()
	select {
	case got := <-queuedDone:
		if !IsReason(got, ControllerShutdown) {
			t.Fatalf("shutdown queued error = %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not reject queued waiter")
	}
	select {
	case <-active.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel active lease")
	}
	active.Release()
	controller.Close()
	if _, err := controller.Acquire(context.Background(), qualificationRequest("interactive", "after-close")); !IsReason(err, ControllerShutdown) {
		t.Fatalf("post-shutdown error = %v, want controller shutdown", err)
	}
}

type qualificationObserver struct {
	mu     sync.Mutex
	stats  []Stats
	events []AdmissionEvent
	query  bool
}

func (o *qualificationObserver) ObserveWorkload(stats Stats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.query {
		// The callback intentionally re-enters the controller through the
		// snapshot supplied by the test; this method itself remains mutex-safe.
		_ = stats.Clone()
	}
	o.stats = append(o.stats, stats.Clone())
}

func (o *qualificationObserver) ObserveAdmission(event AdmissionEvent) {
	o.mu.Lock()
	o.events = append(o.events, event.Clone())
	o.mu.Unlock()
}

type qualificationPanicObserver struct{}

func (qualificationPanicObserver) ObserveWorkload(Stats)           { panic("observer panic") }
func (qualificationPanicObserver) ObserveAdmission(AdmissionEvent) { panic("observer panic") }

func TestQualificationObservabilityIsTypedDefensiveAndPanicSafe(t *testing.T) {
	observer := &qualificationObserver{query: true}
	controller, err := New(qualificationConfig(), WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	request := qualificationRequest("interactive", "observed")
	request.GroupIDs = []string{"z", "a", "z"}
	lease := qualificationAcquire(t, controller, request)
	lease.Release()
	if _, err := controller.Acquire(context.Background(), Request{Class: "interactive", PrincipalID: "", Operation: "run", EstimatedMemoryBytes: 1}); !IsReason(err, InvalidPrincipal) {
		t.Fatalf("observed rejection = %v", err)
	}
	observer.mu.Lock()
	if len(observer.events) < 3 {
		t.Fatalf("observed events = %d, want admitted/released/rejected", len(observer.events))
	}
	last := observer.events[len(observer.events)-1]
	observer.mu.Unlock()
	if last.Outcome != OutcomeRejected || last.Reason != InvalidPrincipal {
		t.Fatalf("last event = %+v", last)
	}
	if len(observer.stats) == 0 {
		t.Fatal("observer received no stats")
	}

	controller.SetObserver(qualificationPanicObserver{})
	lease = qualificationAcquire(t, controller, qualificationRequest("batch", "panic-safe"))
	lease.Release()
	if got := controller.Stats(); got.Running != 0 || got.Queued != 0 {
		t.Fatalf("observer panic corrupted accounting: %+v", got)
	}
}

func TestQualificationTypedErrorsRemainMechanicallyInspectable(t *testing.T) {
	controller, err := New(qualificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Acquire(context.Background(), Request{Class: "interactive", PrincipalID: "p", Operation: "run", EstimatedMemoryBytes: 1 << 30})
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Reason == "" {
		t.Fatalf("error = %v, want typed rejection", err)
	}
	if got, ok := ReasonOf(err); !ok || got != rejection.Reason {
		t.Fatalf("ReasonOf = %q, %v for %+v", got, ok, rejection)
	}
	if got := fmt.Sprint(err); got == "" {
		t.Fatal("typed rejection has empty error text")
	}
}
