// Package deploymentevents composes the deployment mutation event port with
// the platform's durable PostgreSQL event authority. The adapter stays in app
// composition so deployment persistence does not import sibling storage.
package deploymentevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/google/uuid"
)

// Adapter is stateless and safe to share between deployment requests.
type Adapter struct {
	events *eventspostgres.Repository
}

var _ deploymentmodule.NativeDeliveryEventAppender = (*Adapter)(nil)

// New returns an adapter backed by a fresh platform event authority. It is
// retained for low-level tests; production composition should bind the exact
// application-owned repository with NewWithRepository.
func New() *Adapter { return NewWithRepository(eventspostgres.New()) }

// NewWithRepository keeps the event authority explicit for production
// composition and conformance tests.
func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	if events == nil {
		return &Adapter{}
	}
	return &Adapter{events: events}
}

// Matches proves this adapter retains the exact platform event repository
// supplied by application composition.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && events != nil && a.events == events
}

// AppendDeliveryEvent appends through the exact transaction supplied by the
// deployment authority and validates the complete immutable projection before
// returning. It never begins, commits, or rolls back tx.
func (a *Adapter) AppendDeliveryEvent(ctx context.Context, tx deploymentpostgres.Tx, input deploymentmodule.NativeDeliveryEventInput) (deploymentpostgres.Event, error) {
	if a == nil || a.events == nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event transaction is required", deploymentpostgres.ErrInvalid)
	}
	if err := canonicalUUIDv7(input.EventID); err != nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event id: %v", deploymentpostgres.ErrInvalid, err)
	}
	if err := canonicalUUIDv7(input.CorrelationID); err != nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event correlation id: %v", deploymentpostgres.ErrInvalid, err)
	}
	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType,
		AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion,
		CorrelationID: input.CorrelationID, Payload: append([]byte(nil), input.Payload...),
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event identity differs", deploymentpostgres.ErrConflict)
		}
		return deploymentpostgres.Event{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ScopeID ||
		stored.AggregateType != input.AggregateType || stored.AggregateID != input.AggregateID ||
		stored.EventType != input.EventType || stored.SchemaVersion != input.SchemaVersion ||
		stored.CorrelationID != input.CorrelationID || stored.AggregateVersion <= 0 {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event identity differs", deploymentpostgres.ErrConflict)
	}
	return deploymentpostgres.Event{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion,
		OccurredAt: stored.OccurredAt, CorrelationID: stored.CorrelationID,
		Payload: append([]byte(nil), stored.Payload...),
	}, nil
}

// GetDeliveryEvent reads and validates one exact durable delivery event. It
// is the post-commit counterpart to AppendDeliveryEvent and shares the
// caller-owned transaction supplied by the command completer.
func (a *Adapter) GetDeliveryEvent(ctx context.Context, tx deploymentpostgres.Tx, input deploymentmodule.NativeDeliveryEventInput) (deploymentpostgres.Event, error) {
	if a == nil || a.events == nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event adapter is not configured", deploymentpostgres.ErrInvalid)
	}
	if tx == nil {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event transaction is required", deploymentpostgres.ErrInvalid)
	}
	stored, err := a.events.GetEvent(ctx, tx, input.EventID)
	if err != nil {
		return deploymentpostgres.Event{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ScopeID ||
		stored.AggregateType != input.AggregateType || stored.AggregateID != input.AggregateID ||
		stored.EventType != input.EventType || stored.SchemaVersion != input.SchemaVersion ||
		stored.CorrelationID != input.CorrelationID || stored.AggregateVersion <= 0 ||
		!sameCanonical(stored.Payload, input.Payload) {
		return deploymentpostgres.Event{}, fmt.Errorf("%w: delivery event identity differs", deploymentpostgres.ErrConflict)
	}
	return deploymentpostgres.Event{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion,
		OccurredAt: stored.OccurredAt, CorrelationID: stored.CorrelationID,
		Payload: append([]byte(nil), stored.Payload...),
	}, nil
}

func canonicalUUIDv7(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("must be a canonical UUID")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return errors.New("must be a canonical UUID")
	}
	return nil
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
	return err == nil && bytes.Equal(la, ra)
}
