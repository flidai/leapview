package module

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

func TestBuilderAuthoringCatalogIsEditOnlyAndBoundToOpenDraft(t *testing.T) {
	names := []string{
		agenttools.ListDashboardsToolName, agenttools.GetDashboardDraftToolName,
		agenttools.ReadDashboardSourceToolName, agenttools.EditDashboardSourceToolName,
		agenttools.AddDashboardPageToolName, agenttools.AddDashboardVisualToolName,
		agenttools.AssignDashboardFieldToolName, agenttools.PreviewDashboardDraftToolName,
		agenttools.CreateDashboardDraftToolName, agenttools.ForkDashboardToolName,
		agenttools.ExecuteDashboardCommandToolName, agenttools.SetDashboardVisibilityToolName,
	}
	definitions := make([]agentcore.ToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, agentcore.ToolDefinition{Name: name, Handler: agentcore.ToolHandlerFunc(func(context.Context, agentcore.ToolCall) (agentcore.ToolResult, error) {
			return agentcore.ToolResult{Content: "allowed"}, nil
		})})
	}
	chat := scopedBuilderAuthoringTools(definitions, agent.Scope{})
	if len(chat) != 1 || chat[0].Name != agenttools.ListDashboardsToolName {
		t.Fatalf("main chat exposed authoring tools: %v", toolNames(chat))
	}
	builder := scopedBuilderAuthoringTools(definitions, agent.Scope{BuilderDashboardID: "dashboard-1", BuilderDraftID: "draft-1"})
	if len(builder) != 8 {
		t.Fatalf("builder tool list = %v", toolNames(builder))
	}
	for _, definition := range builder {
		if definition.Name == agenttools.ListDashboardsToolName {
			continue
		}
		for _, test := range []struct {
			arguments string
			wantError bool
		}{
			{`{"dashboardId":"dashboard-2","draftId":"draft-1"}`, true},
			{`{"dashboardId":"dashboard-1","draftId":"draft-2"}`, true},
			{`{"dashboardId":"dashboard-1","draftId":"draft-1"}`, false},
		} {
			result, err := definition.Handler.Run(t.Context(), agentcore.ToolCall{Arguments: json.RawMessage(test.arguments)})
			if err != nil || result.IsError != test.wantError {
				t.Fatalf("%s(%s): result=%v err=%v", definition.Name, test.arguments, result, err)
			}
		}
	}
}

func toolNames(definitions []agentcore.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}
