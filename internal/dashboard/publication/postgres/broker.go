package postgres

import (
	"log/slog"

	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	"github.com/flidai/leapview/pkg/pagestream"
)

// Broker is the native publication-scoped, node-local delivery broker. Signal
// envelopes intentionally never enter PostgreSQL; durable stream registration
// and filter CAS remain owned by StreamRegistry.
type Broker struct {
	local  *dashboardstream.DeliveryBroker
	logger *slog.Logger
}

func NewBroker(logger *slog.Logger) *Broker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Broker{local: dashboardstream.NewDeliveryBroker(), logger: logger}
}

// IsNative marks the broker as the PostgreSQL-capability's scoped local
// implementation for native persistence validation.
func (b *Broker) IsNative() bool { return b != nil && b.local != nil }

// Configured reports whether local delivery is ready for use.
func (b *Broker) Configured() bool { return b.IsNative() }

func (b *Broker) Subscribe(streamID string) (<-chan pagestream.SignalPatch, func()) {
	return nil, func() {}
}

func (b *Broker) SubscribeForPublication(publicationID, streamID string) (<-chan pagestream.SignalPatch, func()) {
	if b == nil || b.local == nil {
		return nil, func() {}
	}
	return b.local.SubscribeForPublication(publicationID, streamID)
}

func (b *Broker) PublishEnvelope(streamID string, envelope dashboardstream.Envelope) {
	// Native delivery must always include a publication identity. The legacy
	// unscoped method is intentionally a no-op to prevent cross-publication
	// stream-ID collisions when a caller bypasses the scoped adapter.
}

func (b *Broker) PublishEnvelopeForPublication(publicationID, streamID string, envelope dashboardstream.Envelope) {
	if b == nil || b.local == nil {
		return
	}
	b.local.PublishEnvelopeForPublication(publicationID, streamID, envelope)
}
