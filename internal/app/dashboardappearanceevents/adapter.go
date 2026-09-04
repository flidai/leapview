// Package dashboardappearanceevents adapts dashboard-appearance events to the
// platform's canonical PostgreSQL event authority.
package dashboardappearanceevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/google/uuid"
)

type Adapter struct {
	events *eventspostgres.Repository
}

var _ appearancepostgres.EventPort = (*Adapter)(nil)

func New() *Adapter { return NewWithRepository(eventspostgres.New()) }

func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// Matches proves this adapter is bound to the exact platform event authority
// allocated by application composition.
func (a *Adapter) Matches(events *eventspostgres.Repository) bool {
	return a != nil && a.events != nil && a.events == events
}

// AppendEvent appends through the exact caller-owned transaction and validates
// the immutable event identity and canonical patch payload.
func (a *Adapter) AppendEvent(ctx context.Context, tx appearancepostgres.Tx, input appearancepostgres.EventInput) (appearancepostgres.Event, error) {
	if a == nil || a.events == nil {
		return appearancepostgres.Event{}, errors.New("dashboard appearance event adapter is not configured")
	}
	if tx == nil {
		return appearancepostgres.Event{}, errors.New("dashboard appearance event transaction is required")
	}
	// Preserve the domain port's conflict classification before the canonical
	// repository inspects the remaining immutable fields.
	if err := canonicalEventUUIDv7(input.EventID); err != nil {
		return appearancepostgres.Event{}, fmt.Errorf("%w: appearance event id: %v", appearancepostgres.ErrConflict, err)
	}
	payload, err := json.Marshal(input.Patch)
	if err != nil {
		return appearancepostgres.Event{}, fmt.Errorf("%w: appearance event payload: %v", appearancepostgres.ErrConflict, err)
	}
	const aggregateType = "dashboard_appearance"
	const eventType = "dashboard.appearance.updated"
	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ProjectID, AggregateType: aggregateType,
		AggregateID: input.DashboardID, EventType: eventType, SchemaVersion: 1,
		Payload: payload,
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return appearancepostgres.Event{}, fmt.Errorf("%w: dashboard appearance event identity differs", appearancepostgres.ErrConflict)
		}
		return appearancepostgres.Event{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ProjectID ||
		stored.AggregateType != aggregateType || stored.AggregateID != input.DashboardID ||
		stored.EventType != eventType || stored.SchemaVersion != 1 || stored.AggregateVersion <= 0 {
		return appearancepostgres.Event{}, fmt.Errorf("%w: dashboard appearance event identity differs", appearancepostgres.ErrConflict)
	}
	return appearancepostgres.Event{EventID: stored.EventID, ProjectID: stored.ScopeID, DashboardID: stored.AggregateID, ActorID: input.ActorID, Revision: input.Revision, Patch: input.Patch, AggregateVersion: stored.AggregateVersion}, nil
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
