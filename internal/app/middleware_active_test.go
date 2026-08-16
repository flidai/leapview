package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func TestHealthRoutesRemainUnauthenticated(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	for _, path := range []string{"/healthz", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.Routes().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d body=%s", path, response.Code, http.StatusOK, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("%s Content-Type = %q, want application/json", path, got)
		}
	}
}

func TestMetricsRouteRequiresConfiguredBearerToken(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{
		MetricsBearerToken: "0123456789abcdef0123456789abcdef",
	}))
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("metrics without token status=%d challenge=%q, want 401 Bearer", response.Code, response.Header().Get("WWW-Authenticate"))
	}

	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("metrics with wrong token status=%d, want 401", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "leapview_http_requests_total") {
		t.Fatalf("metrics with valid token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMetricsRouteExportsHealthRequestMetrics(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	handler := server.Routes()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200 body=%s", response.Code, response.Body.String())
	}
	for _, want := range []string{"leapview_http_requests_total", `method="GET"`, `route="/healthz"`, `status="200"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("metrics output missing %q:\n%s", want, response.Body.String())
		}
	}
}
