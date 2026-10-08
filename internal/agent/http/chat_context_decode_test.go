package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
	"github.com/flidai/leapview/pkg/pagestream"
)

func TestChatCommandsDecodeBrowserContext(t *testing.T) {
	for _, surface := range []string{"chat", "builder", "dashboard_builder", "dashboard", "data"} {
		t.Run(surface, func(t *testing.T) {
			context := ui.AgentContextSignal{
				Surface: surface, DashboardID: "dashboard_sales", PageID: "details",
				ReferenceLimit: agent.MaxTurnReferences,
				References: []ui.AgentReferenceSignal{{
					Reference: ui.AgentReferenceKeySignal{Kind: "dashboard", ID: "dashboard_sales"},
					Name:      "Sales", Hierarchy: []string{}, Locations: []ui.AgentReferenceLocationSignal{}, Context: []string{},
				}},
			}
			payload, err := json.Marshal(map[string]any{
				"agentContext":         context,
				"agent":                ui.ChatSignal{ActiveConversationID: "conversation_1", Composer: ui.ComposerSignal{Value: "Explain this chart"}},
				"agentReferenceSearch": ui.AgentReferenceSearchSignal{Query: "sales", RequestID: 1},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range []struct {
				name   string
				method string
				path   string
				target any
			}{
				{name: "turn", method: http.MethodPost, path: "/chats/turns", target: &chatTurnCommandSignals{}},
				{name: "stop", method: http.MethodPost, path: "/chats/stop", target: &chatStopCommandSignals{}},
				{name: "reference search", method: http.MethodGet, path: "/chats/references/search", target: &chatReferenceSearchSignals{}},
			} {
				t.Run(command.name, func(t *testing.T) {
					request := httptest.NewRequest(command.method, command.path, strings.NewReader(string(payload)))
					if command.method == http.MethodGet {
						request.URL.RawQuery = url.Values{"datastar": {string(payload)}}.Encode()
					}
					if err := pagestream.ReadSignals(request, command.target); err != nil {
						t.Fatalf("browser context rejected: %v", err)
					}
					var decoded agent.TurnContext
					switch target := command.target.(type) {
					case *chatTurnCommandSignals:
						decoded = target.AgentContext
					case *chatStopCommandSignals:
						decoded = target.AgentContext
					case *chatReferenceSearchSignals:
						decoded = target.AgentContext
					}
					if decoded.Surface != surface || decoded.PageID != "details" || len(decoded.References) != 1 || decoded.References[0].Reference.ID != "dashboard_sales" {
						t.Fatalf("decoded context = %#v", decoded)
					}
				})
			}
		})
	}
}
