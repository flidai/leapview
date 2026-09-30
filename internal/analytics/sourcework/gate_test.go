package sourcework

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestPauseWaitsForActualWorkAndRetainedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate Gate
		ctx, cancel := context.WithCancel(context.Background())
		lease, err := gate.Acquire(ctx)
		mustSucceed(t, err)
		pause, err := gate.Pause()
		mustSucceed(t, err)
		cancel() // Canceling the request is not evidence its work has stopped.
		cleanup, err := lease.Retain()
		mustSucceed(t, err)
		lease.Release()
		lease.Release()
		if _, err := lease.Retain(); !errors.Is(err, ErrReleasedLease) {
			t.Fatalf("retaining released lease: %v", err)
		}
		if err := pause.Resume(); !errors.Is(err, ErrWorkActive) {
			t.Fatalf("resume with cleanup active: %v", err)
		}
		waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := pause.WaitDrained(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("drain before cleanup completes: %v", err)
		}
		admitted := make(chan *Lease, 1)
		go func() {
			lease, err := gate.Acquire(context.Background())
			if err != nil {
				t.Error(err)
			}
			admitted <- lease
		}()
		synctest.Wait()
		select {
		case <-admitted:
			t.Fatal("timed-out drain reopened admission")
		default:
		}
		cleanup.Release()
		mustSucceed(t, pause.WaitDrained(context.Background()))
		mustSucceed(t, pause.Resume())
		(<-admitted).Release()
	})
}

func TestPausedWaiterCancellationAndStalePause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate Gate
		first, err := gate.Pause()
		mustSucceed(t, err)
		if _, err := gate.Pause(); !errors.Is(err, ErrPaused) {
			t.Fatalf("second pause took ownership: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			lease, err := gate.Acquire(ctx)
			if lease != nil {
				lease.Release()
			}
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter: %v", err)
		}
		mustSucceed(t, first.WaitDrained(context.Background()))
		mustSucceed(t, first.Resume())
		second, err := gate.Pause()
		mustSucceed(t, err)
		if err := first.Resume(); !errors.Is(err, ErrInvalidPause) {
			t.Fatalf("stale handle resumed new pause: %v", err)
		}
		if err := first.WaitDrained(context.Background()); !errors.Is(err, ErrInvalidPause) {
			t.Fatalf("stale handle claimed drain: %v", err)
		}
		mustSucceed(t, second.Resume())
		if lease, err := gate.Acquire(ctx); lease != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled context admitted after resume: lease=%v err=%v", lease, err)
		}
	})
}

func TestCloseFencesWaitersWithoutAbandoningAdmittedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate Gate
		lease, err := gate.Acquire(context.Background())
		mustSucceed(t, err)
		pause, err := gate.Pause()
		mustSucceed(t, err)
		acquireDone, drainDone := make(chan error, 1), make(chan error, 1)
		go func() { _, err := gate.Acquire(context.Background()); acquireDone <- err }()
		go func() { drainDone <- pause.WaitDrained(context.Background()) }()
		synctest.Wait()
		gate.Close()
		gate.Close()
		for _, err := range []error{<-acquireDone, <-drainDone, pause.Resume()} {
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("close failed to fence control or waiter: %v", err)
			}
		}
		cleanup, err := lease.Retain()
		mustSucceed(t, err)
		lease.Release()
		cleanup.Release()
		if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed admission reopened: %v", err)
		}
		if _, err := gate.Pause(); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed gate paused: %v", err)
		}
	})
}

func mustSucceed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
