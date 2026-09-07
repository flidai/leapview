package app

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type runtimeHostLifecycleOwnerFake struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *runtimeHostLifecycleOwnerFake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.err
}

func (f *runtimeHostLifecycleOwnerFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRuntimeHostLifecycleStartIsNoOp(t *testing.T) {
	owner := &runtimeHostLifecycleOwnerFake{}
	lifecycle := newRuntimeHostLifecycle(owner)
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := owner.callCount(); got != 0 {
		t.Fatalf("owner Close calls after Start = %d, want 0", got)
	}
}

func TestRuntimeHostLifecycleStopIsIdempotent(t *testing.T) {
	wantErr := errors.New("runtimehost close failed")
	owner := &runtimeHostLifecycleOwnerFake{err: wantErr}
	lifecycle := newRuntimeHostLifecycle(owner)

	if err := lifecycle.Stop(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("first Stop() error = %v, want %v", err, wantErr)
	}
	if err := lifecycle.Stop(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("second Stop() error = %v, want %v", err, wantErr)
	}
	if got := owner.callCount(); got != 1 {
		t.Fatalf("owner Close calls = %d, want exactly 1", got)
	}
}

func TestRuntimeHostLifecycleStopConcurrentIsIdempotent(t *testing.T) {
	owner := &runtimeHostLifecycleOwnerFake{}
	lifecycle := newRuntimeHostLifecycle(owner)
	const callers = 16
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lifecycle.Stop(context.Background())
		}()
	}
	wg.Wait()
	if got := owner.callCount(); got != 1 {
		t.Fatalf("owner Close calls = %d, want exactly 1", got)
	}
}

func TestRuntimeHostLifecycleNilOrUnconfiguredIsNoOp(t *testing.T) {
	var nilLifecycle *runtimeHostLifecycle
	if err := nilLifecycle.Start(t.Context()); err != nil {
		t.Fatalf("nil lifecycle Start() error = %v", err)
	}
	if err := nilLifecycle.Stop(t.Context()); err != nil {
		t.Fatalf("nil lifecycle Stop() error = %v", err)
	}
	lifecycle := newRuntimeHostLifecycle(nil)
	if err := lifecycle.Start(t.Context()); err != nil {
		t.Fatalf("unconfigured lifecycle Start() error = %v", err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatalf("unconfigured lifecycle Stop() error = %v", err)
	}
}
