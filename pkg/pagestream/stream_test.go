package pagestream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

func TestSignalStreamPatchSendsOnePatchSignalsEventPerCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	stream := NewSignalStream(rec, req)
	if err := stream.Patch(SignalPatch{"status": "loading"}); err != nil {
		t.Fatalf("patch loading: %v", err)
	}
	if err := stream.Patch(SignalPatch{"status": "ready"}); err != nil {
		t.Fatalf("patch ready: %v", err)
	}

	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 2 || patches[0]["status"] != "loading" || patches[1]["status"] != "ready" {
		t.Fatalf("stream patches = %#v", patches)
	}
}

func TestPatchResponseSendsOnePatchSignalsEvent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/command", nil)
	rec := httptest.NewRecorder()

	if err := PatchResponse(rec, req, SignalPatch{"status": "updated"}); err != nil {
		t.Fatalf("patch response: %v", err)
	}

	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 1 || patches[0]["status"] != "updated" {
		t.Fatalf("patch response patches = %#v", patches)
	}
}

func TestRedirectSendsDatastarRedirectEvent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/command", nil)
	rec := httptest.NewRecorder()

	if err := Redirect(rec, req, "/chat/abc"); err != nil {
		t.Fatalf("redirect: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: datastar-patch-elements") || !strings.Contains(body, "window.location") || !strings.Contains(body, "/chat/abc") {
		t.Fatalf("redirect response body = %q", body)
	}
}

func TestSignalStreamForwardRelaysBrokerPatchesAndCleansUp(t *testing.T) {
	broker := NewBroker()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
	rec := newSynchronizedRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		stream := NewSignalStream(rec, req)
		if err := stream.Forward(ctx, broker, "client:page"); err != nil {
			t.Errorf("forward: %v", err)
		}
	}()

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.BodyString(), "broker") && time.Now().Before(deadline) {
		broker.Publish("client:page", SignalPatch{"status": "broker"})
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(rec.BodyString(), "broker") {
		t.Fatal("forwarder did not receive broker patch")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	patches := ssetest.PatchSignals(t, rec.BodyString())
	if len(patches) == 0 || patches[len(patches)-1]["status"] != "broker" {
		t.Fatalf("stream patches = %#v", patches)
	}
	before := rec.BodyString()
	broker.Publish("client:page", SignalPatch{"status": "after-cancel"})
	time.Sleep(10 * time.Millisecond)
	if after := rec.BodyString(); after != before {
		t.Fatalf("stream received patch after cancellation: %q", after)
	}
}

func TestSignalStreamWaitSendsKeepAlivesUntilCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
	rec := newSynchronizedRecorder()
	done := make(chan struct{})

	go func() {
		defer close(done)
		NewSignalStream(rec, req).wait(ctx, time.Millisecond)
	}()

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.BodyString(), ": keepalive\n\n") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(rec.BodyString(), ": keepalive\n\n") {
		t.Fatal("idle stream did not send an SSE keepalive comment")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle stream did not stop after cancellation")
	}
}

func TestSignalStreamForwardUpdatesKeepsQuietSubscriptionAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
	rec := newSynchronizedRecorder()
	done := make(chan error, 1)

	go func() {
		done <- NewSignalStream(rec, req).forwardUpdates(ctx, make(chan SignalPatch), time.Millisecond)
	}()

	deadline := time.Now().Add(time.Second)
	for !strings.Contains(rec.BodyString(), ": keepalive\n\n") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(rec.BodyString(), ": keepalive\n\n") {
		t.Fatal("quiet forwarded stream did not send an SSE keepalive comment")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("forward updates: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarded stream did not stop after cancellation")
	}
}

type synchronizedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func newSynchronizedRecorder() *synchronizedRecorder {
	return &synchronizedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *synchronizedRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(p)
}

func (r *synchronizedRecorder) WriteString(value string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.WriteString(value)
}

func (r *synchronizedRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(statusCode)
}

func (r *synchronizedRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *synchronizedRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func TestSignalStreamForwardRequiresBrokerAndStreamID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/updates", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	if err := NewSignalStream(rec, req).Forward(ctx, nil, "client:page"); err == nil {
		t.Fatal("Forward with nil broker returned nil error")
	}
	if err := NewSignalStream(rec, req).Forward(ctx, NewBroker(), ""); !errors.Is(err, ErrEmptyStreamID) {
		t.Fatalf("Forward with empty stream ID error = %v, want ErrEmptyStreamID", err)
	}
}
