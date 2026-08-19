package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
	servingstate "github.com/flidai/leapview/internal/servingstate"
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

func TestBuildCreatesPrivateStateDirectories(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)
	application, err := app.Build(context.Background(), serveTestConfig(t, home))
	if err != nil {
		t.Fatalf("build development application: %v", err)
	}
	defer application.Shutdown(context.Background())
	for _, path := range []string{home, filepath.Join(home, "artifacts"), filepath.Join(home, "data"), filepath.Join(home, "duckdb"), filepath.Join(home, "ducklake"), filepath.Join(home, "runtime")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("mode for %s = %#o, want 0700", path, got)
		}
	}
}

func TestProductionApplicationAllowsCallbackHostAndRejectsOthers(t *testing.T) {
	home := t.TempDir()
	cfg := serveTestConfig(t, home)
	cfg.Production = true
	cfg.OIDCIssuerURL = "https://issuer.example"
	cfg.OIDCClientID = "client-id"
	cfg.OIDCSecret = "client-secret"
	cfg.OIDCCallbackURL = "https://app.example.com/auth/oidc/callback"
	cfg.PublicURL = "https://app.example.com"
	cfg.CSRFKey = "0123456789abcdef0123456789abcdef"
	cfg.MetricsBearerToken = "0123456789abcdef0123456789abcdef"
	application, err := app.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build production application: %v", err)
	}
	defer application.Shutdown(context.Background())
	for _, test := range []struct {
		name, host string
		want       int
	}{
		{name: "callback host", host: "app.example.com", want: http.StatusOK},
		{name: "unexpected host", host: "evil.example.com", want: http.StatusMisdirectedRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.Host = test.host
			response := httptest.NewRecorder()
			application.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func serveTestConfig(t testing.TB, home string) config.Config {
	t.Helper()
	fixture := extensionfixture.New(t, "ducklake")
	return config.Config{
		HomeDir: home, ManagedDataBackend: "local", ManagedDataDir: filepath.Join(home, "managed-data"),
		ManagedDataMaxFiles: 100, ManagedDataMaxFileBytes: 1 << 20, ManagedDataMaxRevisionBytes: 10 << 20,
		ManagedDataUploadSessionTTL: time.Hour, ManagedDataGCInterval: time.Hour, ManagedDataGCGracePeriod: time.Hour,
		ManagedDataMinFreeBytes: 1, DuckDBNodeMemoryMaxBytes: 256 << 20, DuckDBNodeTempMaxBytes: 1 << 30,
		DuckDBNodeMaxThreads: 2, QueryResultMaxRows: 10_000, QueryResultMaxBytes: 32 << 20,
		QueryCacheRuntimeMaxEntries: 16, QueryCacheRuntimeMaxBytes: 4 << 20,
		QueryCacheNodeMaxEntries: 64, QueryCacheNodeMaxBytes: 16 << 20,
		Environment:                 string(servingstate.DefaultEnvironment),
		DuckDBExtensionSupplyPath:   fixture.SupplyPath,
		DuckDBExtensionSupplySHA256: fixture.SupplySHA256,
		DuckDBExtensionCacheDir:     fixture.CacheDir,
	}
}
