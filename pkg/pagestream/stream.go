package pagestream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

var errMissingForwardBroker = errors.New("pagestream: broker is required")

// keepAliveInterval stays below both LeapView's stream idle deadline and the
// common one-minute idle timeout used by HTTP proxies. SSE comments are
// deliberately invisible to Datastar while still proving the connection is
// live end to end.
const keepAliveInterval = 25 * time.Second

// SignalStream is one long-lived Datastar SSE response that emits signal
// patches.
type SignalStream struct {
	sse *datastar.ServerSentEventGenerator
	w   http.ResponseWriter
	mu  *sync.Mutex
}

// NewSignalStream opens a Datastar SSE signal stream for the request.
func NewSignalStream(w http.ResponseWriter, r *http.Request) SignalStream {
	return SignalStream{sse: datastar.NewSSE(w, r), w: w, mu: &sync.Mutex{}}
}

// Redirect emits a Datastar redirect response for short-lived command handlers.
// Long-lived update streams should use SignalStream and Patch only.
func Redirect(w http.ResponseWriter, r *http.Request, location string) error {
	return datastar.NewSSE(w, r).Redirect(location)
}

// PatchResponse emits a single Datastar patch-signals response.
func PatchResponse(w http.ResponseWriter, r *http.Request, patch SignalPatch) error {
	return NewSignalStream(w, r).Patch(patch)
}

// Patch emits one Datastar patch-signals event. Empty patches are ignored.
func (s SignalStream) Patch(patch SignalPatch) error {
	return s.writeForwarded(patch)
}

func (s SignalStream) writeForwarded(patch SignalPatch) error {
	if len(patch) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sse.MarshalAndPatchSignals(patch)
}

func (s SignalStream) keepAlive() error {
	if s.sse.IsClosed() {
		return s.sse.Context().Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
		return fmt.Errorf("write SSE keepalive: %w", err)
	}
	if err := http.NewResponseController(s.w).Flush(); err != nil {
		return fmt.Errorf("flush SSE keepalive: %w", err)
	}
	return nil
}

// Wait keeps an otherwise idle stream alive until ctx is canceled.
func (s SignalStream) Wait(ctx context.Context) {
	s.wait(ctx, keepAliveInterval)
}

func (s SignalStream) wait(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.keepAlive(); err != nil {
				return
			}
		}
	}
}

// Forward relays signal patches published to streamID until ctx is canceled.
func (s SignalStream) Forward(ctx context.Context, broker *Broker, streamID string) error {
	if broker == nil {
		return errMissingForwardBroker
	}
	updates, unsubscribe, err := broker.Subscribe(streamID)
	if err != nil {
		return err
	}
	defer unsubscribe()
	return s.ForwardUpdates(ctx, updates)
}

// ForwardUpdates relays an already-subscribed mailbox. It lets callers
// subscribe before sending bootstrap state so no refresh event can be lost in
// the bootstrap-to-forward handoff.
func (s SignalStream) ForwardUpdates(ctx context.Context, updates <-chan SignalPatch) error {
	return s.forwardUpdates(ctx, updates, keepAliveInterval)
}

func (s SignalStream) forwardUpdates(ctx context.Context, updates <-chan SignalPatch, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.keepAlive(); err != nil {
				return err
			}
		case patch, ok := <-updates:
			if !ok {
				return nil
			}
			if err := s.writeForwarded(patch); err != nil {
				return err
			}
		}
	}
}
