package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
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

func TestAdminAgentToolsPreserveCanonicalEffectAndTags(t *testing.T) {
	tools := adminAgentToolDTOs([]agentcore.ToolDefinition{{
		Name: "edit_dashboard_source", Effect: "write", Tags: []string{"dashboard", "authoring", "source"},
	}}, nil)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	if tools[0].Effect != "write" {
		t.Fatalf("effect = %q, want write", tools[0].Effect)
	}
	if len(tools[0].Tags) != 3 || tools[0].Tags[0] != "dashboard" {
		t.Fatalf("tags = %#v", tools[0].Tags)
	}
}
