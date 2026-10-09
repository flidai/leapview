package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestResponsesTruncationDoesNotExecuteOrReplayToolCalls(t *testing.T) {
	for _, transport := range []string{"json", "stream"} {
		t.Run(transport, func(t *testing.T) {
			var mu sync.Mutex
			var requests []json.RawMessage
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					t.Errorf("request path = %q, want /responses", r.URL.Path)
					http.Error(w, "unexpected path", http.StatusBadRequest)
					return
				}
				var request json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				turn := len(requests)
				mu.Unlock()

				// The incomplete call deliberately has valid JSON arguments. Tool
				// schema validation must not stand in for the truncation guard.
				var response string
				switch turn {
				case 1:
					response = `{"id":"first","status":"completed","output":[{"type":"function_call","id":"fc_saved","call_id":"saved","name":"record","arguments":"{}"}]}`
				case 2:
					response = `{"id":"cut_short","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"function_call","id":"fc_unfinished","call_id":"unfinished","name":"record","arguments":"{}"}]}`
				case 3:
					response = `{"id":"recovered","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Recovered"}]}]}`
				default:
					t.Errorf("unexpected provider request %d", turn)
					http.Error(w, "too many requests", http.StatusBadRequest)
					return
				}
				if transport == "json" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, response)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				terminal := "response.completed"
				if turn == 2 {
					terminal = "response.incomplete"
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"unfinished\",\"name\":\"record\",\"arguments\":\"\"}}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{}\"}\n\n")
				}
				_, _ = fmt.Fprintf(w, "data: {\"type\":%q,\"response\":%s}\n\n", terminal, response)
			}))
			defer server.Close()

			client := server.Client()
			client.Timeout = 5 * time.Second
			model := NewModel(agentapp.Config{APIKey: "test-key", BaseURL: server.URL, Model: "test-model", APIMode: "responses"}, client)
			var executed []string
			a, err := agentcore.New(agentcore.Definition{
				Name: "responses-truncation", SystemPrompt: "Use the recording tool, then finish.", Model: model,
				Limits: agentcore.Limits{MaxTruncationRetries: 1, MaxTurns: 4},
				Tools: []agentcore.ToolDefinition{{
					Name: "record", InputSchema: json.RawMessage(`{"type":"object"}`),
					Handler: agentcore.ToolHandlerFunc(func(_ context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
						executed = append(executed, call.ID)
						return agentcore.ToolResult{Content: map[string]any{"recorded": true}}, nil
					}),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			result, err := a.Prompt(ctx, agentcore.PromptRequest{Input: "Record once and finish"})
			if err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			if len(executed) != 1 || executed[0] != "saved" {
				t.Fatalf("executed tools = %v, want only the completed call saved", executed)
			}
			if result.StopReason != agentcore.StopReasonCompleted || result.FinalMessage.Content != "Recovered" || result.ToolCalls != 1 {
				t.Fatalf("recovered result = %+v", result)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 3 {
				t.Fatalf("provider requests = %d, want completed call, incomplete call, recovery", len(requests))
			}
			if !strings.Contains(string(requests[2]), "No tool calls from that cut-off response were executed") {
				t.Fatal("recovery request omitted truncation guidance")
			}
			var recovery struct {
				Input []struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
				} `json:"input"`
			}
			if err := json.Unmarshal(requests[2], &recovery); err != nil {
				t.Fatal(err)
			}
			var savedCalls, savedResults int
			for _, item := range recovery.Input {
				if item.CallID == "unfinished" {
					t.Fatalf("unfinished provider state or tool result replayed: %s", requests[2])
				}
				if item.CallID == "saved" {
					if item.Type == "function_call" {
						savedCalls++
					}
					if item.Type == "function_call_output" {
						savedResults++
					}
				}
			}
			if savedCalls != 1 || savedResults != 1 {
				t.Fatalf("recovery retained %d completed calls and %d results, want one of each", savedCalls, savedResults)
			}
			for _, message := range a.Transcript() {
				if strings.Contains(string(message.ProviderState), "unfinished") || message.ToolCallID == "unfinished" {
					t.Fatalf("unfinished provider state or tool result persisted: %+v", message)
				}
				for _, call := range message.ToolCalls {
					if call.ID == "unfinished" {
						t.Fatal("unfinished tool call persisted")
					}
				}
			}
		})
	}
}
