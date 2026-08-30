package resultcache

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestExecutionScopeCoalescesWithinOneGeneration(t *testing.T) {
	scope := NewExecutionScope()
	defer scope.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	execute := func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "value", nil
	}
	results := make(chan any, 2)
	errs := make(chan error, 2)
	go func() {
		value, _, err := scope.Coalesce(context.Background(), "query", execute)
		results <- value
		errs <- err
	}()
	<-started
	go func() {
		value, _, err := scope.Coalesce(context.Background(), "query", execute)
		results <- value
		errs <- err
	}()
	waitForExecutionFlightWaiters(t, scope, "query", 2)
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if value := <-results; value != "value" {
			t.Fatalf("coalesced value = %#v", value)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("executions = %d, want 1", got)
	}
}

func TestExecutionScopesDoNotCoalesceAcrossGenerations(t *testing.T) {
	first, second := NewExecutionScope(), NewExecutionScope()
	defer first.Close()
	defer second.Close()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	execute := func(context.Context) (any, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return "value", nil
	}
	var wait sync.WaitGroup
	for _, scope := range []*ExecutionScope{first, second} {
		wait.Add(1)
		go func(scope *ExecutionScope) {
			defer wait.Done()
			if _, shared, err := scope.Coalesce(context.Background(), "query", execute); err != nil || shared {
				t.Errorf("generation execution shared=%v err=%v", shared, err)
			}
		}(scope)
	}
	<-started
	<-started
	close(release)
	wait.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("executions = %d, want 2", got)
	}
}

func TestExecutionScopeCloseCancelsAndDrainsOwnerFlights(t *testing.T) {
	scope := NewExecutionScope()
	started := make(chan struct{})
	exited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, err := scope.Coalesce(context.Background(), "query", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			close(exited)
			return nil, ctx.Err()
		})
		done <- err
	}()
	<-started
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("execution scope close returned before owner flight exited")
	}
	if err := <-done; !errors.Is(err, ErrExecutionScopeClosed) {
		t.Fatalf("flight error = %v, want closed execution scope", err)
	}
}

func TestExecutionScopeRejectsJoinsAfterClose(t *testing.T) {
	scope := NewExecutionScope()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, _, err := scope.Coalesce(context.Background(), "query", func(context.Context) (any, error) {
		called = true
		return nil, nil
	}); !errors.Is(err, ErrExecutionScopeClosed) {
		t.Fatalf("join error = %v, want closed execution scope", err)
	}
	if called {
		t.Fatal("closed execution scope invoked owner work")
	}
	if _, _, err := scope.CoalesceArrow(context.Background(), "arrow-query", func(context.Context) (ArrowFlightValue, error) {
		called = true
		return ArrowFlightValue{}, nil
	}); !errors.Is(err, ErrExecutionScopeClosed) {
		t.Fatalf("Arrow join error = %v, want closed execution scope", err)
	}
	if called {
		t.Fatal("closed execution scope invoked Arrow owner work")
	}
}

func TestExecutionScopeCloseDrainsArrowFlightHolds(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer allocator.AssertSize(t, 0)
	scope := NewExecutionScope()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		lease, _, err := scope.CoalesceArrow(context.Background(), "query", func(ctx context.Context) (ArrowFlightValue, error) {
			close(started)
			<-ctx.Done()
			result := testArrowResult(t, allocator, "value")
			base, acquireErr := result.Acquire()
			result.Release()
			return ArrowFlightValue{Data: base}, acquireErr
		})
		if lease != nil {
			lease.Release()
		}
		done <- err
	}()
	<-started
	requireNoExecutionScopeCloseError(t, scope.Close())
	if err := <-done; !errors.Is(err, ErrExecutionScopeClosed) {
		t.Fatalf("Arrow flight error = %v, want closed execution scope", err)
	}
}

func TestExecutionScopeConcurrentJoinCancelAndClose(t *testing.T) {
	scope := NewExecutionScope()
	started := make(chan struct{})
	var once sync.Once
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				once.Do(func() { close(started) })
				cancel()
			}()
			_, _, _ = scope.Coalesce(ctx, "query", func(ctx context.Context) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})
		}()
	}
	<-started
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
}

func waitForExecutionFlightWaiters(t *testing.T, scope *ExecutionScope, key string, want int) {
	t.Helper()
	for {
		scope.mu.Lock()
		flight := scope.flights[key]
		ready := flight != nil && flight.waiters >= want
		scope.mu.Unlock()
		if ready {
			return
		}
		runtime.Gosched()
	}
}

func requireNoExecutionScopeCloseError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
