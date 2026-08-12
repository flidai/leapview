package module

import (
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// UICommandBindings is the agent module's public browser command surface.
type UICommandBindings struct {
	CreateConversation uicommand.Binding
	CreateRun          uicommand.Binding
}

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		CreateConversation: agentgen.GenUIActionCreateAgentConversation(),
		CreateRun:          agentgen.GenUIActionCreateAgentRun(),
	}
}
