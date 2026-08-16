package module

import (
	"net/http"

	"github.com/flidai/leapview/internal/agent"
	agentui "github.com/flidai/leapview/internal/agent/ui"
)

type ChatSignal = agentui.ChatSignal
type ChatViewState = agentui.ChatViewState

func (m *Module) ChromeSignal(r *http.Request) ChatSignal {
	if m == nil {
		return ChatSignal{}
	}
	scope := agent.Scope{}
	if m.handler != nil {
		scope = m.handler.Scope(r)
	}
	return m.ChatSignalWith(r.Context(), scope, "", nil, agent.ChatArtifactSignals{}, "", false).Agent
}

func (m *Module) DashboardBootstrap(r *http.Request, projectID string) ChatViewState {
	if m == nil || m.handler == nil {
		return ChatViewState{}
	}
	return m.handler.DashboardBootstrap(r, projectID)
}
