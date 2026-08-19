package workload

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControllerDrainWaitsForFinalLeaseRelease(t *testing.T) {
	controller, err := New(qualificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	lease := qualificationAcquire(t, controller, qualificationRequest("interactive", "drain"))
	if err := lease.Context().Err(); err != nil {
		t.Fatalf("lease context unexpectedly finished before close: %v", err)
	}

	drained := make(chan error, 1)
	go func() { drained <- controller.Drain(context.Background()) }()

	select {
	case err := <-drained:
		t.Fatalf("Drain returned before lease release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the admitted lease context")
	}
	lease.Release()

	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("Drain() = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Drain did not finish after final lease release")
	}
	if got := controller.Stats(); got.Running != 0 || got.Queued != 0 || !got.Closed {
		t.Fatalf("post-drain stats = %+v", got)
	}
}

func TestControllerDrainHonorsContextAndCanRetry(t *testing.T) {
	controller, err := New(qualificationConfig())
	if err != nil {
		t.Fatal(err)
	}
	lease := qualificationAcquire(t, controller, qualificationRequest("interactive", "drain-timeout"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = controller.Drain(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain() = %v, want deadline exceeded", err)
	}
	if got := controller.Stats(); got.Running != 1 || !got.Closed {
		t.Fatalf("timed-out drain stats = %+v", got)
	}

	lease.Release()
	if err := controller.Drain(context.Background()); err != nil {
		t.Fatalf("retry Drain() = %v, want nil", err)
	}
}
