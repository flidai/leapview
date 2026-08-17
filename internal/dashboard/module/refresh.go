package module

import (
	"github.com/Yacobolo/toolbelt/pagestream"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// PublishSemanticModelRefresh asks every active dashboard bound to the model to
// refresh and publishes the durable refresh timestamp to its page stream.
func (m *Module) PublishSemanticModelRefresh(projectID projectgraph.ResourceID, environment, modelID, refreshedAt string) {
	if m == nil || m.coordinators == nil {
		return
	}
	for _, streamID := range m.coordinators.RefreshSemanticModel(projectID, environment, modelID) {
		if m.handler.Broker != nil {
			m.handler.Broker.PublishEnvelope(streamID, pagestream.Envelope{
				Signals: pagestream.SignalPatch{"status": map[string]any{"lastUpdated": refreshedAt}},
			})
		}
	}
}
