package httpmiddleware

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPanicRecoveryWritesGenericInternalServerError(t *testing.T) {
	handler := PanicRecovery(slog.Default())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret panic detail")
	}))
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "secret panic detail") {
		t.Fatalf("panic detail leaked in response body: %q", response.Body.String())
	}
}

func TestAllowedHostsRejectsUnexpectedAndSpoofedLoopbackHosts(t *testing.T) {
	handler := AllowedHosts([]string{"app.example.com", "*.trusted.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name, host, remote string
		want               int
	}{
		{name: "exact", host: "app.example.com", want: http.StatusNoContent},
		{name: "exact port", host: "app.example.com:443", want: http.StatusNoContent},
		{name: "wildcard subdomain", host: "team.trusted.example.com", want: http.StatusNoContent},
		{name: "loopback local", host: "127.0.0.1", remote: "127.0.0.1:1234", want: http.StatusNoContent},
		{name: "spoofed loopback", host: "127.0.0.1", remote: "203.0.113.10:1234", want: http.StatusMisdirectedRequest},
		{name: "wildcard apex", host: "trusted.example.com", want: http.StatusMisdirectedRequest},
		{name: "unexpected", host: "evil.example.com", want: http.StatusMisdirectedRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			request.Host = test.host
			if test.remote != "" {
				request.RemoteAddr = test.remote
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestRequestBodyLimitRejectsDeclaredAndStreamedOversize(t *testing.T) {
	t.Run("content length", func(t *testing.T) {
		called := false
		handler := RequestBodyLimit(RequestBodyLimitConfig{Enabled: true, MaxBytes: 4})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))
		request.ContentLength = 5
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || called {
			t.Fatalf("status=%d called=%v, want 413 and handler not called", response.Code, called)
		}
	})

	t.Run("stream", func(t *testing.T) {
		handler := RequestBodyLimit(RequestBodyLimitConfig{Enabled: true, MaxBytes: 4})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
		request.ContentLength = -1
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
		}
	})
}

func TestPrivateResponseDisablesBrowserAndSharedCaching(t *testing.T) {
	handler := PrivateResponse(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))

	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
}

func TestSecurityHeadersAndOptionalHSTS(t *testing.T) {
	for _, test := range []struct {
		name string
		hsts bool
	}{
		{name: "enabled", hsts: true},
		{name: "without hsts", hsts: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := SecurityHeadersMiddleware(SecurityHeaders(test.hsts))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			for name, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "strict-origin-when-cross-origin",
				"Permissions-Policy":     "camera=(), microphone=(), geolocation=()",
				"X-Frame-Options":        "SAMEORIGIN",
			} {
				if got := response.Header().Get(name); got != want {
					t.Fatalf("%s = %q, want %q", name, got, want)
				}
			}
			csp := response.Header().Get("Content-Security-Policy")
			for _, want := range []string{"default-src 'self'", "object-src 'none'", "frame-ancestors 'self'", "script-src 'self'", "script-src-attr 'none'", "style-src-attr 'none'", "connect-src 'self'", "worker-src 'self' blob:"} {
				if !strings.Contains(csp, want) {
					t.Fatalf("CSP missing %q: %q", want, csp)
				}
			}
			if strings.Contains(csp, "'unsafe-eval'") || strings.Contains(csp, "'unsafe-inline'") {
				t.Fatalf("non-document CSP contains an unsafe directive: %q", csp)
			}
			if strings.Contains(csp, "cdn.jsdelivr.net") {
				t.Fatalf("CSP allows CDN scripts: %q", csp)
			}
			if got := response.Header().Get("Strict-Transport-Security"); test.hsts != (got != "") {
				t.Fatalf("HSTS presence=%v, want %v (%q)", got != "", test.hsts, got)
			}
		})
	}
}

func TestSecurityHeadersRestrictCSPExceptionsToHTMLDocuments(t *testing.T) {
	tests := []struct {
		name              string
		contentType       string
		body              string
		wantUnsafeEval    bool
		wantDynamicStyles bool
	}{
		{name: "html", contentType: "text/html; charset=utf-8", body: "<!doctype html><html></html>", wantUnsafeEval: true, wantDynamicStyles: true},
		{name: "detected html", body: "<!doctype html><html></html>", wantUnsafeEval: true, wantDynamicStyles: true},
		{name: "json", contentType: "application/json", body: `{\"status\":\"ok\"}`},
		{name: "event stream", contentType: "text/event-stream", body: "event: ready\\n\\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := SecurityHeadersMiddleware(SecurityHeaders(false))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.contentType != "" {
					w.Header().Set("Content-Type", test.contentType)
				}
				_, _ = w.Write([]byte(test.body))
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

			policy := response.Header().Get("Content-Security-Policy")
			if got := strings.Contains(policy, "'unsafe-eval'"); got != test.wantUnsafeEval {
				t.Fatalf("unsafe-eval presence = %v, want %v: %q", got, test.wantUnsafeEval, policy)
			}
			if got := strings.Contains(policy, "'unsafe-inline'"); got != test.wantDynamicStyles {
				t.Fatalf("unsafe-inline presence = %v, want %v: %q", got, test.wantDynamicStyles, policy)
			}
			for _, want := range []string{"script-src-attr 'none'", "style-src 'self'"} {
				if !strings.Contains(policy, want) {
					t.Fatalf("CSP missing %q: %q", want, policy)
				}
			}
			if test.wantDynamicStyles {
				for _, want := range []string{"style-src-elem 'self' 'unsafe-inline'", "style-src-attr 'unsafe-inline'"} {
					if !strings.Contains(policy, want) {
						t.Fatalf("document CSP missing %q: %q", want, policy)
					}
				}
			}
		})
	}
}

func TestSecurityHeadersPreserveHandlerOwnedCSP(t *testing.T) {
	const policy = "default-src 'none'; frame-ancestors https://embed.example"
	handler := SecurityHeadersMiddleware(SecurityHeaders(false))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", policy)
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/public", nil))
	if got := response.Header().Get("Content-Security-Policy"); got != policy {
		t.Fatalf("Content-Security-Policy = %q, want handler policy %q", got, policy)
	}
}

func TestRequestCorrelationGeneratesAndPropagatesIdentity(t *testing.T) {
	var requestID, correlationID string
	handler := RequestCorrelation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID = r.Header.Get("X-Request-ID")
		correlationID = r.Header.Get("X-Correlation-ID")
		if !RequestIDWasGenerated(r) {
			t.Fatal("request ID was not marked as generated")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.HasPrefix(requestID, "req_") {
		t.Fatalf("request ID = %q, want generated req_ identity", requestID)
	}
	if correlationID != requestID {
		t.Fatalf("correlation ID = %q, want request ID %q", correlationID, requestID)
	}
	if got := response.Header().Get("X-Request-ID"); got != requestID {
		t.Fatalf("response request ID = %q, want %q", got, requestID)
	}
	if got := response.Header().Get("X-Correlation-ID"); got != correlationID {
		t.Fatalf("response correlation ID = %q, want %q", got, correlationID)
	}
}

func TestRequestCorrelationPreservesClientIdentity(t *testing.T) {
	handler := RequestCorrelation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestIDWasGenerated(r) {
			t.Fatal("client request ID was marked as generated")
		}
		if got := r.Header.Get("X-Request-ID"); got != "client-request" {
			t.Fatalf("request ID = %q, want client-request", got)
		}
		if got := r.Header.Get("X-Correlation-ID"); got != "client-correlation" {
			t.Fatalf("correlation ID = %q, want client-correlation", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "client-request")
	request.Header.Set("X-Correlation-ID", "client-correlation")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != "client-request" {
		t.Fatalf("response request ID = %q, want client-request", got)
	}
	if got := response.Header().Get("X-Correlation-ID"); got != "client-correlation" {
		t.Fatalf("response correlation ID = %q, want client-correlation", got)
	}
}

func TestRequestLoggerOmitsSensitiveHeadersAndValues(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := RequestCorrelation(RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})))
	request := httptest.NewRequest(http.MethodGet, "/?token=secret-query", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("X-Request-ID", "req_123")
	request.AddCookie(&http.Cookie{Name: "lv_session", Value: "secret-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	logged := logs.String()
	for _, want := range []string{"method=GET", "path=/", "status=200", "duration=", "bytes=2", "request_id=req_123", "correlation_id=req_123"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q missing %q", logged, want)
		}
	}
	for _, secret := range []string{"secret-token", "secret-session", "secret-query", "Authorization", "Cookie"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log %q contains sensitive value %q", logged, secret)
		}
	}
}
