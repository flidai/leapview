package workload

import (
	"context"
	"math/rand"
	"testing"
)

type qualificationModelLease struct {
	lease   Lease
	request Request
}

func TestQualificationBoundedModelInvariants(t *testing.T) {
	config := Config{
		Classes: []Class{"red", "blue", "green"},
		Policies: map[Class]Policy{
			"red":   {ReservedRunning: 1, MaximumRunning: 2, MaximumQueued: 16, MaximumMemoryBytes: 16},
			"blue":  {MaximumRunning: 2, MaximumQueued: 16, MaximumMemoryBytes: 16},
			"green": {MaximumRunning: 1, MaximumQueued: 16, MaximumMemoryBytes: 16},
		},
		MaximumRunning:                 3,
		MaximumQueued:                  16,
		MaximumMemoryBytes:             24,
		MaximumRunningPerPrincipal:     2,
		MaximumQueuedPerPrincipal:      8,
		MaximumMemoryBytesPerPrincipal: 16,
		MaximumRunningPerGroup:         2,
		MaximumQueuedPerGroup:          8,
		MaximumMemoryBytesPerGroup:     16,
	}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(0xAD1301))
	active := make([]qualificationModelLease, 0, config.MaximumRunning)
	for step := 0; step < 300; step++ {
		if len(active) > 0 && rng.Intn(3) == 0 {
			index := rng.Intn(len(active))
			active[index].lease.Release()
			active = append(active[:index], active[index+1:]...)
		} else {
			class := config.Classes[rng.Intn(len(config.Classes))]
			principal := []string{"p0", "p1", "p2", "p3"}[rng.Intn(4)]
			request := Request{Class: class, PrincipalID: principal, Operation: "model.step", EstimatedMemoryBytes: int64(1 + rng.Intn(4))}
			if rng.Intn(2) == 0 {
				request.GroupIDs = []string{"g" + string(rune('0'+rng.Intn(3)))}
			}

			// Keep this bounded model deterministic: eligible requests should grant
			// immediately; ineligible requests use a pre-canceled context and must
			// not leave a queue entry behind.
			stats := controller.Stats()
			eligible := stats.Running < config.MaximumRunning && stats.Classes[class].Running < config.Policies[class].MaximumRunning
			classStats := stats.Classes[class]
			if config.MaximumMemoryBytes > 0 && stats.MemoryBytes+request.EstimatedMemoryBytes > config.MaximumMemoryBytes {
				eligible = false
			}
			if limit := config.Policies[class].MaximumMemoryBytes; limit > 0 && classStats.MemoryBytes+request.EstimatedMemoryBytes > limit {
				eligible = false
			}
			principalStats := stats.Principals[principal]
			if config.MaximumRunningPerPrincipal > 0 && principalStats.Running >= config.MaximumRunningPerPrincipal {
				eligible = false
			}
			if config.MaximumMemoryBytesPerPrincipal > 0 && principalStats.MemoryBytes+request.EstimatedMemoryBytes > config.MaximumMemoryBytesPerPrincipal {
				eligible = false
			}
			if len(request.GroupIDs) > 0 {
				groupStats := stats.Groups[request.GroupIDs[0]]
				if config.MaximumRunningPerGroup > 0 && groupStats.Running >= config.MaximumRunningPerGroup {
					eligible = false
				}
				if config.MaximumMemoryBytesPerGroup > 0 && groupStats.MemoryBytes+request.EstimatedMemoryBytes > config.MaximumMemoryBytesPerGroup {
					eligible = false
				}
			}
			if !eligible {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if lease, err := controller.Acquire(ctx, request); lease != nil || err == nil {
					t.Fatalf("ineligible pre-canceled request granted at step %d: lease=%v err=%v", step, lease, err)
				}
			} else {
				lease, err := controller.Acquire(context.Background(), request)
				if err != nil {
					t.Fatalf("eligible request rejected at step %d: %+v: %v", step, request, err)
				}
				active = append(active, qualificationModelLease{lease: lease, request: request})
			}
		}
		qualificationAssertInvariants(t, controller, config)
	}
	for _, admitted := range active {
		admitted.lease.Release()
	}
	qualificationAssertInvariants(t, controller, config)
	if got := controller.Stats(); got.Running != 0 || got.MemoryBytes != 0 || got.Queued != 0 {
		t.Fatalf("final model state = %+v", got)
	}
}

func qualificationAssertInvariants(t *testing.T, controller *Controller, config Config) {
	t.Helper()
	stats := controller.Stats()
	if stats.Running < 0 || stats.Queued < 0 || stats.MemoryBytes < 0 {
		t.Fatalf("negative instance counters: %+v", stats)
	}
	if stats.Running > config.MaximumRunning || stats.Queued > config.MaximumQueued || stats.MemoryBytes > config.MaximumMemoryBytes {
		t.Fatalf("instance limits exceeded: %+v", stats)
	}
	classRunning, classQueued, classMemory := 0, 0, int64(0)
	for _, class := range config.Classes {
		value, ok := stats.Classes[class]
		if !ok {
			t.Fatalf("missing class %q in stats", class)
		}
		if value.Running < 0 || value.Queued < 0 || value.MemoryBytes < 0 || value.Borrowed < 0 {
			t.Fatalf("negative %q counters: %+v", class, value)
		}
		if value.Running > value.Policy.MaximumRunning || value.Queued > value.Policy.MaximumQueued || value.MemoryBytes > value.Policy.MaximumMemoryBytes {
			t.Fatalf("class %q limits exceeded: %+v", class, value)
		}
		classRunning += value.Running
		classQueued += value.Queued
		classMemory += value.MemoryBytes
	}
	if classRunning != stats.Running || classQueued != stats.Queued || classMemory != stats.MemoryBytes {
		t.Fatalf("aggregate mismatch: instance=(%d,%d,%d), classes=(%d,%d,%d)", stats.Running, stats.Queued, stats.MemoryBytes, classRunning, classQueued, classMemory)
	}
	for principal, value := range stats.Principals {
		if value.Running < 0 || value.Queued < 0 || value.MemoryBytes < 0 || value.Running > config.MaximumRunningPerPrincipal || value.Queued > config.MaximumQueuedPerPrincipal || value.MemoryBytes > config.MaximumMemoryBytesPerPrincipal {
			t.Fatalf("principal %q limits exceeded: %+v", principal, value)
		}
	}
	for group, value := range stats.Groups {
		if value.Running < 0 || value.Queued < 0 || value.MemoryBytes < 0 || value.Running > config.MaximumRunningPerGroup || value.Queued > config.MaximumQueuedPerGroup || value.MemoryBytes > config.MaximumMemoryBytesPerGroup {
			t.Fatalf("group %q limits exceeded: %+v", group, value)
		}
	}
}
