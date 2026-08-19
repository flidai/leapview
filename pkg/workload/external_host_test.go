package workload_test

import (
	"context"
	"sync"
	"testing"

	"github.com/flidai/leapview/pkg/workload"
)

type externalHostObserver struct {
	mu     sync.Mutex
	events []workload.AdmissionEvent
	stats  []workload.Stats
}

func (o *externalHostObserver) ObserveWorkload(stats workload.Stats) {
	o.mu.Lock()
	o.stats = append(o.stats, stats.Clone())
	o.mu.Unlock()
}

func (o *externalHostObserver) ObserveAdmission(event workload.AdmissionEvent) {
	o.mu.Lock()
	o.events = append(o.events, event.Clone())
	o.mu.Unlock()
}

func TestQualificationExternalHostCanConstructObserveAndClose(t *testing.T) {
	// These names and limits are deliberately application-specific and do not
	// depend on LeapView classes, defaults, routes, or internal packages.
	config := workload.Config{
		Classes: []workload.Class{"foreground-read", "offline-index"},
		Policies: map[workload.Class]workload.Policy{
			"foreground-read": {ReservedRunning: 1, MaximumRunning: 2, MaximumQueued: 4},
			"offline-index":   {MaximumRunning: 1, MaximumQueued: 4},
		},
		MaximumRunning: 2,
		MaximumQueued:  8,
	}
	observer := &externalHostObserver{}
	controller, err := workload.New(config, workload.WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	request := workload.Request{
		Class:                "foreground-read",
		PrincipalID:          "tenant-17",
		GroupIDs:             []string{"operators", "analytics", "operators"},
		Operation:            "report.render",
		EstimatedMemoryBytes: 4,
	}
	lease, err := controller.Acquire(context.Background(), request)
	if err != nil {
		t.Fatalf("external host Acquire: %v", err)
	}
	current, ok := workload.Current(lease.Context())
	if !ok || current.Class != request.Class || current.PrincipalID != request.PrincipalID || current.Operation != request.Operation {
		t.Fatalf("Current() = (%+v, %v), want admitted request", current, ok)
	}
	if len(current.GroupIDs) != 2 || current.GroupIDs[0] != "analytics" || current.GroupIDs[1] != "operators" {
		t.Fatalf("canonical groups = %v", current.GroupIDs)
	}

	ctx := workload.WithAdmitter(context.Background(), controller)
	admitter, ok := workload.FromContext(ctx)
	if !ok || admitter == nil {
		t.Fatal("external host context did not retain admitter")
	}
	lease.Release()
	controller.Close()
	controller.Close()
	if got := controller.Stats(); !got.Closed || got.Running != 0 || got.Queued != 0 {
		t.Fatalf("closed controller stats = %+v", got)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.stats) == 0 || len(observer.events) < 2 {
		t.Fatalf("external observer did not receive lifecycle data: stats=%d events=%d", len(observer.stats), len(observer.events))
	}
	if observer.events[0].Outcome != workload.OutcomeAdmitted || observer.events[0].Class != request.Class {
		t.Fatalf("first external event = %+v", observer.events[0])
	}
}
