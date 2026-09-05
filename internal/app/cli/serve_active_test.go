package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProductionHTTPServerHasSSESafeTimeouts(t *testing.T) {
	server := productionHTTPServer(":0", http.NewServeMux())
	if server.ReadHeaderTimeout <= 0 || server.ReadTimeout <= 0 || server.IdleTimeout <= 0 {
		t.Fatalf("server timeouts = header %s read %s idle %s, all must be configured", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want zero for long-lived SSE", server.WriteTimeout)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("empty server shutdown: %v", err)
	}
}

func TestResponseLivenessKeepsSSEAliveAndCancelsOnDisconnectOrIdle(t *testing.T) {
	t.Run("keeps healthy stream alive", func(t *testing.T) {
		server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: one\n\n"))
			flusher.Flush()
			time.Sleep(75 * time.Millisecond)
			_, _ = w.Write([]byte("data: two\n\n"))
			flusher.Flush()
		}), 20*time.Millisecond, time.Second))
		defer server.Close()
		response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
		if err != nil {
			t.Fatalf("GET stream: %v", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if got, want := string(body), "data: one\n\ndata: two\n\n"; got != want {
			t.Fatalf("stream body = %q, want %q", got, want)
		}
	})

	t.Run("disconnect cancels handler", func(t *testing.T) {
		canceled := make(chan struct{})
		server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: one\n\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(canceled)
		}), 5*time.Second, 100*time.Millisecond))
		defer server.Close()
		response, err := http.Get(server.URL)
		if err != nil {
			t.Fatalf("GET stream: %v", err)
		}
		_, _ = io.ReadAll(io.LimitReader(response.Body, int64(len("data: one\n\n"))))
		_ = response.Body.Close()
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("disconnected stream handler was not canceled")
		}
	})

	t.Run("idle stream is canceled", func(t *testing.T) {
		canceled := make(chan struct{})
		server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: one\n\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			close(canceled)
		}), time.Second, 20*time.Millisecond))
		defer server.Close()
		response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
		if err != nil {
			t.Fatalf("GET stream: %v", err)
		}
		defer response.Body.Close()
		select {
		case <-canceled:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("idle stream context was not canceled")
		}
	})
}
