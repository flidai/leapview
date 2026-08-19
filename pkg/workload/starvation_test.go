package workload

import (
	"context"
	"testing"
	"time"
)

func TestQualificationEveryEligibleClassMakesProgress(t *testing.T) {
	config := Config{
		Classes: []Class{"reserved", "background"},
		Policies: map[Class]Policy{
			"reserved":   {ReservedRunning: 1, MaximumRunning: 1, MaximumQueued: 32},
			"background": {MaximumRunning: 1, MaximumQueued: 32},
		},
		MaximumRunning: 1,
		MaximumQueued:  32,
	}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	held := qualificationAcquire(t, controller, qualificationRequest("background", "holder"))
	type result struct {
		class Class
		lease Lease
		err   error
	}
	results := make(chan result, 12)
	for i := 0; i < 6; i++ {
		for _, class := range []Class{"background", "reserved"} {
			class := class
			go func() {
				lease, err := controller.Acquire(context.Background(), qualificationRequest(class, string(class)+"-actor"))
				results <- result{class: class, lease: lease, err: err}
			}()
		}
	}
	qualificationWaitQueued(t, controller, 12)
	held.Release()
	counts := map[Class]int{}
	for i := 0; i < 12; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("%s waiter: %v", got.class, got.err)
			}
			counts[got.class]++
			got.lease.Release()
		case <-time.After(time.Second):
			t.Fatal("eligible class stopped making progress")
		}
	}
	if counts["reserved"] != 6 || counts["background"] != 6 {
		t.Fatalf("class progress = %v, want six grants per class", counts)
	}
}

func TestQualificationNoisyActorCannotStarveQuietActor(t *testing.T) {
	config := Config{
		Classes:        []Class{"shared"},
		Policies:       map[Class]Policy{"shared": {MaximumRunning: 1, MaximumQueued: 32}},
		MaximumRunning: 1,
		MaximumQueued:  32,
	}
	controller, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	held := qualificationAcquire(t, controller, qualificationRequest("shared", "holder"))
	type result struct {
		actor string
		lease Lease
		err   error
	}
	results := make(chan result, 16)
	for i := 0; i < 8; i++ {
		go func() {
			lease, err := controller.Acquire(context.Background(), qualificationRequest("shared", "noisy"))
			results <- result{actor: "noisy", lease: lease, err: err}
		}()
	}
	qualificationWaitQueued(t, controller, 8)
	for i := 0; i < 8; i++ {
		go func() {
			lease, err := controller.Acquire(context.Background(), qualificationRequest("shared", "quiet"))
			results <- result{actor: "quiet", lease: lease, err: err}
		}()
	}
	qualificationWaitQueued(t, controller, 16)
	held.Release()
	noisyBeforeQuiet := 0
	quietSeen := false
	quietCount := 0
	for i := 0; i < 16; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("%s waiter: %v", got.actor, got.err)
			}
			if got.actor == "quiet" {
				quietSeen = true
				quietCount++
			} else if !quietSeen {
				noisyBeforeQuiet++
			}
			got.lease.Release()
		case <-time.After(time.Second):
			t.Fatal("quiet actor stopped making progress")
		}
	}
	if noisyBeforeQuiet > 1 || quietCount != 8 {
		t.Fatalf("actor starvation: noisyBeforeQuiet=%d quietCount=%d", noisyBeforeQuiet, quietCount)
	}
}
