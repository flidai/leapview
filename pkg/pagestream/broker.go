package pagestream

import (
	"errors"
	"sync"
)

// DefaultPendingLimit is the number of patches one subscription may queue
// before Broker disconnects it as a slow consumer.
const DefaultPendingLimit = 256

// ErrEmptyStreamID is returned when subscribing without a stream identity.
var ErrEmptyStreamID = errors.New("pagestream: stream ID is required")

// SignalPatch is a Datastar signal patch. Pagestream intentionally streams
// signal patches only; it does not transport element morphs or scripts.
type SignalPatch map[string]any

// Broker provides bounded in-process fan-out to every subscriber of a stream.
// Publish never blocks on a slow subscriber. When a subscriber's mailbox is
// full, Broker closes that subscription instead of silently dropping patches.
// Publish transfers ownership of accepted patches to the broker; callers must
// not mutate a patch after publishing it.
type Broker struct {
	mu           sync.Mutex
	pendingLimit int
	clients      map[string]map[*brokerSubscription]struct{}
}

type brokerSubscription struct {
	out    chan SignalPatch
	closed bool
}

func NewBroker() *Broker {
	return NewBrokerWithPendingLimit(DefaultPendingLimit)
}

// NewBrokerWithPendingLimit creates a broker with the given per-subscription
// mailbox capacity. It panics when pendingLimit is not positive.
func NewBrokerWithPendingLimit(pendingLimit int) *Broker {
	if pendingLimit < 1 {
		panic("pagestream: pending limit must be positive")
	}
	return &Broker{
		pendingLimit: pendingLimit,
		clients:      map[string]map[*brokerSubscription]struct{}{},
	}
}

// Subscribe creates a bounded mailbox for streamID. The returned unsubscribe
// function is idempotent. Broker also closes the mailbox if the subscriber
// cannot keep up with published patches.
func (b *Broker) Subscribe(streamID string) (<-chan SignalPatch, func(), error) {
	if streamID == "" {
		return nil, nil, ErrEmptyStreamID
	}
	subscription := &brokerSubscription{out: make(chan SignalPatch, b.pendingLimit)}

	b.mu.Lock()
	if b.clients[streamID] == nil {
		b.clients[streamID] = map[*brokerSubscription]struct{}{}
	}
	b.clients[streamID][subscription] = struct{}{}
	b.mu.Unlock()

	return subscription.out, func() {
		b.mu.Lock()
		b.removeLocked(streamID, subscription)
		b.mu.Unlock()
	}, nil
}

// Publish offers a patch to every current subscriber without blocking. A
// subscriber whose mailbox is full is disconnected; other subscribers continue
// independently.
func (b *Broker) Publish(streamID string, patch SignalPatch) {
	if b == nil || streamID == "" || len(patch) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscription := range b.clients[streamID] {
		select {
		case subscription.out <- patch:
		default:
			b.removeLocked(streamID, subscription)
		}
	}
}

func (b *Broker) removeLocked(streamID string, subscription *brokerSubscription) {
	if subscription.closed {
		return
	}
	subscription.closed = true
	delete(b.clients[streamID], subscription)
	if len(b.clients[streamID]) == 0 {
		delete(b.clients, streamID)
	}
	close(subscription.out)
}
