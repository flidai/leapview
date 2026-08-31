// Package releaseevents composes the Release-owned event port with the
// platform's PostgreSQL event authority. Release persistence depends only on
// its local event contract; app composition owns this sibling storage mapping.
package releaseevents

import (
	"context"
	"errors"
	"fmt"

	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	eventwatermill "github.com/flidai/leapview/internal/platform/events/watermill"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
)

// Adapter is stateless and safe to share between release requests.
type Adapter struct {
	events *eventwatermill.Adapter
}

var _ releasepostgres.EventAppender = (*Adapter)(nil)

// New returns an adapter backed by the platform's durable PostgreSQL event
// log.
func New() *Adapter { return NewWithRepository(eventspostgres.New()) }

// NewWithRepository binds the adapter to the exact platform event authority
// allocated by application composition.
func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	watermillAdapter, err := eventwatermill.New(events)
	if err != nil {
		return &Adapter{}
	}
	return &Adapter{events: watermillAdapter}
}

// Matches proves this adapter retains the exact platform event repository
// supplied by application composition rather than a sibling allocation.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events.Matches(events)
}

// AppendEvent appends through the caller-owned release transaction and
// validates the immutable event identity before returning. Transaction
// ownership remains with Release.
func (a *Adapter) AppendEvent(ctx context.Context, tx releasepostgres.Tx, input releasepostgres.EventInput) (releasepostgres.Event, error) {
	if a == nil || a.events == nil {
		return releasepostgres.Event{}, fmt.Errorf("release event adapter is not configured")
	}
	stored, err := a.events.AppendEvent(ctx, tx, eventwatermill.TopicRelease, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType,
		AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion,
		CorrelationID: input.CorrelationID, Payload: input.Payload,
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return releasepostgres.Event{}, fmt.Errorf("%w: release event identity differs", releasepostgres.ErrConflict)
		}
		// Keep the release domain's invalid sentinel while delegating strict
		// identity/payload validation to the shared event boundary.
		if errors.Is(err, eventwatermill.ErrInvalid) {
			return releasepostgres.Event{}, fmt.Errorf("%w: %v", releasepostgres.ErrInvalid, err)
		}
		return releasepostgres.Event{}, err
	}
	return releasepostgres.Event{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion, OccurredAt: stored.OccurredAt,
		CorrelationID: stored.CorrelationID, Payload: append([]byte(nil), stored.Payload...),
	}, nil
}
