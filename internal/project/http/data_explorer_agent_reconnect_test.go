package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/flidai/leapview/pkg/pagestream"
)

type explorerReconnectRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (r *explorerReconnectRecorder) Flush() {
	r.ResponseRecorder.Flush()
	// Header and defaults flushes occur before the analytical refresh.
	if strings.Contains(r.Body.String(), `"dataExplorer":`) {
		r.cancel()
	}
}

func TestDataExplorerAgentReconnectUsesNonDestructiveBootstrap(t *testing.T) {
	// Datastar reuses the signals captured when it opened the stream. A retry
	// can therefore carry no agent state even after the user started a chat.
	for _, capturedSignals := range []string{"{}", `{"agent":{"activeConversationId":"old-conversation"}}`} {
		t.Run(capturedSignals, func(t *testing.T) {
			h, _ := dataExplorerAgentBrowserFixture(t)
			h.AgentBootstrap = func(*http.Request) projectui.DataExplorerAgentBootstrap {
				return projectui.DataExplorerAgentBootstrap{
					Agent: map[string]any{
						"activeConversationId": "", "transcript": []any{}, "conversations": []any{},
						"status":   map[string]any{"enabled": true, "running": false},
						"composer": map[string]any{"value": "", "disabled": false, "placeholder": "Ask about these data"},
					},
					Visuals: map[string]any{},
					Refresh: map[string]any{
						"conversations": []any{map[string]any{"id": "updated-conversation", "title": "Updated title"}},
						"status":        map[string]any{"enabled": true},
					},
				}
			}
			subscribed, released := false, false
			h.AgentSubscribe = func(_ *http.Request, clientID string) (<-chan pagestream.SignalPatch, func(), error) {
				subscribed = clientID == "explorer-browser"
				return nil, func() { released = true }, nil
			}
			values := url.Values{
				"route": {"data"}, "surface": {"explore"}, "mode": {"explore"},
				"semanticModel": {"semantic:sales"}, "dataset": {"orders"},
				"dimension": {"orders.status"}, "datastar": {capturedSignals},
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			response := &explorerReconnectRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?"+values.Encode(), nil)
			request.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "explorer-browser"})
			h.Updates(response, request)
			if !subscribed || !released {
				t.Fatalf("reconnect subscription/release = %t/%t", subscribed, released)
			}

			var defaults, refresh map[string]any
			for _, event := range ssetest.Events(t, response.Body.String()) {
				patch, ok, err := ssetest.DecodePatchSignalEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					continue
				}
				if strings.Contains(event.Data, "onlyIfMissing true") {
					defaults = patch
				} else if patch["dataExplorer"] != nil {
					refresh = patch
				}
			}
			if defaults == nil || refresh == nil {
				t.Fatalf("reconnect defaults-only chat frame = %t, normal analytical refresh = %t", defaults != nil, refresh != nil)
			}
			for _, key := range []string{"agent", "agentVisuals", "agentReferenceSearch"} {
				if _, exists := defaults[key]; !exists {
					t.Errorf("first connection lacks %s defaults", key)
				}
			}
			contextDefaults, ok := defaults["agentContext"].(map[string]any)
			if !ok || contextDefaults["references"] == nil {
				t.Fatalf("attached reference defaults missing: %#v", defaults["agentContext"])
			}
			for _, key := range []string{"agentVisuals", "agentReferenceSearch"} {
				if _, exists := refresh[key]; exists {
					t.Errorf("normal refresh can overwrite %s", key)
				}
			}
			agentRefresh, ok := refresh["agent"].(map[string]any)
			if !ok {
				t.Fatalf("reconnect lost conversation/availability refresh: %#v", refresh["agent"])
			}
			if conversations, ok := agentRefresh["conversations"].([]any); !ok || len(conversations) != 1 || conversations[0].(map[string]any)["title"] != "Updated title" {
				t.Errorf("reconnect conversation list = %#v", agentRefresh["conversations"])
			}
			if status, ok := agentRefresh["status"].(map[string]any); !ok || status["enabled"] != true {
				t.Errorf("reconnect availability = %#v", agentRefresh["status"])
			}
			for _, key := range []string{"activeConversationId", "transcript", "composer"} {
				if _, exists := agentRefresh[key]; exists {
					t.Errorf("normal refresh can overwrite agent.%s", key)
				}
			}
			if status, ok := agentRefresh["status"].(map[string]any); ok {
				for _, key := range []string{"running", "runId", "error", "canContinue"} {
					if _, exists := status[key]; exists {
						t.Errorf("normal refresh can overwrite agent.status.%s", key)
					}
				}
			}
			contextRefresh, ok := refresh["agentContext"].(map[string]any)
			if !ok || contextRefresh["surface"] != "data" || contextRefresh["modelId"] != "semantic:sales" || contextRefresh["datasetId"] != "orders" || contextRefresh["exploration"] == nil {
				t.Fatalf("reconnect lost refreshed governed context: %#v", refresh["agentContext"])
			}
			if _, exists := contextRefresh["references"]; exists {
				t.Error("normal refresh can clear attached references")
			}
			defaultAgent, ok := defaults["agent"].(map[string]any)
			if !ok || defaultAgent["activeConversationId"] != "" {
				t.Fatalf("fresh browser conversation defaults = %#v", defaults["agent"])
			}
			if transcript, ok := defaultAgent["transcript"].([]any); !ok || len(transcript) != 0 {
				t.Errorf("fresh browser transcript defaults = %#v", defaultAgent["transcript"])
			}
			if composer, ok := defaultAgent["composer"].(map[string]any); !ok || composer["value"] != "" {
				t.Errorf("fresh browser composer defaults = %#v", defaultAgent["composer"])
			}
		})
	}
}
