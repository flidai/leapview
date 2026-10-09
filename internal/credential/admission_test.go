package credential

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProviderAdmissionStartsClosedAndReopensAfterConfirmedDrain(t *testing.T) {
	gate := NewProviderAdmission()
	if _, _, err := gate.Acquire(t.Context()); !errors.Is(err, ErrProviderPaused) {
		t.Fatalf("startup admission = %v, want paused", err)
	}
	if err := gate.Resume(); err != nil {
		t.Fatal(err)
	}
	work, release, err := gate.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Pause(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unconfirmed drain = %v", err)
	}
	if !errors.Is(work.Err(), context.Canceled) {
		t.Fatal("pause did not cancel admitted provider work")
	}
	if err := gate.Resume(); !errors.Is(err, ErrProviderBusy) {
		t.Fatalf("reopen before provider release = %v", err)
	}
	if _, _, err := gate.Acquire(t.Context()); !errors.Is(err, ErrProviderPaused) {
		t.Fatalf("timed-out drain reopened admission: %v", err)
	}
	release()
	release()
	if err := gate.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := gate.Resume(); err != nil {
		t.Fatal(err)
	}
	next, finish, err := gate.Acquire(t.Context())
	if err != nil || next.Err() != nil {
		t.Fatalf("reopened work = %v, %v", next, err)
	}
	finish()
}

func TestProviderAdmissionWaitsWithoutTurningPauseIntoJobFailure(t *testing.T) {
	gate := NewProviderAdmission()
	entered := make(chan error, 1)
	go func() {
		_, release, err := gate.Wait(t.Context())
		if release != nil {
			defer release()
		}
		entered <- err
	}()
	select {
	case err := <-entered:
		t.Fatalf("closed provider admitted waiting work: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := gate.Resume(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-entered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider work did not resume")
	}
}

func TestProviderAdmissionCanceledWaitNeverAdmitsWork(t *testing.T) {
	gate := NewProviderAdmission()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v", err)
	}
	if err := gate.Pause(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := gate.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := gate.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition = %v", err)
	}
}
