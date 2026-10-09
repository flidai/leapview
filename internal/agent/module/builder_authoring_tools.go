package module

import (
	"context"
	"encoding/json"
	"strings"

	agentcap "github.com/flidai/leapview/internal/agent"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	agentcore "github.com/flidai/leapview/pkg/agent"
)

// Main chat can author private drafts through the same authorized application
// facade. Builder edits remain bound to its server-resolved open draft; neither
// surface exposes lifecycle commands or publication.
func scopedBuilderAuthoringTools(definitions []agentcore.ToolDefinition, scope agentcap.Scope) []agentcore.ToolDefinition {
	mainChat := scope.MainChatAuthoring && strings.TrimSpace(scope.PrincipalID) != "" && !scope.Credential.Restricted && scope.Credential.PermissionProfile == "" && scope.Credential.Permissions == nil && scope.BuilderDashboardID == "" && scope.BuilderDraftID == ""
	selected := definitions[:0:0]
	for _, definition := range definitions {
		switch definition.Name {
		case agenttools.CreateDashboardDraftToolName:
			if !mainChat {
				continue
			}
		case agenttools.ForkDashboardToolName,
			agenttools.ExecuteDashboardCommandToolName, agenttools.SetDashboardVisibilityToolName:
			continue
		case agenttools.GetDashboardDraftToolName, agenttools.ReadDashboardSourceToolName,
			agenttools.EditDashboardSourceToolName, agenttools.AddDashboardPageToolName,
			agenttools.AddDashboardVisualToolName, agenttools.AssignDashboardFieldToolName,
			agenttools.PreviewDashboardDraftToolName:
			if mainChat {
				break
			}
			if scope.BuilderDashboardID == "" || scope.BuilderDraftID == "" {
				continue
			}
			definition.Handler = scopedBuilderHandler(definition.Handler, scope)
		}
		selected = append(selected, definition)
	}
	return selected
}

func scopedBuilderHandler(handler agentcore.ToolHandler, scope agentcap.Scope) agentcore.ToolHandler {
	return agentcore.ToolHandlerFunc(func(ctx context.Context, call agentcore.ToolCall) (agentcore.ToolResult, error) {
		var arguments struct {
			DashboardID string `json:"dashboardId"`
			DraftID     string `json:"draftId"`
		}
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
			return agenttools.ToolError("invalid_arguments", "dashboard tool arguments must be JSON"), nil
		}
		if strings.TrimSpace(arguments.DashboardID) != scope.BuilderDashboardID ||
			(arguments.DraftID != "" && strings.TrimSpace(arguments.DraftID) != scope.BuilderDraftID) {
			return agenttools.ToolError("scope_mismatch", "agent can edit only the dashboard draft open in Builder"), nil
		}
		return handler.Run(ctx, call)
	})
}
