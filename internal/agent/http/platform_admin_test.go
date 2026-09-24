package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
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

func TestAgentConfigReportsRuntimeStateWithoutSecrets(t *testing.T) {
	service := agent.NewService(nil, agent.Config{APIKey: "do-not-expose", BaseURL: "https://provider.example/v1", Model: "model"})
	service.ConfigureDefaultModel(func(agent.Config) agentcore.Model {
		return agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
			return agentcore.ModelResponse{}, nil
		})
	})
	handler := NewHandler(Options{
		Service: service,
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
	})

	get := httptest.NewRecorder()
	handler.GetAgentConfig(get, httptest.NewRequest(http.MethodGet, "/api/v1/agent/config", nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatalf("get status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}

	body := get.Body.String()
	if !strings.Contains(body, `"configured":true`) || !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"status":"enabled"`) {
		t.Fatalf("runtime status body=%s", body)
	}
	if strings.Contains(body, "do-not-expose") || strings.Contains(body, "provider.example") {
		t.Fatalf("runtime response exposed deployment secrets: %s", body)
	}

	manualToggle := httptest.NewRequest(http.MethodPatch, "/api/v1/agent/config", strings.NewReader(`{"enabled":false}`))
	manualToggle.Header.Set("Content-Type", "application/json")
	manualToggle.Header.Set("If-Match", get.Header().Get("ETag"))
	toggleResponse := httptest.NewRecorder()
	handler.UpdateAgentConfig(toggleResponse, manualToggle)
	if toggleResponse.Code != http.StatusUnprocessableEntity || !service.Enabled() {
		t.Fatalf("deployment-managed enablement accepted browser mutation: status=%d body=%s", toggleResponse.Code, toggleResponse.Body.String())
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
