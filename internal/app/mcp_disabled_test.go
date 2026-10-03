package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
)

func TestMCPDisabledClosesTransportDiscoveryAndAuthorization(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "disabled-mcp-token"}),
	}))
	handler := server.Routes()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/mcp", ""},
		{http.MethodPost, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`},
		{http.MethodDelete, "/mcp", ""},
		{http.MethodGet, "/.well-known/oauth-protected-resource", ""},
		{http.MethodGet, "/.well-known/oauth-protected-resource/mcp", ""},
		{http.MethodGet, "/.well-known/oauth-authorization-server", ""},
		{http.MethodPost, "/oauth/register", ""},
		{http.MethodGet, "/oauth/authorize", ""},
		{http.MethodPost, "/oauth/authorize", ""},
		{http.MethodPost, "/oauth/token", "grant_type=authorization_code&code=disabled-code&resource=https%3A%2F%2Fleapview.example%2Fmcp"},
		{http.MethodPost, "/oauth/token", "grant_type=refresh_token&refresh_token=disabled-token"},
		{http.MethodPost, "/oauth/revoke", "token=disabled-token"},
	} {
		t.Run(tc.method+" "+tc.path+" "+tc.body, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer disabled-mcp-token")
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if tc.path == "/mcp" {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "application/json, text/event-stream")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
