package module

import (
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// UICommandBindings is the agent module's public browser command surface.
type UICommandBindings struct {
	UpdateConfig        uicommand.Binding
	CreateConversation  uicommand.Binding
	ManageConversations uicommand.Binding
	CreateRun           uicommand.Binding
	CancelRun           uicommand.Binding
}

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		UpdateConfig:        agentgen.GenUIActionUpdateAgentConfig(),
		CreateConversation:  agentgen.GenUIActionCreateAgentConversation(),
		ManageConversations: agentgen.GenUIActionManageAgentConversations(),
		CreateRun:           agentgen.GenUIActionCreateAgentRun(),
		CancelRun:           agentgen.GenUIActionCancelAgentRun(),
	}
}
