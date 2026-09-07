package workload

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

type sustainedWorkloadObservation struct {
	mu             sync.Mutex
	maxRunning     int
	maxQueued      int
	maxMemory      int64
	admitted       int
	terminal       int
	byClass        map[Class]int
	byPrincipal    map[string]int
	violations     []string
	maximumRunning int
	maximumQueued  int
	maximumMemory  int64
}

type sustainedWorkloadSnapshot struct {
	maxQueued   int
	admitted    int
	terminal    int
	byClass     map[Class]int
	byPrincipal map[string]int
	violations  []string
}

func newSustainedWorkloadObservation(config Config) *sustainedWorkloadObservation {
	return &sustainedWorkloadObservation{
		byClass:        make(map[Class]int),
		byPrincipal:    make(map[string]int),
		maximumRunning: config.MaximumRunning,
		maximumQueued:  config.MaximumQueued,
		maximumMemory:  config.MaximumMemoryBytes,
	}
}

func (o *sustainedWorkloadObservation) ObserveWorkload(stats Stats) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if stats.Running > o.maxRunning {
		o.maxRunning = stats.Running
	}
	if stats.Queued > o.maxQueued {
		o.maxQueued = stats.Queued
	}
	if stats.MemoryBytes > o.maxMemory {
		o.maxMemory = stats.MemoryBytes
	}
	if stats.Running > o.maximumRunning {
		o.violationLocked(fmt.Sprintf("running=%d exceeds %d", stats.Running, o.maximumRunning))
	}
	if stats.Queued > o.maximumQueued {
		o.violationLocked(fmt.Sprintf("queued=%d exceeds %d", stats.Queued, o.maximumQueued))
	}
	if stats.MemoryBytes > o.maximumMemory {
		o.violationLocked(fmt.Sprintf("memory=%d exceeds %d", stats.MemoryBytes, o.maximumMemory))
	}
	for class, classStats := range stats.Classes {
		if classStats.Running > classStats.Policy.MaximumRunning {
			o.violationLocked(fmt.Sprintf("class %s running=%d exceeds %d", class, classStats.Running, classStats.Policy.MaximumRunning))
		}
	}
	for principal, actorStats := range stats.Principals {
		if actorStats.Running > 1 {
			o.violationLocked(fmt.Sprintf("principal %s running=%d exceeds 1", principal, actorStats.Running))
		}
	}
	for group, actorStats := range stats.Groups {
		if actorStats.Running > 2 {
			o.violationLocked(fmt.Sprintf("group %s running=%d exceeds 2", group, actorStats.Running))
		}
	}
}

func (o *sustainedWorkloadObservation) ObserveAdmission(event AdmissionEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch event.Outcome {
	case OutcomeAdmitted:
		o.admitted++
		o.byClass[event.Class]++
		o.byPrincipal[event.PrincipalID]++
	case OutcomeReleased, OutcomeCanceled, OutcomeTimedOut:
		o.terminal++
	}
}

func (o *sustainedWorkloadObservation) violationLocked(message string) {
	if len(o.violations) == 0 || o.violations[len(o.violations)-1] != message {
		o.violations = append(o.violations, message)
	}
}

func (o *sustainedWorkloadObservation) snapshot() sustainedWorkloadSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	snapshot := sustainedWorkloadSnapshot{
		maxQueued:   o.maxQueued,
		admitted:    o.admitted,
		terminal:    o.terminal,
		byClass:     make(map[Class]int, len(o.byClass)),
		byPrincipal: make(map[string]int, len(o.byPrincipal)),
		violations:  append([]string(nil), o.violations...),
	}
	for class, count := range o.byClass {
		snapshot.byClass[class] = count
	}
	for principal, count := range o.byPrincipal {
		snapshot.byPrincipal[principal] = count
	}
	return snapshot
}

func waitForSustainedQueue(c *Controller, want int, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if c.Stats().Queued >= want {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

// TestQualificationSustainedWorkload keeps a contended controller busy for a
// bounded interval. It verifies that queueing, class/actor fairness, resource
// limits, and cancellation leave no accounting behind after the run drains.
func TestQualificationSustainedWorkload(t *testing.T) {
	config := Config{
		Classes: []Class{"interactive", "batch"},
		Policies: map[Class]Policy{
			"interactive": {ReservedRunning: 1, MaximumRunning: 3, MaximumQueued: 32, MaximumMemoryBytes: 8},
			"batch":       {MaximumRunning: 3, MaximumQueued: 32, MaximumMemoryBytes: 8},
		},
		MaximumRunning:                 4,
		MaximumQueued:                  32,
		MaximumMemoryBytes:             8,
		MaximumRunningPerPrincipal:     1,
		MaximumQueuedPerPrincipal:      4,
		MaximumMemoryBytesPerPrincipal: 2,
		MaximumRunningPerGroup:         2,
		MaximumQueuedPerGroup:          16,
		MaximumMemoryBytesPerGroup:     4,
	}
	observation := newSustainedWorkloadObservation(config)
	controller, err := New(config, WithObserver(observation))
	if err != nil {
		t.Fatal(err)
	}

	// Fill every running slot before starting the workers so the qualification
	// always observes real queue pressure, even on a very fast machine.
	holders := make([]Lease, 0, config.MaximumRunning)
	for i := 0; i < config.MaximumRunning; i++ {
		class := config.Classes[i%len(config.Classes)]
		holders = append(holders, qualificationAcquire(t, controller, Request{
			Class: class, PrincipalID: fmt.Sprintf("holder-%d", i), GroupIDs: []string{fmt.Sprintf("holder-group-%d", i)},
			Operation: "qualification.sustained.hold", EstimatedMemoryBytes: 1,
		}))
	}

	const workerCount = 12
	ctx, cancel := context.WithTimeout(t.Context(), 350*time.Millisecond)
	defer cancel()
	start := make(chan struct{})
	failures := make(chan error, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		i := i
		go func() {
			defer workers.Done()
			<-start
			request := Request{
				Class:                config.Classes[i%len(config.Classes)],
				PrincipalID:          fmt.Sprintf("worker-%d", i),
				GroupIDs:             []string{fmt.Sprintf("worker-group-%d", i%4)},
				Operation:            "qualification.sustained.run",
				EstimatedMemoryBytes: 1,
			}
			for ctx.Err() == nil {
				lease, acquireErr := controller.Acquire(ctx, request)
				if acquireErr != nil {
					if ctx.Err() == nil {
						failures <- acquireErr
					}
					return
				}
				// Yield while admitted to keep other workers contending for the
				// same bounded slots without introducing a wall-clock sleep.
				for yield := 0; yield < 64; yield++ {
					runtime.Gosched()
				}
				lease.Release()
			}
		}()
	}
	close(start)
	if !waitForSustainedQueue(controller, workerCount, time.Second) {
		cancel()
		for _, holder := range holders {
			holder.Release()
		}
		workers.Wait()
		controller.Close()
		t.Fatal("sustained workers did not establish the expected queue")
	}
	for _, holder := range holders {
		holder.Release()
	}
	workers.Wait()

	drainContext, drainCancel := context.WithTimeout(t.Context(), time.Second)
	defer drainCancel()
	if err := controller.Drain(drainContext); err != nil {
		t.Fatalf("drain sustained workload: %v", err)
	}
	close(failures)
	for acquireErr := range failures {
		t.Fatalf("unexpected sustained Acquire error: %v", acquireErr)
	}

	snapshot := observation.snapshot()
	if len(snapshot.violations) != 0 {
		t.Fatalf("sustained workload limit violations: %v", snapshot.violations)
	}
	if snapshot.maxQueued == 0 || snapshot.admitted < workerCount*2 {
		t.Fatalf("sustained workload was too short: maxQueued=%d admitted=%d", snapshot.maxQueued, snapshot.admitted)
	}
	for _, class := range config.Classes {
		if snapshot.byClass[class] == 0 {
			t.Fatalf("class %q made no sustained progress: %+v", class, snapshot.byClass)
		}
	}
	for i := 0; i < workerCount; i++ {
		principal := fmt.Sprintf("worker-%d", i)
		if snapshot.byPrincipal[principal] == 0 {
			t.Fatalf("principal %q made no sustained progress: %+v", principal, snapshot.byPrincipal)
		}
	}
	if snapshot.terminal < snapshot.admitted {
		t.Fatalf("sustained lifecycle events = admitted %d terminal %d", snapshot.admitted, snapshot.terminal)
	}
	if stats := controller.Stats(); stats.Running != 0 || stats.Queued != 0 || stats.MemoryBytes != 0 || !stats.Closed {
		t.Fatalf("sustained workload leaked accounting: %+v", stats)
	}
}
