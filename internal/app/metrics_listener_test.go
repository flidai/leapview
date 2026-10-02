package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/platform/observability"
)

func TestPublicMetricsUnavailableEvenWithValidBearer(t *testing.T) {
	token := strings.Repeat("m", 32)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{MetricsBearerToken: token}))
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	server.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("public metrics=%d, want 404", w.Code)
	}
}

func TestMetricsListenerAuthenticatesAndShutsDown(t *testing.T) {
	token := strings.Repeat("m", 32)
	listener := newMetricsListener("127.0.0.1:0", observability.New().MetricsHandler(token, accessmodule.BearerToken))
	if err := listener.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Stop(context.Background()) })
	client := &http.Client{Timeout: 2 * time.Second}
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/metrics", "", http.StatusUnauthorized}, {"/metrics", "wrong", http.StatusUnauthorized},
		{"/metrics", token, http.StatusOK}, {"/healthz", token, http.StatusNotFound},
	} {
		req, err := http.NewRequest(http.MethodGet, "http://"+listener.listener.Addr().String()+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatalf("%s=%d, want %d", tc.path, response.StatusCode, tc.status)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := listener.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-listener.Fatal():
		t.Fatalf("normal shutdown reported fatal: %v", err)
	default:
	}
}

func TestMetricsListenerBindFailureDoesNotStart(t *testing.T) {
	bound, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	listener := newMetricsListener(bound.Addr().String(), http.NotFoundHandler())
	if err := listener.Start(t.Context()); err == nil {
		t.Fatal("occupied metrics port accepted")
	}
}
