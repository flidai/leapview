package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticationAndAuthenticatedBrowserResponsesArePrivate(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	handler := server.Routes()

	for _, path := range []string{"/login", "/", "/auth/azureadv2"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := response.Header().Get("Pragma"); got != "no-cache" {
				t.Fatalf("Pragma = %q, want no-cache", got)
			}
		})
	}
}
