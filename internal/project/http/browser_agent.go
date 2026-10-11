package http

import (
	stdhttp "net/http"

	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectui "github.com/flidai/leapview/internal/project/ui"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

// DataExplorerAgentBootstrap and DataExplorerAgentCommandBindings are the neutral
// agent projections accepted by the project's browser composition surface.
type DataExplorerAgentBootstrap = projectui.DataExplorerAgentBootstrap
type DataExplorerAgentCommandBindings = projectui.DataExplorerAgentCommandBindings

func (h *BrowserHandler) dataExplorerAgentBootstrap(r *stdhttp.Request) projectui.DataExplorerAgentBootstrap {
	var bootstrap projectui.DataExplorerAgentBootstrap
	if h.AgentBootstrap != nil {
		bootstrap = h.AgentBootstrap(r)
	}
	if bootstrap.Agent == nil && bootstrap.Refresh == nil {
		bootstrap.Refresh = map[string]any{
			"conversations": []any{}, "status": map[string]any{"enabled": false},
		}
	}
	return bootstrap
}

// Split tab-owned chat defaults from the authoritative Explorer refresh.
// Reconnects retain selection, draft, run controls, visuals and references.
func dataExplorerAgentBootstrapDefaults(patch map[string]any, agent DataExplorerAgentBootstrap, context projectsignals.AgentContextSignal) pagestream.SignalPatch {
	defaults := pagestream.SignalPatch{}
	for _, key := range []string{"agent", "agentVisuals", "agentReferenceSearch"} {
		defaults[key] = patch[key]
		delete(patch, key)
	}
	defaults["agentContext"] = map[string]any{"references": context.References}
	patch["agentContext"] = projectui.DataExplorerAgentContextRefreshPayload(context)
	if agent.Refresh != nil {
		patch["agent"] = agent.Refresh
	}
	return defaults
}

type dataExplorerAgentSubscription struct {
	clientID    string
	updates     <-chan pagestream.SignalPatch
	unsubscribe func()
}

// Embedded turns stream their transcript on the command response. The canonical
// stream also carries conversation/title changes for this browser and principal.
func (h *BrowserHandler) subscribeDataExplorerAgent(w stdhttp.ResponseWriter, r *stdhttp.Request) (dataExplorerAgentSubscription, bool) {
	subscription := dataExplorerAgentSubscription{}
	if uitransport.Route(r) != "data" || r.URL.Query().Get("surface") != "explore" || h.AgentSubscribe == nil {
		return subscription, true
	}
	var err error
	subscription.clientID, err = h.ClientIDs.Ensure(w, r)
	if err != nil {
		stdhttp.Error(w, "page-stream client identity is unavailable", stdhttp.StatusServiceUnavailable)
		return subscription, false
	}
	subscription.updates, subscription.unsubscribe, err = h.AgentSubscribe(r, subscription.clientID)
	if err != nil {
		stdhttp.Error(w, "agent updates are unavailable", stdhttp.StatusServiceUnavailable)
		return subscription, false
	}
	return subscription, true
}
