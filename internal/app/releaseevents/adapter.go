// Package releaseevents composes the Release-owned event port with the
// platform's PostgreSQL event authority. Release persistence depends only on
// its local event contract; app composition owns this sibling storage mapping.
package releaseevents

import (
	"context"
	"encoding/json"
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
func New() *Adapter { return &Adapter{events: eventspostgres.New()} }

// NewWithRepository binds the adapter to the exact platform event authority
// allocated by application composition.
func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// Matches proves this adapter retains the exact platform event repository
// supplied by application composition rather than a sibling allocation.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events != nil && a.events == events
}

// AppendEvent appends through the caller-owned release transaction and
// validates the immutable event identity before returning. Transaction
// ownership remains with Release.
func (a *Adapter) AppendEvent(ctx context.Context, tx releasepostgres.Tx, input releasepostgres.EventInput) (releasepostgres.Event, error) {
	if a == nil || a.events == nil {
		return releasepostgres.Event{}, fmt.Errorf("release event adapter is not configured")
	}
	if input.EventID != "" {
		if input.EventID != strings.TrimSpace(input.EventID) {
			return releasepostgres.Event{}, releasepostgres.ErrInvalid
		}
		if _, err := uuid.Parse(input.EventID); err != nil {
			return releasepostgres.Event{}, fmt.Errorf("%w: event id is not a UUID", releasepostgres.ErrInvalid)
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
		return releasepostgres.Event{}, err
	}
	if input.EventID != "" && stored.EventID != input.EventID ||
		stored.ScopeID != input.ScopeID || stored.AggregateType != input.AggregateType ||
		stored.AggregateID != input.AggregateID || stored.EventType != input.EventType ||
		stored.SchemaVersion != input.SchemaVersion || stored.CorrelationID != input.CorrelationID ||
		!sameCanonical(stored.Payload, input.Payload) {
		return releasepostgres.Event{}, fmt.Errorf("%w: release event identity differs", releasepostgres.ErrConflict)
	}
	return releasepostgres.Event{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion, OccurredAt: stored.OccurredAt,
		CorrelationID: stored.CorrelationID, Payload: append([]byte(nil), stored.Payload...),
	}, nil
}

func sameCanonical(left, right []byte) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	la, err := json.Marshal(a)
	if err != nil {
		return false
	}
	ra, err := json.Marshal(b)
	return err == nil && string(la) == string(ra)
}
