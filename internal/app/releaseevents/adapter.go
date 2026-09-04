// Package releaseevents composes the Release-owned event port with the
// platform's PostgreSQL event authority. Release persistence depends only on
// its local event contract; app composition owns this sibling storage mapping.
package releaseevents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/google/uuid"
)

// Adapter is stateless and safe to share between release requests.
type Adapter struct {
	events *eventspostgres.Repository
}

var _ releasepostgres.EventAppender = (*Adapter)(nil)

// New returns an adapter backed by the platform's durable PostgreSQL event
// log.
func New() *Adapter { return NewWithRepository(eventspostgres.New()) }

// NewWithRepository binds the adapter to the exact platform event authority
// allocated by application composition.
func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// Matches proves this adapter retains the exact platform event repository
// supplied by application composition rather than a sibling allocation.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && events != nil && a.events == events
}

// AppendEvent appends through the caller-owned release transaction and
// validates the immutable event identity before returning. Transaction
// ownership remains with Release.
func (a *Adapter) AppendEvent(ctx context.Context, tx releasepostgres.Tx, input releasepostgres.EventInput) (releasepostgres.Event, error) {
	if a == nil || a.events == nil {
		return releasepostgres.Event{}, fmt.Errorf("release event adapter is not configured")
	}
	if input.EventID != "" {
		parsed, err := uuid.Parse(input.EventID)
		if input.EventID != strings.TrimSpace(input.EventID) || err != nil || parsed.String() != input.EventID || parsed.Version() != 7 {
			return releasepostgres.Event{}, fmt.Errorf("%w: release event id must be a canonical UUIDv7", releasepostgres.ErrInvalid)
		}
	}
	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType,
		AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion,
		CorrelationID: input.CorrelationID, Payload: input.Payload,
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return releasepostgres.Event{}, fmt.Errorf("%w: release event identity differs", releasepostgres.ErrConflict)
		}
		// Keep the release domain's invalid sentinel for malformed inputs.
		return releasepostgres.Event{}, err
	}
	return releasepostgres.Event{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion, OccurredAt: stored.OccurredAt,
		CorrelationID: stored.CorrelationID, Payload: append([]byte(nil), stored.Payload...),
	}, nil
}
