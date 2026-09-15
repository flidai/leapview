package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIntervalWorkerRunsImmediatelyAndCanRestart(t *testing.T) {
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	worker := NewIntervalWorker(time.Hour, func(context.Context) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
	})
	if err := worker.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not run its initial task")
	}
	if err := worker.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after duplicate start = %d, want 1", got)
	}

	started = make(chan struct{}, 1)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("restarted worker did not run its initial task")
	}
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIntervalWorkerStopIsBoundedAndCanFinishDraining(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	worker := NewIntervalWorker(time.Hour, func(context.Context) {
		close(entered)
		<-release
	})
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker task did not start")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := worker.Stop(stopCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIntervalWorkerConcurrentStartStopIsSafe(t *testing.T) {
	worker := NewIntervalWorker(time.Hour, func(context.Context) {})
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() { _ = worker.Start(context.Background()) })
		group.Go(func() { _ = worker.Stop(context.Background()) })
	}
	group.Wait()
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
