package module

import (
	"context"
	"testing"

	agentcap "github.com/flidai/leapview/internal/agent"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

type toolContextTestKey struct{}

func TestWrapToolContextAppliesScopeToEveryHandler(t *testing.T) {
	definitions := []agentcore.ToolDefinition{{
		Name: "one",
		Handler: agentcore.ToolHandlerFunc(func(ctx context.Context, _ agentcore.ToolCall) (agentcore.ToolResult, error) {
			return agentcore.ToolResult{Content: map[string]any{"principal": ctx.Value(toolContextTestKey{})}}, nil
		}),
	}}
	scope := agentcap.Scope{PrincipalID: "principal-1", ProjectID: "project:active", DevAuthBypass: true}
	wrapped := wrapToolContext(definitions, func(ctx context.Context, got agentcap.Scope) context.Context {
		if got.PrincipalID != scope.PrincipalID || got.ProjectID != scope.ProjectID || got.DevAuthBypass != scope.DevAuthBypass {
			t.Fatalf("decorator scope = %#v, want %#v", got, scope)
		}
		return context.WithValue(ctx, toolContextTestKey{}, got.PrincipalID)
	}, scope)
	result, err := wrapped[0].Handler.Run(t.Context(), agentcore.ToolCall{Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content.(map[string]any)["principal"] != "principal-1" {
		t.Fatalf("tool result = %#v", result.Content)
	}
}
