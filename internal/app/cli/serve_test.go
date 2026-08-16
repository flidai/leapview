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

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workspace"
)

func buildServeTestApplication(ctx context.Context, cfg config.Config, production bool, environment servingstate.Environment) (*app.Application, func(), error) {
	cfg.Production = production
	cfg.Environment = string(environment)
	application, err := app.Build(ctx, cfg)
	return application, func() {
		if application != nil {
			_ = application.Shutdown(context.Background())
		}
	}, err
}

func TestServeProductionModeHonorsConfigEnv(t *testing.T) {
	cfg := config.Config{Production: true}
	if !serveProductionMode(cfg, rootOptions{}) {
		t.Fatal("serve production mode ignored config production setting")
	}
}

func TestServeCommandConstructionDoesNotParseEnvironment(t *testing.T) {
	t.Setenv("LEAPVIEW_WORKLOAD_INTERACTIVE_MAX_RUNNING", "invalid")
	cmd := serveCommand(context.Background(), &rootOptions{})
	if cmd == nil {
		t.Fatal("serveCommand() returned nil")
	}
}

func TestServeEnvironmentDefaultsToProductionEnvironment(t *testing.T) {
	if got := serveEnvironment(true, "", ""); got != servingstate.Environment("prod") {
		t.Fatalf("production serve environment = %q, want prod", got)
	}
	if got := serveEnvironment(false, "", ""); got != servingstate.DefaultEnvironment {
		t.Fatalf("development serve environment = %q, want %q", got, servingstate.DefaultEnvironment)
	}
	if got := serveEnvironment(true, "dev", "prod"); got != servingstate.DefaultEnvironment {
		t.Fatalf("explicit production serve environment = %q, want dev", got)
	}
	if got := serveEnvironment(true, "", "staging"); got != servingstate.Environment("staging") {
		t.Fatalf("configured serve environment = %q, want staging", got)
	}
}

func TestServeEnvironmentFlagValueIgnoresSharedCommandDefaults(t *testing.T) {
	if got := serveEnvironmentFlagValue(false, "dev"); got != "" {
		t.Fatalf("unchanged serve environment flag value = %q, want empty", got)
	}
	if got := serveEnvironmentFlagValue(true, "dev"); got != "dev" {
		t.Fatalf("changed serve environment flag value = %q, want dev", got)
	}
}

func TestListenURLHandlesQualifiedAddr(t *testing.T) {
	for _, tt := range []struct {
		addr string
		want string
	}{
		{addr: ":8080", want: "http://localhost:8080"},
		{addr: "127.0.0.1:18080", want: "http://127.0.0.1:18080"},
		{addr: "0.0.0.0:8080", want: "http://0.0.0.0:8080"},
	} {
		if got := listenURL(tt.addr); got != tt.want {
			t.Fatalf("listenURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestProductionHTTPServerHasTimeouts(t *testing.T) {
	server := productionHTTPServer(":0", http.NewServeMux())
	if server.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout is not configured")
	}
	if server.ReadTimeout <= 0 {
		t.Fatal("ReadTimeout is not configured")
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want zero for long-lived SSE", server.WriteTimeout)
	}
	if server.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout is not configured")
	}
	if server.Shutdown(context.Background()) != nil {
		t.Fatal("empty server shutdown should be a no-op")
	}
}

func TestResponseLivenessKeepsSSEAliveBeyondOrdinaryTimeout(t *testing.T) {
	server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not implement http.Flusher")
			return
		}
		_, _ = w.Write([]byte("data: one\n\n"))
		flusher.Flush()
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("data: two\n\n"))
		flusher.Flush()
	}), 20*time.Millisecond, time.Second))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL)
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
}

func TestResponseLivenessDisconnectCancelsStream(t *testing.T) {
	streamCanceled := make(chan struct{})
	server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: one\n\n"))
		flusher.Flush()
		<-r.Context().Done()
		close(streamCanceled)
	}), 5*time.Second, 100*time.Millisecond))
	defer server.Close()

	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	_, _ = io.ReadAll(io.LimitReader(response.Body, int64(len("data: one\n\n"))))
	_ = response.Body.Close()
	select {
	case <-streamCanceled:
	case <-time.After(time.Second):
		t.Fatal("disconnected stream handler was not canceled")
	}
}

func TestResponseLivenessCancelsIdleSSE(t *testing.T) {
	idleCanceled := make(chan struct{})
	server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(idleCanceled)
	}), time.Second, 20*time.Millisecond))
	defer server.Close()

	response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer response.Body.Close()
	select {
	case <-idleCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("idle stream context was not canceled")
	}
}

func TestResponseLivenessBoundsNonSSEResponse(t *testing.T) {
	server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}), 20*time.Millisecond, time.Second))
	defer server.Close()

	started := time.Now()
	response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ordinary response took %s, want deadline enforcement", elapsed)
	}
	// If the deadline expires before headers are written, net/http correctly
	// closes the connection and the client observes EOF. If headers were already
	// committed, the response is truncated instead. Both are valid enforcement.
	if err != nil {
		return
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if len(body) != 0 {
		t.Fatalf("ordinary response body = %q, want empty after write deadline", body)
	}
}

func TestResponseLivenessDoesNotPromoteLateResponseToSSE(t *testing.T) {
	server := httptest.NewServer(withResponseLiveness(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("data: late\n\n"))
		w.(http.Flusher).Flush()
	}), 20*time.Millisecond, time.Second))
	defer server.Close()

	response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
	if err != nil {
		return
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if len(body) != 0 {
		t.Fatalf("late SSE body = %q, want ordinary deadline enforcement", body)
	}
}

func TestDefaultHTTPServerShutdownTimeout(t *testing.T) {
	if defaultHTTPServerShutdownTimeout < 5*time.Second {
		t.Fatalf("shutdown timeout = %s, want at least 5s", defaultHTTPServerShutdownTimeout)
	}
}

func TestDeploymentBackedDevServerAlwaysOpensPlatformStore(t *testing.T) {
	home := t.TempDir()
	_, cleanup, err := buildServeTestApplication(context.Background(), serveTestConfig(home), false, servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("deployment-backed dev server: %v", err)
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(home, "leapview.db")); err != nil {
		t.Fatalf("platform store was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "artifacts")); err != nil {
		t.Fatalf("artifact directory was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "data")); err != nil {
		t.Fatalf("DuckLake data directory was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "ducklake")); err != nil {
		t.Fatalf("DuckLake catalog directory was not created: %v", err)
	}
}

func TestDeploymentBackedServerCreatesPrivateStateDirectories(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	restoreUmask := setServeTestUmask(t, 0)
	_, cleanup, err := buildServeTestApplication(context.Background(), serveTestConfig(home), false, servingstate.DefaultEnvironment)
	restoreUmask()
	if err != nil {
		t.Fatalf("deployment-backed dev server: %v", err)
	}
	defer cleanup()

	for _, path := range []string{
		home,
		filepath.Join(home, "artifacts"),
		filepath.Join(home, "data"),
		filepath.Join(home, "duckdb"),
		filepath.Join(home, "ducklake"),
		filepath.Join(home, "runtime"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("mode for %s = %#o, want 0700", path, got)
		}
	}
}

func TestProductionServerAllowsCallbackHostAndRejectsOthers(t *testing.T) {
	home := t.TempDir()
	cfg := serveTestConfig(home)
	cfg.Production = true
	cfg.OIDCIssuerURL = "https://issuer.example"
	cfg.OIDCClientID = "client-id"
	cfg.OIDCSecret = "client-secret"
	cfg.OIDCCallbackURL = "https://app.example.com/auth/oidc/callback"
	cfg.PublicURL = "https://app.example.com"
	cfg.CSRFKey = "0123456789abcdef0123456789abcdef"
	cfg.MetricsBearerToken = "0123456789abcdef0123456789abcdef"
	server, cleanup, err := buildServeTestApplication(context.Background(), cfg, true, servingstate.Environment("prod"))
	if err != nil {
		t.Fatalf("production server: %v", err)
	}
	defer cleanup()
	handler := server.Handler()

	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{name: "callback host", host: "app.example.com", want: http.StatusOK},
		{name: "unexpected host", host: "evil.example.com", want: http.StatusMisdirectedRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func setServeTestUmask(t *testing.T, mask int) func() {
	t.Helper()
	old := syscall.Umask(mask)
	return func() {
		syscall.Umask(old)
	}
}

func TestDeploymentBackedDevServerSeedsPlatformAdminPrincipal(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	_, cleanup, err := buildServeTestApplication(ctx, serveTestConfig(home), false, servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("deployment-backed dev server: %v", err)
	}
	defer cleanup()

	store, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	repo := accesssqlite.NewRepository(store.SQLDB())
	if err := projectsqlite.NewRepositoryWithSecurables(store.SQLDB(), repo).Ensure(ctx, workspace.EnsureInput{ID: "other", Title: "Other"}); err != nil {
		t.Fatalf("ensure other workspace: %v", err)
	}
	principal, err := repo.PrincipalByID(ctx, "dev")
	if err != nil {
		t.Fatalf("lookup dev principal: %v", err)
	}
	if principal.Email != "dev@localhost" || principal.DisplayName != "Local Developer" {
		t.Fatalf("dev principal = %#v, want Local Developer", principal)
	}
	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeManageGrants, access.WorkspaceObject("other"))
	if err != nil {
		t.Fatalf("check dev platform privilege: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("local dev principal missing platform admin privilege")
	}
}

func TestDeploymentBackedDevServerDoesNotCreateWorkspacesOrDeployments(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	_, cleanup, err := buildServeTestApplication(ctx, serveTestConfig(home), false, servingstate.DefaultEnvironment)
	if err != nil {
		t.Fatalf("deployment-backed dev server: %v", err)
	}
	defer cleanup()

	store, err := platform.Open(ctx, filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	workspaceRepo := projectsqlite.NewRepository(store.SQLDB())
	workspaces, err := workspaceRepo.List(ctx)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(workspaces) != 0 {
		t.Fatalf("workspaces = %#v, want none before explicit deploy", workspaces)
	}
	var servingStateCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM serving_states`).Scan(&servingStateCount); err != nil {
		t.Fatalf("count serving states: %v", err)
	}
	if servingStateCount != 0 {
		t.Fatalf("serving state count = %d, want none before explicit deploy", servingStateCount)
	}
}

func serveTestConfig(home string) config.Config {
	return config.Config{
		HomeDir:                       home,
		ManagedDataBackend:            "local",
		ManagedDataDir:                filepath.Join(home, "managed-data"),
		ManagedDataMaxFiles:           100,
		ManagedDataMaxFileBytes:       1 << 20,
		ManagedDataMaxRevisionBytes:   10 << 20,
		ManagedDataUploadSessionTTL:   time.Hour,
		ManagedDataGCInterval:         time.Hour,
		ManagedDataGCGracePeriod:      time.Hour,
		ManagedDataMinFreeBytes:       1,
		DuckDBNodeMemoryMaxBytes:      256 << 20,
		DuckDBNodeTempMaxBytes:        1 << 30,
		DuckDBNodeMaxThreads:          2,
		QueryResultMaxRows:            10_000,
		QueryResultMaxBytes:           32 << 20,
		QueryCacheRuntimeMaxEntries:   16,
		QueryCacheRuntimeMaxBytes:     4 << 20,
		QueryCacheWorkspaceMaxEntries: 32,
		QueryCacheWorkspaceMaxBytes:   8 << 20,
		QueryCacheNodeMaxEntries:      64,
		QueryCacheNodeMaxBytes:        16 << 20,
	}
}
