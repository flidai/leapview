package http

import (
	stdhttp "net/http"

	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/flidai/leapview/pkg/pagestream"
)

// DataExplorerAgentBootstrap and DataExplorerAgentCommandBindings are the neutral
// agent projections accepted by the project's browser composition surface.
type DataExplorerAgentBootstrap = projectui.DataExplorerAgentBootstrap
type DataExplorerAgentCommandBindings = projectui.DataExplorerAgentCommandBindings

func (h *BrowserHandler) dataExplorerAgentBootstrap(r *stdhttp.Request) projectui.DataExplorerAgentBootstrap {
	if h.AgentBootstrap == nil {
		return projectui.DataExplorerAgentBootstrap{}
	}
	return h.AgentBootstrap(r)
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
