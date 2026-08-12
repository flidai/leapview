package uiaction

import (
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

var (
	CreateConversation = uicommand.Must("agent.conversation.create", agentgen.GenCommandOperationCreateAgentConversation())
	CreateRun          = uicommand.Must("agent.run.create", agentgen.GenCommandOperationCreateAgentRun())
)

func Bindings() []uicommand.Binding {
	return []uicommand.Binding{CreateConversation, CreateRun}
}
