package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	agentapp "github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/platform/outbound"
)

func TestConnectionRequiresSuccessfulToolRoundTrip(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		failure string
		calls   int
	}{
		{name: "success", calls: 2},
		{name: "authentication failure", failure: "auth", calls: 1},
		{name: "model ignores tool", failure: "tool", calls: 1},
		{name: "incomplete final answer", failure: "final", calls: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("probe did not use the selected endpoint and credential")
				}
				var req openAIChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if req.Model != "selected-model" || !req.Stream || len(req.Tools) != 1 || req.Tools[0].Function.Name != "connection_check" {
					t.Error("probe must exercise the selected model with the synthetic streaming tool")
				}
				if scenario.failure == "auth" {
					http.Error(w, "invalid credential", http.StatusUnauthorized)
					return
				}
				choice := openAIChoice{Message: openAIMessage{Role: "assistant", Content: "OK"}, FinishReason: "stop"}
				if calls == 1 && scenario.failure != "tool" {
					choice.Message.Content = ""
					choice.Message.ToolCalls = []openAIToolCall{{ID: "check-1", Type: "function", Function: openAIFunctionCall{Name: "connection_check", Arguments: `{}`}}}
					choice.FinishReason = "tool_calls"
				}
				if calls == 2 {
					if len(req.Messages) != 3 || req.Messages[2].Role != "tool" || req.Messages[2].ToolCallID != "check-1" {
						t.Error("second request must contain the synthetic tool result")
					}
					if scenario.failure == "final" {
						choice.FinishReason = "length"
					}
				}
				writeJSON(t, w, openAIChatResponse{Choices: []openAIChoice{choice}})
			}))
			defer server.Close()
			model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "selected-model", APIMode: "chat-completions"}, server.Client())
			err := testConnection(t.Context(), model)
			if (err != nil) != (scenario.failure != "") {
				t.Fatalf("connection result = %v", err)
			}
			if calls != scenario.calls {
				t.Fatalf("provider calls = %d, want %d", calls, scenario.calls)
			}
		})
	}
}

func TestConnectionRejectsLoopbackDestination(t *testing.T) {
	err := TestConnection(t.Context(), agentapp.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1", Model: "selected-model", APIMode: "chat-completions"})
	if !errors.Is(err, outbound.ErrDestinationDenied) {
		t.Fatalf("connection probe error = %v, want outbound destination denial", err)
	}
}
