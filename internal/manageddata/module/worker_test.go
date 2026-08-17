package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/workload"
)

type testUploadExpirer struct{ called chan struct{} }

func (e testUploadExpirer) ExpireUploads(context.Context) (control.ExpireResult, error) {
	select {
	case e.called <- struct{}{}:
	default:
	}
	return control.ExpireResult{Expired: 1}, nil
}

func TestMaintenanceWorkerUsesModuleLifecycle(t *testing.T) {
	called := make(chan struct{}, 1)
	worker := newMaintenanceWorker(testUploadExpirer{called: called}, MaintenanceWorkerConfig{
		Interval: time.Millisecond,
		Acquire: func(ctx context.Context) (MaintenanceLease, error) {
			return testMaintenanceLease{ctx: ctx}, nil
		},
	})
	worker.Start(t.Context())
	worker.Start(t.Context())
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("managed-data maintenance did not run")
	}
	if err := worker.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceWorkerSkipsSaturatedPassWithoutQueueing(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1}, workload.Maintenance: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	held, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Interactive, PrincipalID: "principal:test", Operation: "hold", EstimatedMemoryBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	worker := newMaintenanceWorker(testUploadExpirer{called: called}, MaintenanceWorkerConfig{
		Interval: 10 * time.Millisecond,
		Acquire: func(ctx context.Context) (MaintenanceLease, error) {
			return controller.Acquire(ctx, workload.Request{Class: workload.Maintenance, PrincipalID: "system:managed-data", Operation: "managed_data.collect", EstimatedMemoryBytes: 64 << 20})
		},
	})
	worker.Start(t.Context())
	select {
	case <-called:
		t.Fatal("maintenance ran while node capacity was saturated")
	case <-time.After(40 * time.Millisecond):
	}
	if stats := controller.Stats(); stats.Queued != 0 {
		t.Fatalf("maintenance queued: %#v", stats)
	}
	held.Release()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not retry")
	}
	if err := worker.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type testMaintenanceLease struct{ ctx context.Context }

func (l testMaintenanceLease) Context() context.Context { return l.ctx }
func (testMaintenanceLease) Release()                   {}
