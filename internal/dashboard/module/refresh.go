package module

import (
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
)

// PublishSemanticModelRefresh asks every active dashboard bound to the model to
// refresh and publishes the durable refresh timestamp to its page stream.
func (m *Module) PublishSemanticModelRefresh(projectID projectgraph.ResourceID, environment, modelID, refreshedAt string) {
	if m == nil || m.coordinators == nil {
		return
	}
	for _, target := range m.coordinators.RefreshSemanticModelTargets(projectID, environment, modelID) {
		broker := m.handler.Broker
		if target.Publication != "" && m.publicBroker != nil {
			broker = scopedPublicationBroker(m.publicBroker, target.Publication)
		}
		if broker != nil {
			broker.PublishEnvelope(target.StreamID, dashboardstream.Envelope{
				Signals:  pagestream.SignalPatch{"status": map[string]any{"lastUpdated": refreshedAt}},
				Delivery: dashboardstream.DeliveryMetadata{Boundary: true},
			})
		}
	}
}
