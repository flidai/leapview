// Package dashboardauthoringevents adapts dashboard-authoring domain events
// to the platform's canonical PostgreSQL event log.
package dashboardauthoringevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/google/uuid"
)

type Adapter struct{ events *eventspostgres.Repository }

var _ authoringpostgres.EventPort = (*Adapter)(nil)

func New() *Adapter { return &Adapter{events: eventspostgres.New()} }

func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// Matches proves this adapter is bound to the exact platform event authority
// allocated by application composition.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events != nil && a.events == events
}

// AppendEvent appends through the exact caller-owned transaction and validates
// every immutable event field before returning to the authoring repository.
func (a *Adapter) AppendEvent(ctx context.Context, tx authoringpostgres.Tx, input authoringpostgres.EventInput) (authoringpostgres.Event, error) {
	if a == nil || a.events == nil {
		return authoringpostgres.Event{}, errors.New("dashboard authoring event adapter is not configured")
	}
	if tx == nil {
		return authoringpostgres.Event{}, errors.New("dashboard authoring event transaction is required")
	}
	if err := canonicalUUIDv7(input.EventID); err != nil {
		return authoringpostgres.Event{}, fmt.Errorf("%w: authoring event id: %v", authoring.ErrConflict, err)
	}
	if input.CorrelationID != "" {
		if err := canonicalUUIDv7(input.CorrelationID); err != nil {
			return authoringpostgres.Event{}, fmt.Errorf("%w: authoring correlation id: %v", authoring.ErrConflict, err)
		}
	}
	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ProjectID, AggregateType: "dashboard_authoring",
		AggregateID: input.DashboardID, EventType: input.Type, SchemaVersion: 1,
		CorrelationID: input.CorrelationID, Payload: append([]byte(nil), input.Payload...),
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return authoringpostgres.Event{}, fmt.Errorf("%w: dashboard authoring event identity differs", authoring.ErrConflict)
		}
		return authoringpostgres.Event{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ProjectID ||
		stored.AggregateType != "dashboard_authoring" || stored.AggregateID != input.DashboardID ||
		stored.EventType != input.Type || stored.SchemaVersion != 1 ||
		stored.CorrelationID != input.CorrelationID || stored.AggregateVersion <= 0 ||
		!sameCanonical(stored.Payload, input.Payload) {
		return authoringpostgres.Event{}, fmt.Errorf("%w: dashboard authoring event identity differs", authoring.ErrConflict)
	}
	return authoringpostgres.Event{EventID: stored.EventID, ProjectID: stored.ScopeID, DashboardID: stored.AggregateID, ActorID: input.ActorID, CorrelationID: stored.CorrelationID, Revision: input.Revision, Type: stored.EventType, AggregateVersion: stored.AggregateVersion, Payload: append([]byte(nil), stored.Payload...)}, nil
}

func canonicalUUIDv7(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("must be a canonical UUIDv7")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return errors.New("must be a canonical UUIDv7")
	}
	return nil
}

func sameCanonical(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	lb, err := json.Marshal(l)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(r)
	return err == nil && bytes.Equal(lb, rb)
}
