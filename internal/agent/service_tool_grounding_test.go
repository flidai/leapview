package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestServiceAgentEndToEndSendsPromptToolsAndGroundsAnswerInToolResult(t *testing.T) {
	ctx := context.Background()
	store := openAgentAppStore(t, ctx)
	defer store.Close()
	principal := createAgentAppPrincipal(t, ctx, store, "grounded-agent@example.com")
	model := newRecordingAgentModel(
		agentcore.ModelResponse{
			ToolCalls:    []agentcore.ToolCall{{ID: "call_catalog", Name: "catalog_lookup", Arguments: json.RawMessage(`{}`)}},
			FinishReason: agentcore.FinishReasonToolCalls,
		},
		agentcore.ModelResponse{Content: "The catalog tool reports the CFO dashboard.", FinishReason: agentcore.FinishReasonStop},
	)
	service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
	service.SetSystemPromptProvider(func(context.Context) (string, error) {
		return "Use only tool evidence; never claim dashboard data without a tool result.", nil
	})
	service.SetToolProviders(func(Scope) []agentcore.ToolDefinition {
		return []agentcore.ToolDefinition{{
			Name: "catalog_lookup", Description: "Look up dashboard metadata.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Handler: agentcore.ToolHandlerFunc(func(context.Context, agentcore.ToolCall) (agentcore.ToolResult, error) {
				return agentcore.ToolResult{Content: map[string]any{"dashboard": "CFO Command Center"}}, nil
			}),
		}}
	})
	scope := Scope{ProjectID: "project:finance", PrincipalID: principal.ID}
	conversation, err := service.CreateConversation(ctx, scope, "Grounded answer")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Prompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Which dashboard is available?"})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.Content != "The catalog tool reports the CFO dashboard." {
		t.Fatalf("answer = %q, want grounded fake-provider answer", result.Content)
	}
	requests := model.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want initial request plus tool-result follow-up", len(requests))
	}
	first := requests[0]
	if first.SystemPrompt == "" || !strings.Contains(first.SystemPrompt, "Use only tool evidence") {
		t.Fatalf("system prompt was not sent to provider: %#v", first)
	}
	if len(first.Messages) == 0 || first.Messages[0].Role != agentcore.RoleSystem || first.Messages[0].Content != first.SystemPrompt {
		t.Fatalf("system message missing from provider request: %#v", first.Messages)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "catalog_lookup" {
		t.Fatalf("tools were not registered with provider: %#v", first.Tools)
	}
	second := requests[1]
	if len(second.Messages) == 0 || second.Messages[len(second.Messages)-1].Role != agentcore.RoleTool || !strings.Contains(second.Messages[len(second.Messages)-1].Content, "CFO Command Center") {
		t.Fatalf("tool result was not returned to provider: %#v", second.Messages)
	}
}
