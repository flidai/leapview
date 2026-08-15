package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/workload"
)

func TestStorageRetentionSkipsWhenMaintenanceCapacityIsUnavailable(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1},
		workload.Maintenance: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	held, err := controller.Acquire(context.Background(), workload.Request{
		Class: workload.Interactive, PrincipalID: "test:holder", Operation: "hold", EstimatedMemoryBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	repo := &retentionProbe{}
	retention := NewRetention(RetentionConfig{
		States: repo, Admission: controller,
		CatalogPath: "unused", DataPath: "unused", Environment: "test",
	})
	if err := retention.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if repo.called {
		t.Fatal("storage retention ran while maintenance capacity was unavailable")
	}
	if stats := controller.Stats(); stats.Queued != 0 {
		t.Fatalf("maintenance queued instead of skipping: %#v", stats)
	}
}

type retentionProbe struct {
	called bool
}

func (r *retentionProbe) ReconcileRetention(context.Context, string, time.Time) error {
	r.called = true
	return nil
}

func (r *retentionProbe) ReferencedDuckLakeSnapshots(context.Context, string) ([]int64, error) {
	r.called = true
	return nil, nil
}
