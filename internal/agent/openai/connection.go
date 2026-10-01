package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentapp "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type connectionStream struct{}

func (connectionStream) Delta(context.Context, string) error { return nil }

// TestConnection sends synthetic data only and never executes a product tool.
func TestConnection(ctx context.Context, config agentapp.Config) error {
	return testConnection(ctx, NewModel(config, nil))
}

func testConnection(ctx context.Context, model *OpenAIModel) error {
	req := agentcore.ModelRequest{Purpose: agentcore.ModelRequestPurposeTurn, Limits: agentcore.Limits{ReserveOutputTokens: 1024}, Messages: []agentcore.Message{{Role: agentcore.RoleUser, Content: "Call connection_check exactly once with no arguments. After its result, reply with OK."}}, Tools: []agentcore.ToolSpec{{Name: "connection_check", Description: "Harmless configuration connectivity check", InputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}}}
	response, err := model.Complete(ctx, req, connectionStream{})
	if err != nil {
		return err
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "connection_check" {
		return fmt.Errorf("provider did not perform the connection-check tool call")
	}
	call := response.ToolCalls[0]
	req.Messages = append(req.Messages, agentcore.Message{Role: agentcore.RoleAssistant, Content: response.Content, ToolCalls: response.ToolCalls, ProviderState: response.ProviderState}, agentcore.Message{Role: agentcore.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: `{"ok":true}`})
	response, err = model.Complete(ctx, req, connectionStream{})
	if err != nil {
		return err
	}
	if strings.TrimSpace(response.Content) == "" || len(response.ToolCalls) != 0 || response.FinishReason != agentcore.FinishReasonStop {
		return fmt.Errorf("provider did not complete the tool round-trip")
	}
	return nil
}
