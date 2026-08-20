package workload

import (
	"context"
	"sync"
	"testing"
	"time"
)

type qualificationNoopObserver struct{}

func (qualificationNoopObserver) ObserveWorkload(Stats)           {}
func (qualificationNoopObserver) ObserveAdmission(AdmissionEvent) {}

func TestQualificationRaceAcquireGrantCancel(t *testing.T) {
	config := qualificationConfig()
	config.MaximumRunning = 2
	config.MaximumQueued = 64
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	held := qualificationAcquire(t, controller, qualificationRequest("interactive", "held"))
	held2 := qualificationAcquire(t, controller, qualificationRequest("batch", "held-2"))

	var wg sync.WaitGroup
	var leasesMu sync.Mutex
	leases := make([]Lease, 0, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			lease, err := controller.Acquire(ctx, qualificationRequest("interactive", "racer"))
			if err == nil {
				leasesMu.Lock()
				leases = append(leases, lease)
				leasesMu.Unlock()
			}
		}(i)
	}
	// Grant and cancellation race while waiters are present.
	go held.Release()
	go held2.Release()
	wg.Wait()
	leasesMu.Lock()
	for _, lease := range leases {
		go lease.Release()
	}
	leasesMu.Unlock()
	qualificationWaitFor(t, func() bool { return controller.Stats().Running == 0 && controller.Stats().Queued == 0 })
}

func TestQualificationRaceReleaseShutdownAndStatistics(t *testing.T) {
	config := qualificationConfig()
	config.MaximumRunning = 3
	config.MaximumQueued = 64
	controller, err := New(config, WithObserver(qualificationNoopObserver{}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var leasesMu sync.Mutex
	leases := make([]Lease, 0, 64)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := qualificationRequest(config.Classes[i%len(config.Classes)], "shutdown-racer")
			lease, err := controller.Acquire(context.Background(), request)
			if err == nil {
				leasesMu.Lock()
				leases = append(leases, lease)
				leasesMu.Unlock()
			}
		}(i)
	}
	var reads sync.WaitGroup
	reads.Add(2)
	go func() {
		defer reads.Done()
		for i := 0; i < 100; i++ {
			_ = controller.Stats()
		}
	}()
	go func() {
		defer reads.Done()
		for i := 0; i < 50; i++ {
			controller.SetObserver(qualificationNoopObserver{})
		}
	}()
	go func() {
		time.Sleep(time.Millisecond)
		controller.Close()
		controller.Close()
	}()
	wg.Wait()
	reads.Wait()
	leasesMu.Lock()
	for _, lease := range leases {
		lease.Release()
	}
	leasesMu.Unlock()
	qualificationWaitFor(t, func() bool { return controller.Stats().Running == 0 && controller.Stats().Queued == 0 })
	if got := controller.Stats(); !got.Closed {
		t.Fatalf("controller was not closed: %+v", got)
	}
}

func TestQualificationRaceNestedRelease(t *testing.T) {
	controller, err := New(qualificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := qualificationRequest("interactive", "nested-racer")
	parent := qualificationAcquire(t, controller, request)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nested, err := controller.Acquire(parent.Context(), request)
			if err == nil {
				nested.Release()
			}
		}()
	}
	go parent.Release()
	wg.Wait()
	parent.Release()
	qualificationWaitFor(t, func() bool { return controller.Stats().Running == 0 })
}

func TestQualificationRaceCancelShutdownEmitsOneTerminalEvent(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		observer := &qualificationObserver{}
		controller, err := New(Config{
			Classes:        []Class{"class"},
			Policies:       map[Class]Policy{"class": {MaximumRunning: 1, MaximumQueued: 1}},
			MaximumRunning: 1,
			MaximumQueued:  1,
		}, WithObserver(observer))
		if err != nil {
			t.Fatal(err)
		}
		holder := qualificationAcquire(t, controller, schedulerRequest("class", "holder"))
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, acquireErr := controller.Acquire(ctx, schedulerRequest("class", "queued"))
			result <- acquireErr
		}()
		qualificationWaitQueued(t, controller, 1)
		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() { defer racers.Done(); <-start; cancel() }()
		go func() { defer racers.Done(); <-start; controller.Close() }()
		close(start)
		racers.Wait()
		if err := <-result; err == nil {
			t.Fatal("cancel/shutdown race unexpectedly admitted queued work")
		}
		holder.Release()

		observer.mu.Lock()
		terminal := 0
		for _, event := range observer.events {
			if event.PrincipalID == "queued" && (event.Outcome == OutcomeCanceled || event.Outcome == OutcomeRejected) {
				terminal++
			}
		}
		observer.mu.Unlock()
		if terminal != 1 {
			t.Fatalf("iteration %d emitted %d terminal events for queued request, want 1", iteration, terminal)
		}
	}
}
