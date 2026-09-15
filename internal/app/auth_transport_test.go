package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticationAndAuthenticatedBrowserResponsesArePrivate(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	handler := server.Routes()

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/login"},
		{method: http.MethodGet, path: "/"},
		{method: http.MethodGet, path: "/auth/azureadv2"},
		{method: http.MethodPost, path: "/auth/switch-account"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := response.Header().Get("Pragma"); got != "no-cache" {
				t.Fatalf("Pragma = %q, want no-cache", got)
			}
		})
	}
}
