package protocol

import (
	"context"
	"testing"

	apiidempotency "github.com/flidai/leapview/internal/platform/http/idempotency"
)

func TestLeaseLossPublishedBeforeHandlerCancellation(t *testing.T) {
	p := &Protocol{}
	leaseLost := make(chan error, 1)
	handlerCtx, cancelHandler := context.WithCancelCause(t.Context())
	defer cancelHandler(nil)
	cancelled := false
	p.notifyIdempotencyLeaseLost(apiidempotency.ErrLeaseLost, leaseLost, func(cause error) {
		cancelHandler(cause)
		cancelled = true
		// The handler can return as soon as cancellation wakes it. Its caller
		// must already see lease loss rather than commit the captured response.
		select {
		case err := <-leaseLost:
			if err != apiidempotency.ErrLeaseLost {
				t.Fatalf("lease loss = %v", err)
			}
		default:
			t.Fatal("handler cancellation preceded lease-loss publication")
		}
		if p.LeaseRenewalError() == nil {
			t.Fatal("handler cancellation preceded unhealthy readiness")
		}
	})
	if !cancelled || context.Cause(handlerCtx) != apiidempotency.ErrLeaseLost {
		t.Fatalf("handler cancelled=%t cause=%v", cancelled, context.Cause(handlerCtx))
	}
}
