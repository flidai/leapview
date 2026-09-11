package stream

import (
	"reflect"
	"sync"

	"github.com/flidai/leapview/pkg/pagestream"
)

// Envelope carries dashboard refresh delivery policy beside a signal patch.
// Delivery metadata never enters the browser's signal graph.
type Envelope struct {
	Signals  pagestream.SignalPatch
	Delivery DeliveryMetadata
}

// DeliveryMetadata defines dashboard refresh ordering and coalescing.
// Generation zero means the message is not generation scoped.
type DeliveryMetadata struct {
	Generation    uint64
	Boundary      bool
	CoalesceGroup string
	MergeRoots    []string
}

// DeliveryBroker fans dashboard refresh envelopes out to subscribers while
// rejecting stale generations and coalescing explicitly compatible results.
type DeliveryBroker struct {
	mu           sync.Mutex
	pendingLimit int
	clients      map[string]map[*deliverySubscription]struct{}
}

type deliverySubscription struct {
	mu                sync.Mutex
	pending           []queuedEnvelope
	pendingLimit      int
	generation        uint64
	hasGeneration     bool
	nextID            uint64
	generationChanged chan struct{}
	closed            bool
	out               chan pagestream.SignalPatch
	wake              chan struct{}
	done              chan struct{}
	once              sync.Once
	detach            func()
}

type queuedEnvelope struct {
	id       uint64
	version  uint64
	changed  chan struct{}
	envelope Envelope
}

func NewDeliveryBroker() *DeliveryBroker {
	return NewDeliveryBrokerWithPendingLimit(256)
}

// NewDeliveryBrokerWithPendingLimit bounds each subscriber's pending
// envelopes. A slow subscriber is disconnected once this mailbox is full.
func NewDeliveryBrokerWithPendingLimit(pendingLimit int) *DeliveryBroker {
	if pendingLimit < 1 {
		panic("dashboard stream delivery pending limit must be positive")
	}
	return &DeliveryBroker{pendingLimit: pendingLimit, clients: map[string]map[*deliverySubscription]struct{}{}}
}

func (b *DeliveryBroker) Subscribe(streamID string) (<-chan pagestream.SignalPatch, func()) {
	return b.subscribe(streamID)
}

// SubscribeForPublication scopes a stream key by publication identity. Native
// delivery is node-local; this composite key prevents equal stream IDs from
// different publications sharing subscribers.
func (b *DeliveryBroker) SubscribeForPublication(publicationID, streamID string) (<-chan pagestream.SignalPatch, func()) {
	return b.Subscribe(publicationID + "\x00" + streamID)
}

func (b *DeliveryBroker) subscribe(streamID string) (<-chan pagestream.SignalPatch, func()) {
	subscription := &deliverySubscription{
		pendingLimit:      b.pendingLimit,
		out:               make(chan pagestream.SignalPatch),
		wake:              make(chan struct{}, 1),
		done:              make(chan struct{}),
		generationChanged: make(chan struct{}),
	}
	subscription.detach = func() {
		b.mu.Lock()
		if clients := b.clients[streamID]; clients != nil {
			delete(clients, subscription)
			if len(clients) == 0 {
				delete(b.clients, streamID)
			}
		}
		b.mu.Unlock()
	}

	b.mu.Lock()
	if b.clients[streamID] == nil {
		b.clients[streamID] = map[*deliverySubscription]struct{}{}
	}
	b.clients[streamID][subscription] = struct{}{}
	b.mu.Unlock()
	go subscription.forward()
	return subscription.out, subscription.disconnect
}

func (b *DeliveryBroker) PublishEnvelope(streamID string, envelope Envelope) {
	if b == nil || len(envelope.Signals) == 0 || streamID == "" {
		return
	}
	b.mu.Lock()
	subscriptions := make([]*deliverySubscription, 0, len(b.clients[streamID]))
	for subscription := range b.clients[streamID] {
		subscriptions = append(subscriptions, subscription)
	}
	b.mu.Unlock()

	for _, subscription := range subscriptions {
		subscription.enqueue(envelope)
	}
}

// PublishEnvelopeForPublication publishes to a publication-scoped stream on
// this process only. Cross-node routing is an affinity/deployment concern, not
// a PostgreSQL signal relay.
func (b *DeliveryBroker) PublishEnvelopeForPublication(publicationID, streamID string, envelope Envelope) {
	b.PublishEnvelope(publicationID+"\x00"+streamID, envelope)
}

func (s *deliverySubscription) enqueue(envelope Envelope) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	generation := envelope.Delivery.Generation
	if generation > 0 && s.hasGeneration && generation < s.generation {
		s.mu.Unlock()
		return
	}
	if generation > 0 && (!s.hasGeneration || generation > s.generation) {
		hadGeneration := s.hasGeneration
		s.generation = generation
		s.hasGeneration = true
		kept := s.pending[:0]
		for _, pending := range s.pending {
			// Generation-zero status envelopes are durable metadata (for
			// example lastUpdated), not stale result payloads.
			if pending.envelope.Delivery.Generation == 0 || pending.envelope.Delivery.Generation >= generation {
				kept = append(kept, pending)
			}
		}
		s.pending = kept
		if hadGeneration {
			close(s.generationChanged)
			s.generationChanged = make(chan struct{})
		}
	}

	next := cloneEnvelope(envelope)
	if len(s.pending) > 0 && shouldCoalesce(s.pending[len(s.pending)-1].envelope, next) {
		last := len(s.pending) - 1
		close(s.pending[last].changed)
		s.pending[last].changed = make(chan struct{})
		s.pending[last].envelope = coalesceEnvelopes(s.pending[last].envelope, next)
		s.pending[last].version++
	} else {
		if len(s.pending) >= s.pendingLimit {
			s.mu.Unlock()
			s.disconnect()
			return
		}
		s.nextID++
		s.pending = append(s.pending, queuedEnvelope{id: s.nextID, version: 1, changed: make(chan struct{}), envelope: next})
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func shouldCoalesce(current, next Envelope) bool {
	if current.Delivery.Boundary || next.Delivery.Boundary {
		return false
	}
	if current.Delivery.Generation != next.Delivery.Generation {
		return false
	}
	return current.Delivery.CoalesceGroup != "" && current.Delivery.CoalesceGroup == next.Delivery.CoalesceGroup
}

func coalesceEnvelopes(current, next Envelope) Envelope {
	mergeRoots := make(map[string]struct{}, len(current.Delivery.MergeRoots)+len(next.Delivery.MergeRoots))
	retainedRoots := make([]string, 0, len(current.Delivery.MergeRoots)+len(next.Delivery.MergeRoots))
	for _, roots := range [][]string{current.Delivery.MergeRoots, next.Delivery.MergeRoots} {
		for _, root := range roots {
			if _, exists := mergeRoots[root]; exists {
				continue
			}
			mergeRoots[root] = struct{}{}
			retainedRoots = append(retainedRoots, root)
		}
	}
	next.Signals = coalesceSignalPatches(current.Signals, next.Signals, mergeRoots)
	next.Delivery.MergeRoots = retainedRoots
	return next
}

func cloneEnvelope(envelope Envelope) Envelope {
	envelope.Signals = coalesceSignalPatches(nil, envelope.Signals, nil)
	envelope.Delivery.MergeRoots = append([]string(nil), envelope.Delivery.MergeRoots...)
	return envelope
}

func (s *deliverySubscription) forward() {
	defer close(s.out)
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			select {
			case <-s.done:
				return
			case <-s.wake:
			}
			continue
		}
		item := s.pending[0]
		envelope := item.envelope
		generationChanged := s.generationChanged
		itemChanged := item.changed
		s.mu.Unlock()
		if envelope.Delivery.Generation > 0 {
			select {
			case s.out <- envelope.Signals:
			case <-itemChanged:
				continue
			case <-s.done:
				return
			case <-generationChanged:
				continue
			}
		} else {
			select {
			case s.out <- envelope.Signals:
			case <-itemChanged:
				continue
			case <-s.done:
				return
			}
		}
		s.mu.Lock()
		if len(s.pending) > 0 && s.pending[0].id == item.id && s.pending[0].version == item.version {
			s.pending = s.pending[1:]
		}
		s.mu.Unlock()
	}
}

func (s *deliverySubscription) disconnect() {
	s.once.Do(func() {
		if s.detach != nil {
			s.detach()
		}
		s.close()
	})
}

func (s *deliverySubscription) close() {
	s.mu.Lock()
	s.closed = true
	s.pending = nil
	s.mu.Unlock()
	close(s.done)
}

func coalesceSignalPatches(current, next pagestream.SignalPatch, mergeRoots map[string]struct{}) pagestream.SignalPatch {
	result := make(pagestream.SignalPatch, len(current)+len(next))
	for key, value := range current {
		result[key] = value
	}
	for key, value := range next {
		if _, merge := mergeRoots[key]; merge {
			if combined, ok := mergeStringMaps(result[key], value); ok {
				result[key] = combined
				continue
			}
		}
		result[key] = value
	}
	return result
}

// mergeStringMaps preserves concrete generated signal map types.
func mergeStringMaps(current, next any) (any, bool) {
	nextValue := reflect.ValueOf(next)
	if !nextValue.IsValid() || nextValue.Kind() != reflect.Map || nextValue.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	currentValue := reflect.ValueOf(current)
	if current == nil {
		currentValue = reflect.MakeMap(nextValue.Type())
	}
	if !currentValue.IsValid() || currentValue.Kind() != reflect.Map || currentValue.Type() != nextValue.Type() {
		return nil, false
	}
	merged := reflect.MakeMapWithSize(nextValue.Type(), currentValue.Len()+nextValue.Len())
	iterator := currentValue.MapRange()
	for iterator.Next() {
		merged.SetMapIndex(iterator.Key(), iterator.Value())
	}
	iterator = nextValue.MapRange()
	for iterator.Next() {
		merged.SetMapIndex(iterator.Key(), iterator.Value())
	}
	return merged.Interface(), true
}
