package resultcache

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

func TestPrewarmOwnerCancellationRetriesForegroundArrowWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		defer allocator.AssertSize(t, 0)
		scope := NewExecutionScope()
		defer scope.Close()
		warmCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		warmDone := make(chan error, 1)
		foregroundDone := make(chan error, 1)
		warmCalls, foregroundCalls := 0, 0
		data := testArrowResult(t, allocator, "foreground")
		defer data.Release()
		go func() {
			lease, _, err := scope.CoalesceArrow(warmCtx, "same-key", func(ctx context.Context) (ArrowFlightValue, error) {
				warmCalls++
				<-ctx.Done()
				return ArrowFlightValue{}, ctx.Err()
			})
			if lease != nil {
				lease.Release()
			}
			warmDone <- err
		}()
		synctest.Wait()
		if warmCalls != 1 {
			t.Fatal("warm owner did not enter execution")
		}
		go func() {
			lease, _, err := scope.CoalesceArrow(context.Background(), "same-key", func(context.Context) (ArrowFlightValue, error) {
				foregroundCalls++
				hold, err := data.Acquire()
				return ArrowFlightValue{Data: hold}, err
			})
			if lease != nil {
				lease.Release()
			}
			foregroundDone <- err
		}()
		synctest.Wait()
		scope.mu.Lock()
		flight := scope.arrowFlights["same-key"]
		joined := flight != nil && flight.waiters == 2
		scope.mu.Unlock()
		if !joined || foregroundCalls != 0 {
			t.Fatal("foreground did not join warmup's existing flight")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-warmDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("warm result = %v, want canceled", err)
			}
		default:
			t.Fatal("canceled warm caller did not drain")
		}
		select {
		case err := <-foregroundDone:
			if err != nil || foregroundCalls != 1 {
				t.Fatalf("foreground result = %v, replacement executions=%d, want success and one replacement", err, foregroundCalls)
			}
		default:
			t.Fatal("foreground waiter did not retry/drain after warm owner cancellation")
		}
	})
}
