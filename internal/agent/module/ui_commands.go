package module

import (
	agentuiaction "github.com/flidai/leapview/internal/agent/uiaction"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// UICommandBindings is the agent module's public browser command surface.
type UICommandBindings struct {
	CreateConversation uicommand.Binding
	CreateRun          uicommand.Binding
}

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		CreateConversation: agentuiaction.CreateConversation,
		CreateRun:          agentuiaction.CreateRun,
	}
}
