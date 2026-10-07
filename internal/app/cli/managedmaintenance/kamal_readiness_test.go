package managedmaintenance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishedReadinessWaitsForRestartedProxy(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("unexpected readiness path %s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			http.Error(w, "proxy is restoring its routes", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := waitPublishedReadiness(ctx, server.Client(), server.URL+"/readyz"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatal("readiness did not wait for proxy recovery")
	}
}

func TestPublishedReadinessRemainsBoundedWhenProxyNeverReady(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitPublishedReadiness(ctx, server.Client(), server.URL+"/readyz"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness returned %v", err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := waitPublishedReadiness(ctx, server.Client(), server.URL+"/readyz"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unready proxy returned %v", err)
	}
}
