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
	"github.com/google/uuid"
)

type Adapter struct {
	events *eventspostgres.Repository
}

var _ publicationpostgres.EventPort = (*Adapter)(nil)

func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// Matches proves this adapter is bound to the exact platform event authority
// allocated by application composition.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events != nil && a.events == events
}

// AppendEvent appends through the exact caller-owned transaction. The platform
// event authority validates the complete immutable projection before return.
func (a *Adapter) AppendEvent(ctx context.Context, tx publicationpostgres.Tx, input publicationpostgres.EventInput) (publicationpostgres.Event, error) {
	if a == nil || a.events == nil {
		return publicationpostgres.Event{}, errors.New("dashboard publication event adapter is not configured")
	}
	if tx == nil {
		return publicationpostgres.Event{}, errors.New("dashboard publication event transaction is required")
	}
	// Preserve the domain port's historical conflict classification before the
	// canonical repository inspects the remaining immutable input fields.
	if err := canonicalEventUUIDv7(input.EventID); err != nil {
		return publicationpostgres.Event{}, fmt.Errorf("%w: publication event id: %v", publication.ErrConflict, err)
	}
	if input.CorrelationID != "" {
		if err := canonicalCorrelationUUIDv7(input.CorrelationID); err != nil {
			return publicationpostgres.Event{}, fmt.Errorf("%w: publication correlation id: %v", publication.ErrConflict, err)
		}
	}
	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ProjectID, AggregateType: "dashboard_publication",
		AggregateID: input.PublicationID, EventType: input.Type, SchemaVersion: 1,
		CorrelationID: input.CorrelationID, Payload: append([]byte(nil), input.Payload...),
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return publicationpostgres.Event{}, fmt.Errorf("%w: dashboard publication event identity differs", publication.ErrConflict)
		}
		return publicationpostgres.Event{}, err
	}
	return publicationpostgres.Event{EventID: stored.EventID, ProjectID: stored.ScopeID, PublicationID: stored.AggregateID, ActorID: input.ActorID, CorrelationID: stored.CorrelationID, Type: stored.EventType, ServingStateID: input.ServingStateID, Revision: input.Revision, AggregateVersion: stored.AggregateVersion, Payload: append([]byte(nil), stored.Payload...)}, nil
}

func canonicalCorrelationUUIDv7(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("must be a canonical UUIDv7")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != 7 {
		return errors.New("must be a canonical UUIDv7")
	}
	return nil
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
