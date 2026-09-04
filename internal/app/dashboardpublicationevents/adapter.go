// Package dashboardpublicationevents adapts dashboard-publication domain
// events to the platform's canonical PostgreSQL event log.
package dashboardpublicationevents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	publication "github.com/flidai/leapview/internal/dashboard/publication"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/events/watermill"
	"github.com/google/uuid"
)

type Adapter struct {
	events *watermill.Adapter
}

var _ publicationpostgres.EventPort = (*Adapter)(nil)

func New() *Adapter { return NewWithRepository(eventspostgres.New()) }

func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	shared, err := watermill.New(events)
	if err != nil {
		return &Adapter{}
	}
	return &Adapter{events: shared}
}

// Matches proves this adapter is bound to the exact platform event authority
// allocated by application composition.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events.Matches(events)
}

// AppendEvent appends through the exact caller-owned transaction and validates
// the complete immutable event projection before returning.
func (a *Adapter) AppendEvent(ctx context.Context, tx publicationpostgres.Tx, input publicationpostgres.EventInput) (publicationpostgres.Event, error) {
	if a == nil || a.events == nil {
		return publicationpostgres.Event{}, errors.New("dashboard publication event adapter is not configured")
	}
	if tx == nil {
		return publicationpostgres.Event{}, errors.New("dashboard publication event transaction is required")
	}
	// Preserve the domain port's historical conflict classification before the
	// shared boundary inspects the remaining immutable input fields.
	if err := canonicalEventUUIDv7(input.EventID); err != nil {
		return publicationpostgres.Event{}, fmt.Errorf("%w: publication event id: %v", publication.ErrConflict, err)
	}
	correlationID := eventspostgres.CanonicalCorrelationID(input.CorrelationID)
	stored, err := a.events.AppendEvent(ctx, tx, watermill.TopicDashboard, watermill.EventInput{
		EventID: input.EventID, ScopeID: input.ProjectID, AggregateType: "dashboard_publication",
		AggregateID: input.PublicationID, EventType: input.Type, SchemaVersion: 1,
		CorrelationID: correlationID, Payload: append([]byte(nil), input.Payload...),
	})
	if err != nil {
		if isEventIDValidation(err) {
			return publicationpostgres.Event{}, fmt.Errorf("%w: publication event id: %v", publication.ErrConflict, err)
		}
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return publicationpostgres.Event{}, fmt.Errorf("%w: dashboard publication event identity differs", publication.ErrConflict)
		}
		return publicationpostgres.Event{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ProjectID ||
		stored.AggregateType != "dashboard_publication" || stored.AggregateID != input.PublicationID ||
		stored.EventType != input.Type || stored.SchemaVersion != 1 ||
		stored.CorrelationID != correlationID || stored.AggregateVersion <= 0 {
		return publicationpostgres.Event{}, fmt.Errorf("%w: dashboard publication event identity differs", publication.ErrConflict)
	}
	return publicationpostgres.Event{EventID: stored.EventID, ProjectID: stored.ScopeID, PublicationID: stored.AggregateID, ActorID: input.ActorID, CorrelationID: stored.CorrelationID, Type: stored.EventType, ServingStateID: input.ServingStateID, Revision: input.Revision, AggregateVersion: stored.AggregateVersion, Payload: append([]byte(nil), stored.Payload...)}, nil
}

func isEventIDValidation(err error) bool {
	var validation *watermill.ValidationError
	return errors.As(err, &validation) && validation.Field == "eventId"
}

func canonicalEventUUIDv7(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("must be a canonical UUIDv7")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return errors.New("must be a canonical UUIDv7")
	}
	return nil
}
