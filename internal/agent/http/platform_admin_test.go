package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentConfigRequiresPlatformAdministrator(t *testing.T) {
	for _, test := range []struct {
		name       string
		admin      bool
		wantStatus int
	}{
		{name: "non-admin", wantStatus: http.StatusForbidden},
		{name: "admin", admin: true, wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(Options{
				CurrentPrincipal: func(*http.Request) (Principal, bool) {
					return Principal{ID: "principal-1"}, true
				},
				PlatformAdmin: func(context.Context, string) (bool, error) { return test.admin, nil },
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil)
			rec := httptest.NewRecorder()
			handler.GetAgentConfig(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantStatus)
			}
		})
	}
}
