package app

import (
	"context"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestUpdatesLoginBootstrapStreamsOnceWithoutAuthAndReturns(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=login", nil)
	rec := httptest.NewRecorder()
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		server.Routes().ServeHTTP(rec, req)
	}()

	select {
	case <-returned:
	case <-time.After(time.Second):
		cancel()
		<-returned
		t.Fatal("login bootstrap stream did not return after its initial patch")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: datastar-patch-signals") || !strings.Contains(body, `"status"`) {
		t.Fatalf("login noop updates did not stream a signal patch:\n%s", body)
	}
}

func TestUpdatesRejectsUnknownRoute(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/updates?route=missing", nil)
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestUpdatesRequiresRouteQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/updates?dashboard=executive-sales&datastar=%7B%22runtime%22%3A%7B%22kind%22%3A%22dashboard%22%7D%7D", nil)
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "updates route is required") {
		t.Fatalf("body = %q, want controlled missing route error", rec.Body.String())
	}
}

func TestLegacyUpdateRoutesAreNotRegistered(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	for _, path := range []string{
		"/data/updates",
		"/chat/updates",
		"/admin/storage/updates",
		"/admin/queries/updates",
		"/workspaces/test-workspace/updates",
		"/workspaces/test-workspace/assets/model_table:olist.orders/updates",
		"/workspaces/test-workspace/chat/updates",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		server.Routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d body=%s", path, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
}

func TestApplicationRefreshesSecureStreamCookieBehindHTTPSProxy(t *testing.T) {
	for _, options := range []assemblyConfig{{CookieSecure: true}, {PublicURL: "https://leapview.example"}} {
		application := assembleRuntime(fakeMetrics{}, options)
		backend := httptest.NewServer(application.Routes())
		target, err := url.Parse(backend.URL)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		proxy.Director = func(r *http.Request) { original(r); r.Header.Set("X-Forwarded-Proto", "http") }
		frontend := httptest.NewTLSServer(proxy)
		request, err := http.NewRequest(http.MethodGet, frontend.URL+"/updates?route=login", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(&http.Cookie{Name: webtransport.ClientIDCookieName, Value: "retained-client"})
		response, err := frontend.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		cookies := response.Cookies()
		response.Body.Close()
		frontend.Close()
		backend.Close()
		if response.StatusCode != http.StatusOK || len(cookies) != 1 || cookies[0].Name != webtransport.ClientIDCookieName || cookies[0].Value != "retained-client" || !cookies[0].Secure {
			t.Fatalf("configuration=%+v status=%d cookies=%+v", options, response.StatusCode, cookies)
		}
	}
}
