package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	oidcauth "github.com/flidai/leapview/internal/access/oidc"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/workspace"
)

func TestAuthRouteRateLimit(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		RateLimits: RateLimitConfig{Enabled: true, AuthLimit: 1, AuthWindow: time.Minute},
	})
	handler := server.Routes()

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i == 0 && rec.Code == http.StatusTooManyRequests {
			t.Fatal("first auth request was unexpectedly rate limited")
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second auth status = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
	}
}

func TestPanicRecoveryWritesInternalServerError(t *testing.T) {
	handler := panicRecovery(slog.Default())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic detail leaked in response body: %q", rec.Body.String())
	}
}

func TestProductionRateLimitConfigDoesNotTrustProxyHeadersByDefault(t *testing.T) {
	if ProductionRateLimitConfig().UseRealIP {
		t.Fatal("production rate limit config trusts proxy headers by default")
	}
}

func TestAllowedHostsRejectsUnexpectedHost(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		AllowedHosts: []string{"app.example.com", "*.trusted.example.com"},
	})
	handler := server.Routes()

	for _, tc := range []struct {
		name       string
		host       string
		remoteAddr string
		want       int
	}{
		{name: "exact", host: "app.example.com", want: http.StatusOK},
		{name: "exact with port", host: "app.example.com:443", want: http.StatusOK},
		{name: "wildcard subdomain", host: "team.trusted.example.com", want: http.StatusOK},
		{name: "local healthcheck", host: "127.0.0.1", remoteAddr: "127.0.0.1:12345", want: http.StatusOK},
		{name: "spoofed loopback host", host: "127.0.0.1", remoteAddr: "203.0.113.10:12345", want: http.StatusMisdirectedRequest},
		{name: "wildcard apex", host: "trusted.example.com", want: http.StatusMisdirectedRequest},
		{name: "unexpected", host: "evil.example.com", want: http.StatusMisdirectedRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			if tc.remoteAddr != "" {
				req.RemoteAddr = tc.remoteAddr
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestAllowedHostRejectionsKeepProductionMiddlewareCoverage(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		AllowedHosts:    []string{"app.example.com"},
		RequestLogging:  true,
		Logger:          logger,
		SecurityHeaders: SecurityHeaders(true),
	})
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "evil.example.com"
	req.RemoteAddr = "203.0.113.10:12345"
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusMisdirectedRequest, rec.Body.String())
	}
	headers := rec.Result().Header
	for name, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := headers.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}

	logged := buf.String()
	for _, want := range []string{"method=GET", "path=/healthz", "status=421"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q missing %q", logged, want)
		}
	}
	if strings.Contains(logged, "secret-token") || strings.Contains(logged, "Authorization") {
		t.Fatalf("log %q leaked sensitive authorization data", logged)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Host = "app.example.com"
	metricsReq.RemoteAddr = "203.0.113.10:12345"
	metricsRec := httptest.NewRecorder()
	handler.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d body=%s", metricsRec.Code, http.StatusOK, metricsRec.Body.String())
	}
	if body := metricsRec.Body.String(); !strings.Contains(body, `status="421"`) {
		t.Fatalf("metrics output missing host rejection status:\n%s", body)
	}
}

func TestRequestBodyLimitRejectsOversizedContentLength(t *testing.T) {
	called := false
	handler := requestBodyLimit(RequestBodyLimitConfig{Enabled: true, MaxBytes: 4})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))
	req.ContentLength = 5
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if called {
		t.Fatal("next handler was called for oversized request")
	}
}

func TestRequestBodyLimitStopsStreamedOversizedBody(t *testing.T) {
	handler := requestBodyLimit(RequestBodyLimitConfig{Enabled: true, MaxBytes: 4})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestHealthRoutesAreUnauthenticated(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		Auth: testAuth(store, "test", AuthConfig{APITokenOnly: true}),
	}))
	handler := server.Routes()

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("%s Content-Type = %q, want application/json", path, got)
		}
	}
}

func TestLogoutSurfacesRevocationAuditFailure(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		Email:       "logout@example.com",
		DisplayName: "Logout Test",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	token, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		CREATE TRIGGER reject_session_revoked_audit
		BEFORE INSERT ON audit_events
		WHEN NEW.action = 'session.revoked'
		BEGIN
			SELECT RAISE(ABORT, 'forced session revocation audit failure');
		END;
	`); err != nil {
		t.Fatalf("create audit failure trigger: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "lv_session", Value: token})
	rec := httptest.NewRecorder()
	NewAuth(repo, AuthConfig{}).Logout(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("logout status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Fatalf("logout Location = %q, want no redirect", location)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("logout cookies = %#v, want session cookie unchanged", cookies)
	}
	if got, err := repo.PrincipalForToken(ctx, token); err != nil || got.ID != principal.ID {
		t.Fatalf("rolled-back session principal = %#v, err = %v", got, err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "sign_out"})
	if err != nil {
		t.Fatalf("list sign-out audit events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("sign-out audit events = %#v, want none", events)
	}
}

func TestAPITokenOnlyAuthChallengesInsteadOfOIDCRedirect(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		Auth:        testAuth(store, "test", AuthConfig{APITokenOnly: true}),
		WorkspaceID: "test",
	}))
	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want no OIDC redirect", got)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", got)
	}
}

func TestMetricsRouteExportsHTTPMetrics(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"leapview_http_requests_total",
		`method="GET"`,
		`route="/healthz"`,
		`status="200"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsRouteRequiresConfiguredBearerToken(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{

		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}))
	handler := server.Routes()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("metrics status without token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want Bearer challenge", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("metrics status with wrong token = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status with valid token = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "leapview_http_requests_total") {
		t.Fatalf("metrics output missing LeapView metrics:\n%s", body)
	}

	for _, header := range []string{
		"bearer 0123456789abcdef0123456789abcdef",
		"BEARER 0123456789abcdef0123456789abcdef",
		"Bearer   0123456789abcdef0123456789abcdef  ",
	} {
		req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", header)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("metrics status with Authorization %q = %d, want %d body=%s", header, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}

func TestMetricsRouteUsesAuthRateLimit(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{

		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
		RateLimits:         RateLimitConfig{Enabled: true, AuthLimit: 1, AuthWindow: time.Minute},
	}))
	handler := server.Routes()

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "192.0.2.30:1234"
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusUnauthorized {
			t.Fatalf("first metrics status = %d, want %d body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second metrics status = %d, want %d body=%s", rec.Code, http.StatusTooManyRequests, rec.Body.String())
		}
	}
}

func TestReadinessFailsWithoutPlatformStore(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

func TestReadinessChecksActiveWorkspaceRuntime(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{

		WorkspaceRepo: activeMetadataWorkspaceRepo{summaries: []workspace.Summary{{
			ID:                   "test-workspace",
			ActiveServingStateID: "deploy_1",
		}}},
	}))
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"workspaceRuntime:test-workspace":"ok"`) {
		t.Fatalf("readyz body missing active workspace runtime check:\n%s", body)
	}
}

func TestReadinessCanRequireOneActiveDeployment(t *testing.T) {
	handler := newHealth(healthConfig{
		Platform: func(context.Context) error { return nil },
		ActiveWorkspaces: func(context.Context) ([]string, error) {
			return nil, nil
		},
		RuntimeReady:            func(context.Context, string) error { return nil },
		RequireActiveDeployment: true,
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.Readyz(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(
			response.Body.String(),
			`"runtime":"no_active_deployments"`,
		) {
		t.Fatalf(
			"readyz without active deployment = %d %s",
			response.Code,
			response.Body.String(),
		)
	}
}

func TestReadinessFailsClosedOnRuntimeLeaseRenewalFailure(t *testing.T) {
	want := errors.New("snapshot lease renewal failed")
	handler := newHealth(healthConfig{
		Platform:          func(context.Context) error { return nil },
		RuntimeLeaseReady: func(context.Context) error { return want },
		ActiveWorkspaces:  func(context.Context) ([]string, error) { return nil, nil },
		RuntimeReady:      func(context.Context, string) error { return nil },
	})
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	handler.Readyz(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"runtimeLease":"failed"`) {
		t.Fatalf("lease failure readiness = %d %s", response.Code, response.Body.String())
	}
}

func TestReadinessIncludesMapAssetIntegrity(t *testing.T) {
	assets := &fakeMapAssetReadiness{}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{DashboardAssets: assets}))
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mapAssets":"ok"`) || assets.calls != 1 {
		t.Fatalf("ready map assets response = %d %s, calls=%d", response.Code, response.Body.String(), assets.calls)
	}

	assets.err = errors.New("map asset digest mismatch")
	response = httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"mapAssets":"map asset digest mismatch"`) {
		t.Fatalf("corrupt map assets response = %d %s", response.Code, response.Body.String())
	}
}

type fakeMapAssetReadiness struct {
	calls int
	err   error
}

func (f *fakeMapAssetReadiness) Verify(context.Context) error {
	f.calls++
	return f.err
}

func (*fakeMapAssetReadiness) Handler() http.Handler {
	return http.NotFoundHandler()
}

func TestReadinessFailsWhenActiveWorkspaceRuntimeIsMissing(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{

		WorkspaceRepo: activeMetadataWorkspaceRepo{summaries: []workspace.Summary{{
			ID:                   "missing",
			ActiveServingStateID: "deploy_1",
		}}},
	}))
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"workspaceRuntime:missing"`) || !strings.Contains(body, `runtime for workspace \"missing\" is not configured`) {
		t.Fatalf("readyz body missing runtime failure:\n%s", body)
	}
}

type activeMetadataWorkspaceRepo struct {
	summaries []workspace.Summary
	err       error
}

func (r activeMetadataWorkspaceRepo) Ensure(context.Context, workspace.EnsureInput) error {
	return nil
}

func (r activeMetadataWorkspaceRepo) List(context.Context) ([]workspace.Summary, error) {
	return r.summaries, r.err
}

func (r activeMetadataWorkspaceRepo) ListWithActiveMetadata(context.Context, string) ([]workspace.Summary, error) {
	return r.summaries, r.err
}

func (r activeMetadataWorkspaceRepo) ByID(_ context.Context, id workspace.WorkspaceID) (workspace.Summary, error) {
	for _, summary := range r.summaries {
		if summary.ID == id {
			return summary, r.err
		}
	}
	return workspace.Summary{ID: id}, r.err
}

func (r activeMetadataWorkspaceRepo) ActiveServingStateGraph(context.Context, workspace.WorkspaceID, string) (workspace.AssetGraph, bool, error) {
	return workspace.AssetGraph{}, false, r.err
}

func (r activeMetadataWorkspaceRepo) AssetVersions(context.Context, workspace.WorkspaceID, string, workspace.AssetID) ([]workspace.AssetVersion, error) {
	return nil, r.err
}

func TestDeploymentAPIRateLimitPreservesAuth(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		Auth: auth,

		WorkspaceID: "test",
		RateLimits:  RateLimitConfig{Enabled: true, APILimit: 1, APIWindow: time.Minute},
	}))
	handler := server.Routes()

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets", nil)
		req.RemoteAddr = "192.0.2.20:1234"
		req.Header.Set("Authorization", "Bearer dev")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusOK {
			t.Fatalf("first API status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second API status = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
		if i == 1 {
			if rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatalf("rate limit headers = %#v body=%s", rec.Header(), rec.Body.String())
			}
			var problem map[string]any
			if json.Unmarshal(rec.Body.Bytes(), &problem) != nil || problem["code"] != "RATE_LIMITED" || problem["requestId"] == "" {
				t.Fatalf("rate limit problem = %#v", problem)
			}
		}
	}
}

func TestDevBypassStillUsesGrantPrivileges(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	auth := NewAuth(repo, AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unseeded dev status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	if err := SeedLocalDeveloperPlatformAdmin(ctx, repo); err != nil {
		t.Fatalf("seed local developer: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seeded dev status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestUpdatesRateLimitAllowsOrdinaryReconnects(t *testing.T) {
	auth := testAuth(testStore(t), "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		Auth:       auth,
		RateLimits: RateLimitConfig{Enabled: true, UpdatesLimit: 2, UpdatesWindow: time.Minute},
	})
	handler := server.Routes()

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=dashboard&workspace=test-workspace&dashboard=executive-sales&page=overview", nil)
		req.RemoteAddr = "192.0.2.30:1234"
		req.Header.Set("Authorization", "Bearer dev")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("reconnect %d was unexpectedly rate limited", i+1)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		SecurityHeaders: SecurityHeaders(true),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	headers := rec.Result().Header
	for name, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Permissions-Policy":        "camera=(), microphone=(), geolocation=()",
		"X-Frame-Options":           "SAMEORIGIN",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := headers.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	csp := headers.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'self'",
		"script-src 'self' 'unsafe-eval'",
		"style-src 'self' 'unsafe-inline'",
		"connect-src 'self'",
		"worker-src 'self' blob:",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("Content-Security-Policy missing %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "cdn.jsdelivr.net") {
		t.Fatalf("Content-Security-Policy allows CDN scripts: %q", csp)
	}
}

func TestSecurityHeadersOmitHSTSWhenDisabled(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		SecurityHeaders: SecurityHeaders(false),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if got := rec.Result().Header.Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q, want empty", got)
	}
}

func TestRequestLoggerDoesNotLogSensitiveHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{
		RequestLogging: true,
		Logger:         logger,
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Request-ID", "req_123")
	req.Header.Set("X-Correlation-ID", "corr_456")
	req.AddCookie(&http.Cookie{Name: "lv_session", Value: "secret-session"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	logged := buf.String()
	for _, want := range []string{"method=GET", "path=/", "status=200", "duration=", "bytes=", "request_id=req_123", "correlation_id=corr_456"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q missing %q", logged, want)
		}
	}
	for _, secret := range []string{"secret-token", "secret-session", "Authorization", "Cookie"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log %q contains sensitive value %q", logged, secret)
		}
	}
}

func TestAuthBeginCreatesOIDCStateCookieAndRedirect(t *testing.T) {
	auth := testAuth(testStore(t), "test", AuthConfig{
		DevBypass:    true,
		CSRFKey:      "0123456789abcdef0123456789abcdef",
		CookieSecure: true,
	})
	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{authURL: "https://issuer.example/authorize"}})

	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2", nil)
	rec := httptest.NewRecorder()
	auth.Begin(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "https://issuer.example/authorize?") {
		t.Fatalf("Location = %q", location)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	state := parsed.Query().Get("state")
	nonce := parsed.Query().Get("nonce")
	if state == "" || nonce == "" {
		t.Fatalf("redirect missing state/nonce: %s", location)
	}
	cookie := oidcStateCookieForTest(rec.Result().Cookies())
	if cookie == nil {
		t.Fatalf("%s cookie missing", oidcStateCookieName)
	}
	if cookie.HttpOnly != true || cookie.Secure != true || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 10*60 {
		t.Fatalf("state cookie options = %#v", cookie)
	}
	cookieState, cookieNonce, err := auth.DecodeOIDCState(cookie.Value)
	if err != nil {
		t.Fatalf("decode state cookie: %v", err)
	}
	if cookieState != state || cookieNonce != nonce {
		t.Fatalf("cookie state/nonce = %q/%q, redirect = %q/%q", cookieState, cookieNonce, state, nonce)
	}
}

func TestAuthBeginFailsClosedWhenRandomnessUnavailable(t *testing.T) {
	restore := setAuthRandomReaderForTest(errReader{})
	defer restore()
	auth := testAuth(testStore(t), "test", AuthConfig{
		DevBypass:    true,
		CSRFKey:      "0123456789abcdef0123456789abcdef",
		CookieSecure: true,
	})
	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{authURL: "https://issuer.example/authorize"}})

	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2", nil)
	rec := httptest.NewRecorder()
	auth.Begin(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want no redirect", got)
	}
	if cookie := oidcStateCookieForTest(rec.Result().Cookies()); cookie != nil {
		t.Fatalf("unexpected state cookie: %#v", cookie)
	}
}

func TestProductionConfigFailsBeforeAuthStoreSetupWithoutCSRFKey(t *testing.T) {
	cfg := config.Config{Production: true, APITokenOnlyAuth: true}
	if err := cfg.ValidateProductionAuth(); err == nil {
		t.Fatal("expected production auth validation to fail")
	}
}

func TestAuthCallbackRejectsInvalidOIDCState(t *testing.T) {
	auth := testAuth(testStore(t), "test", AuthConfig{
		DevBypass: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{}})
	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=wrong&code=code", nil)
	req.AddCookie(auth.OIDCStateCookie("right", "nonce"))
	rec := httptest.NewRecorder()

	auth.Callback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthCallbackRejectsExpiredOIDCStateCookie(t *testing.T) {
	issuedAt := time.Date(2026, 7, 7, 8, 0, 0, 0, time.UTC)
	restore := setAuthNowForTest(issuedAt)
	auth := testAuth(testStore(t), "test", AuthConfig{
		DevBypass: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{}})
	cookie := auth.OIDCStateCookie("state", "nonce")
	restore()
	restore = setAuthNowForTest(issuedAt.Add(11 * time.Minute))
	defer restore()

	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=state&code=code", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()

	auth.Callback(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "" {
		t.Fatalf("expired state redirected unexpectedly: %q", location)
	}
}

func TestAuthCallbackCreatesSessionAndAuditEvents(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{
		DevBypass: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{claims: oidcauth.Claims{Issuer: "https://issuer.example", Subject: "subject-1", Email: "user@example.com", Name: "User Example"}}})
	target := "/oauth/authorize?client_id=claude&resource=https%3A%2F%2Fleapview.example%2Fmcp&response_type=code"
	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=state&code=code", nil)
	req.AddCookie(auth.OIDCStateCookie("state", "nonce"))
	req.AddCookie(auth.AuthReturnCookie(target))
	rec := httptest.NewRecorder()

	auth.Callback(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != target {
		t.Fatalf("callback redirect = %q, want %q", location, target)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "lv_session" && cookie.Value != "" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("session cookie missing")
	}
	principal, err := accesssqlite.NewRepository(store.SQLDB()).PrincipalForToken(context.Background(), sessionCookie.Value)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if principal.Email != "user@example.com" || principal.DisplayName != "User Example" {
		t.Fatalf("principal = %#v", principal)
	}
	events, err := accesssqlite.NewRepository(store.SQLDB()).ListAuditEvents(context.Background(), access.AuditEventFilter{Action: "sign_in"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 || events[0].PrincipalID != principal.ID {
		t.Fatalf("sign_in events = %#v, principal %q", events, principal.ID)
	}
}

func TestAuthenticationReturnCookieRejectsExternalAndTamperedTargets(t *testing.T) {
	auth := testAuth(testStore(t), "test", AuthConfig{CSRFKey: "0123456789abcdef0123456789abcdef"})
	for name, cookie := range map[string]*http.Cookie{
		"external": auth.AuthReturnCookie("https://attacker.example/oauth/authorize"),
		"tampered": {
			Name: authReturnCookieName, Value: auth.AuthReturnCookie("/oauth/authorize?client_id=safe").Value + "tampered",
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			if target := auth.AuthenticationRedirectTarget(response, request, "/"); target != "/" {
				t.Fatalf("authentication redirect = %q, want fallback", target)
			}
		})
	}
}

func TestLocalLoginCreatesSessionAndAuditEvents(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "local@example.com", DisplayName: "Local User"})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{
		LocalAuth: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(url.Values{
		"email":    {"local@example.com"},
		"password": {created.Password},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	auth.LocalLogin(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 body=%s", rec.Code, rec.Body.String())
	}
	principal := principalFromSessionCookie(t, repo, rec.Result().Cookies())
	if principal.ID != created.Principal.ID {
		t.Fatalf("session principal = %#v, want %s", principal, created.Principal.ID)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "sign_in"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 || events[0].PrincipalID != created.Principal.ID || events[0].Status != "success" {
		t.Fatalf("sign_in events = %#v", events)
	}
}

func TestLocalLoginResumesProtectedOAuthAuthorizationRequest(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "oauth@example.com", DisplayName: "OAuth User"})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{
		LocalAuth: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	target := "/oauth/authorize?client_id=claude&code_challenge=challenge&resource=https%3A%2F%2Fleapview.example%2Fmcp&response_type=code"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	redirect := httptest.NewRecorder()
	auth.Middleware("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated authorization request reached the protected handler")
	})).ServeHTTP(redirect, request)
	if redirect.Code != http.StatusFound || redirect.Header().Get("Location") != "/login" {
		t.Fatalf("authorization redirect = %d %q", redirect.Code, redirect.Header().Get("Location"))
	}
	var returnCookie *http.Cookie
	for _, cookie := range redirect.Result().Cookies() {
		if cookie.Name == "lv_auth_return" {
			returnCookie = cookie
			break
		}
	}
	if returnCookie == nil || !returnCookie.HttpOnly {
		t.Fatalf("authorization return cookie = %#v", returnCookie)
	}

	login := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(url.Values{
		"email":    {"oauth@example.com"},
		"password": {created.Password},
	}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.AddCookie(returnCookie)
	response := httptest.NewRecorder()
	auth.LocalLogin(response, login)
	if response.Code != http.StatusFound || response.Header().Get("Location") != target {
		t.Fatalf("login redirect = %d %q, want %q", response.Code, response.Header().Get("Location"), target)
	}
}

func TestLocalLoginRejectsBadPasswordAndAuditsDenied(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	if _, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "local@example.com"}); err != nil {
		t.Fatalf("create local user: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{
		LocalAuth: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/local/login", strings.NewReader(url.Values{
		"email":    {"local@example.com"},
		"password": {"bad-password"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	auth.LocalLogin(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 body=%s", rec.Code, rec.Body.String())
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "sign_in"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 || events[0].Status != "denied" {
		t.Fatalf("sign_in denied events = %#v", events)
	}
}

func TestLocalPasswordMustChangeBlocksProtectedRoutesUntilChanged(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "local@example.com", MustChange: true})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: created.Principal.ID, Email: created.Principal.Email, Role: access.RolePlatformAdmin}); err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{
		LocalAuth: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test"}))
	sessionSecret, err := repo.CreateSession(ctx, created.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(auth.SessionCookie(sessionSecret, time.Now().Add(time.Hour)))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("must-change status = %d, want 403 body=%s", rec.Code, rec.Body.String())
	}

	initialPublisherSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: created.Principal.ID,
		Name:        access.APITokenNameInitialPublisher,
		Privileges:  []access.Privilege{access.PrivilegeUseWorkspace},
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create initial publisher token: %v", err)
	}
	publisherReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	publisherReq.Header.Set("Authorization", "Bearer "+initialPublisherSecret)
	publisherRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(publisherRec, publisherReq)
	if publisherRec.Code != http.StatusOK {
		t.Fatalf("initial publisher with must-change administrator status = %d, want 200 body=%s", publisherRec.Code, publisherRec.Body.String())
	}

	regularSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: created.Principal.ID,
		Name:        "regular-token",
		Privileges:  []access.Privilege{access.PrivilegeUseWorkspace},
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create regular token: %v", err)
	}
	regularReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	regularReq.Header.Set("Authorization", "Bearer "+regularSecret)
	regularRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(regularRec, regularReq)
	if regularRec.Code != http.StatusForbidden {
		t.Fatalf("regular token with must-change administrator status = %d, want 403 body=%s", regularRec.Code, regularRec.Body.String())
	}

	passwordReq := httptest.NewRequest(http.MethodPost, "/auth/local/password", strings.NewReader(url.Values{
		"currentPassword": {created.Password},
		"newPassword":     {"changed-password"},
	}.Encode()))
	passwordReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	passwordReq.AddCookie(auth.SessionCookie(sessionSecret, time.Now().Add(time.Hour)))
	passwordRec := httptest.NewRecorder()
	auth.LocalPassword(passwordRec, passwordReq)
	if passwordRec.Code != http.StatusFound {
		t.Fatalf("password change status = %d, want 302 body=%s", passwordRec.Code, passwordRec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(auth.SessionCookie(sessionSecret, time.Now().Add(time.Hour)))
	rec = httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("after password change status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthCallbackUsesOIDCIssuerAndSubjectAsStableIdentity(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{
		DevBypass: true,
		CSRFKey:   "0123456789abcdef0123456789abcdef",
	})
	repo := accesssqlite.NewRepository(store.SQLDB())

	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{claims: oidcauth.Claims{Issuer: "https://issuer.example", Subject: "subject-1", Email: "first@example.com", Name: "First"}}})
	req := httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=state&code=code", nil)
	req.AddCookie(auth.OIDCStateCookie("state", "nonce"))
	rec := httptest.NewRecorder()
	auth.Callback(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("first callback status = %d, want 302 body=%s", rec.Code, rec.Body.String())
	}
	first := principalFromSessionCookie(t, repo, rec.Result().Cookies())

	auth.ConfigureOIDCTestClients(map[string]oidcClient{"azureadv2": fakeOIDCClient{claims: oidcauth.Claims{Issuer: "https://issuer.example", Subject: "subject-1", Email: "second@example.com", Name: "Second"}}})
	req = httptest.NewRequest(http.MethodGet, "/auth/azureadv2/callback?state=state&code=code", nil)
	req.AddCookie(auth.OIDCStateCookie("state", "nonce"))
	rec = httptest.NewRecorder()
	auth.Callback(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("second callback status = %d, want 302 body=%s", rec.Code, rec.Body.String())
	}
	second := principalFromSessionCookie(t, repo, rec.Result().Cookies())

	if second.ID != first.ID {
		t.Fatalf("principal changed after email update: first=%q second=%q", first.ID, second.ID)
	}
	if second.Email != "second@example.com" || second.DisplayName != "Second" {
		t.Fatalf("updated principal = %#v, want latest OIDC metadata", second)
	}
}

func TestAuthAuditsDisabledPrincipalCredentialFailures(t *testing.T) {
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	ctx := context.Background()
	user, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{
		ExternalID:  "disabled-auth-user",
		UserName:    "disabled-auth@example.com",
		Email:       "disabled-auth@example.com",
		DisplayName: "Disabled Auth",
		Active:      true,
	})
	if err != nil {
		t.Fatalf("create SCIM user: %v", err)
	}
	sessionSecret, err := repo.CreateSession(ctx, user.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	apiSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: user.Principal.ID, Name: "disabled-auth-token"})
	if err != nil {
		t.Fatalf("create API token: %v", err)
	}
	if _, err := repo.DisableSCIMUser(ctx, user.Principal.ID); err != nil {
		t.Fatalf("disable SCIM user: %v", err)
	}
	auth := NewAuth(repo, AuthConfig{CSRFKey: "0123456789abcdef0123456789abcdef"})

	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test", nil)
	apiReq.Header.Set("Authorization", "Bearer "+apiSecret)
	apiReq.Header.Set("X-Request-ID", "disabled_api_req")
	if _, _, ok := auth.Authenticate(apiReq); ok {
		t.Fatal("disabled API token authenticated")
	}
	sessionReq := httptest.NewRequest(http.MethodGet, "/workspaces/test", nil)
	sessionReq.AddCookie(&http.Cookie{Name: "lv_session", Value: sessionSecret})
	sessionReq.Header.Set("X-Request-ID", "disabled_session_req")
	if _, _, ok := auth.Authenticate(sessionReq); ok {
		t.Fatal("disabled session authenticated")
	}
	if _, err := repo.CredentialForAPIToken(ctx, apiSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled api credential err = %v, want sql.ErrNoRows", err)
	}

	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "credential.denied"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("credential denied events = %d, want 2: %#v", len(events), events)
	}
	byRequest := map[string]access.AuditEvent{}
	for _, event := range events {
		byRequest[event.RequestID] = event
	}
	for requestID, targetType := range map[string]string{"disabled_api_req": "api_token", "disabled_session_req": "session"} {
		event, ok := byRequest[requestID]
		if !ok {
			t.Fatalf("missing audit event for %s: %#v", requestID, events)
		}
		if event.PrincipalID != user.Principal.ID || event.Status != "denied" || event.TargetType != targetType || !strings.Contains(event.MetadataJSON, "principal_disabled") {
			t.Fatalf("audit event for %s = %#v", requestID, event)
		}
	}
}

func TestInvalidBearerDoesNotFallBackToSession(t *testing.T) {
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	ctx := context.Background()
	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		Email:       "owner@example.com",
		DisplayName: "Owner",
		Role:        access.RolePlatformAdmin,
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	sessionSecret, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	auth := NewAuth(repo, AuthConfig{CSRFKey: "0123456789abcdef0123456789abcdef"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	req.AddCookie(&http.Cookie{Name: "lv_session", Value: sessionSecret})
	if _, _, ok := auth.Authenticate(req); ok {
		t.Fatal("invalid bearer authenticated by falling back to the session cookie")
	}
}

func TestBearerTokenParserAcceptsStandardAuthSchemeVariations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{name: "canonical", header: "Bearer secret", want: "secret"},
		{name: "lowercase scheme", header: "bearer secret", want: "secret"},
		{name: "uppercase scheme", header: "BEARER secret", want: "secret"},
		{name: "extra whitespace", header: "Bearer   secret  ", want: "secret"},
		{name: "missing token", header: "Bearer   ", want: ""},
		{name: "wrong scheme", header: "Basic secret", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tc.header)
			if got := bearerToken(req); got != tc.want {
				t.Fatalf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestCSRFBearerBypassDoesNotApplyWhenSessionCookiePresent(t *testing.T) {
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	ctx := context.Background()
	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		Email:       "owner@example.com",
		DisplayName: "Owner",
		Role:        access.RolePlatformAdmin,
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	sessionSecret, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	apiSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: principal.ID, Name: "cli"})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	auth := NewAuth(repo, AuthConfig{CSRFKey: "0123456789abcdef0123456789abcdef"})
	handler := auth.CSRFMiddleware(auth.Middleware("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))

	sessionReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/mutate", nil)
	sessionReq.Header.Set("Authorization", "Bearer invalid-token")
	sessionReq.AddCookie(&http.Cookie{Name: "lv_session", Value: sessionSecret})
	sessionRec := httptest.NewRecorder()
	handler.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusForbidden {
		t.Fatalf("session+invalid bearer POST without CSRF status = %d, want %d", sessionRec.Code, http.StatusForbidden)
	}

	apiReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/mutate", nil)
	apiReq.Header.Set("Authorization", "Bearer "+apiSecret)
	apiRec := httptest.NewRecorder()
	handler.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusNoContent {
		t.Fatalf("api bearer POST without CSRF status = %d, want %d body=%s", apiRec.Code, http.StatusNoContent, apiRec.Body.String())
	}
}

type fakeOIDCClient struct {
	authURL string
	claims  oidcauth.Claims
}

func (c fakeOIDCClient) AuthCodeURL(state, nonce string) string {
	base := c.authURL
	if base == "" {
		base = "https://issuer.example/authorize"
	}
	values := url.Values{}
	values.Set("state", state)
	values.Set("nonce", nonce)
	return base + "?" + values.Encode()
}

func (c fakeOIDCClient) Authenticate(_ context.Context, _ string, expectedNonce string) (oidcauth.Claims, error) {
	if expectedNonce != "nonce" {
		return oidcauth.Claims{}, errUnauthorized
	}
	return c.claims, nil
}

func oidcStateCookieForTest(cookies []*http.Cookie) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == oidcStateCookieName {
			return cookie
		}
	}
	return nil
}

func principalFromSessionCookie(t *testing.T, repo *accesssqlite.Repository, cookies []*http.Cookie) access.Principal {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == "lv_session" && cookie.Value != "" {
			principal, err := repo.PrincipalForToken(context.Background(), cookie.Value)
			if err != nil {
				t.Fatalf("resolve session: %v", err)
			}
			return principal
		}
	}
	t.Fatal("session cookie missing")
	return access.Principal{}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
