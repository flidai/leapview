package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/google/uuid"
)

type retentionWorkerLease struct {
	ctx      context.Context
	released *atomic.Int32
}

func (l *retentionWorkerLease) Context() context.Context { return l.ctx }
func (l *retentionWorkerLease) QueueWait() time.Duration { return 0 }
func (l *retentionWorkerLease) Release() {
	if l.released != nil {
		l.released.Add(1)
	}
}

func TestDuckLakeRetentionWorkerRunsAndStopsWithoutSurfacingPassFailure(t *testing.T) {
	var calls atomic.Int32
	var released atomic.Int32
	worker := newDuckLakeRetentionWorker(duckLakeRetentionWorkerConfig{
		Interval: time.Hour,
		Acquire: func(context.Context) (workloadmodule.Lease, error) {
			return &retentionWorkerLease{ctx: context.Background(), released: &released}, nil
		},
		Pass: func(context.Context) error {
			calls.Add(1)
			return errors.New("maintenance failed")
		},
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("worker did not run an initial pass")
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := released.Load(); got != 1 {
		t.Fatalf("lease Release calls = %d, want 1", got)
	}
}

func TestDuckLakeRetentionWorkerSkipsWhenWorkloadSaturated(t *testing.T) {
	var calls atomic.Int32
	worker := newDuckLakeRetentionWorker(duckLakeRetentionWorkerConfig{
		Acquire: func(context.Context) (workloadmodule.Lease, error) {
			return nil, errors.New("saturated")
		},
		Pass: func(context.Context) error {
			calls.Add(1)
			return nil
		},
	})
	worker.runPass(context.Background())
	if calls.Load() != 0 {
		t.Fatal("maintenance pass ran despite workload saturation")
	}
}

func TestDuckLakeRetentionOperationIDIsDeterministicBucketedUUIDv7(t *testing.T) {
	base := time.Date(2026, 9, 2, 12, 5, 0, 0, time.UTC)
	first := duckLakeRetentionOperationID(base, time.Hour, "pool-a", "catalog-a")
	sameBucket := duckLakeRetentionOperationID(base.Add(50*time.Minute), time.Hour, "pool-a", "catalog-a")
	nextBucket := duckLakeRetentionOperationID(base.Add(time.Hour), time.Hour, "pool-a", "catalog-a")
	if first != sameBucket {
		t.Fatalf("same-bucket operation IDs differ: %q vs %q", first, sameBucket)
	}
	if first == nextBucket {
		t.Fatal("next-bucket operation ID reused the previous ID")
	}
	parsed, err := uuid.Parse(first)
	if err != nil {
		t.Fatalf("parse operation ID: %v", err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("operation UUID version = %d, want 7", parsed.Version())
	}
}

func TestDuckLakeRetentionWorkerStopCancelsBlockingPass(t *testing.T) {
	started := make(chan struct{})
	worker := newDuckLakeRetentionWorker(duckLakeRetentionWorkerConfig{
		Interval: time.Hour,
		Acquire: func(context.Context) (workloadmodule.Lease, error) {
			return &retentionWorkerLease{ctx: context.Background()}, nil
		},
		Pass: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking pass did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
