package stream

import (
	"reflect"
	"sync"
	"time"

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
	mu      sync.Mutex
	clients map[string]map[*deliverySubscription]struct{}
}

type deliverySubscription struct {
	mu            sync.Mutex
	pending       []Envelope
	generation    uint64
	hasGeneration bool
	closed        bool
	out           chan pagestream.SignalPatch
	wake          chan struct{}
	done          chan struct{}
	once          sync.Once
}

func NewDeliveryBroker() *DeliveryBroker {
	return &DeliveryBroker{clients: map[string]map[*deliverySubscription]struct{}{}}
}

func (b *DeliveryBroker) Subscribe(streamID string) (<-chan pagestream.SignalPatch, func()) {
	subscription := &deliverySubscription{
		out:  make(chan pagestream.SignalPatch, 1),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}

	b.mu.Lock()
	if b.clients[streamID] == nil {
		b.clients[streamID] = map[*deliverySubscription]struct{}{}
	}
	b.clients[streamID][subscription] = struct{}{}
	b.mu.Unlock()

	go subscription.forward()
	return subscription.out, func() {
		subscription.once.Do(func() {
			b.mu.Lock()
			delete(b.clients[streamID], subscription)
			if len(b.clients[streamID]) == 0 {
				delete(b.clients, streamID)
			}
			b.mu.Unlock()
			subscription.close()
		})
	}
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
		s.generation = generation
		s.hasGeneration = true
		kept := s.pending[:0]
		for _, pending := range s.pending {
			if pending.Delivery.Generation >= generation {
				kept = append(kept, pending)
			}
		}
		s.pending = kept
		select {
		case <-s.out:
		default:
		}
	}

	next := cloneEnvelope(envelope)
	if len(s.pending) > 0 && shouldCoalesce(s.pending[len(s.pending)-1], next) {
		last := len(s.pending) - 1
		s.pending[last] = coalesceEnvelopes(s.pending[last], next)
	} else {
		// Boundaries are lossless. Coalescible result bursts remain bounded by
		// collapsing into the preceding compatible envelope.
		s.pending = append(s.pending, next)
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
		pending := len(s.pending) > 0
		sent := false
		if pending {
			envelope := s.pending[0]
			select {
			case s.out <- envelope.Signals:
				s.pending = s.pending[1:]
				sent = true
			default:
			}
		}
		s.mu.Unlock()
		if sent {
			continue
		}
		if !pending {
			select {
			case <-s.done:
				return
			case <-s.wake:
			}
			continue
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-s.done:
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-s.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
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
