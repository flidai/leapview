// Package agentevents composes the Agent-owned domain-event port with the
// platform's canonical PostgreSQL event authority. The adapter lives in app
// composition so Agent persistence remains independent of sibling storage
// packages while sharing the caller-owned transaction.
package agentevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
)

// Adapter is stateless and safe to share between Agent requests.
type Adapter struct {
	events *eventspostgres.Repository
}

var _ agentpostgres.DomainEventAppender = (*Adapter)(nil)

// New returns an adapter backed by the platform's durable PostgreSQL event
// log.
func New() *Adapter { return &Adapter{events: eventspostgres.New()} }

// NewWithRepository is useful to composition tests and keeps the event
// authority explicit without exposing its concrete projection to Agent.
func NewWithRepository(events *eventspostgres.Repository) *Adapter {
	return &Adapter{events: events}
}

// AppendDomainEvent appends through the exact caller-owned transaction and
// validates the complete immutable projection before returning. It never
// begins, commits, or rolls back tx.
func (a *Adapter) AppendDomainEvent(ctx context.Context, tx agentpostgres.Tx, input agentpostgres.DomainEventInput) (agentpostgres.DomainEvent, error) {
	if a == nil || a.events == nil {
		return agentpostgres.DomainEvent{}, errors.New("agent domain event adapter is not configured")
	}
	if input.EventID == "" || input.EventID != strings.TrimSpace(input.EventID) {
		return agentpostgres.DomainEvent{}, errors.New("agent domain event id must be a canonical UUID")
	}
	parsed, err := uuid.Parse(input.EventID)
	if err != nil || parsed.String() != strings.ToLower(input.EventID) {
		return agentpostgres.DomainEvent{}, errors.New("agent domain event id must be a canonical UUID")
	}
	if input.CorrelationID != "" && input.CorrelationID != strings.TrimSpace(input.CorrelationID) {
		return agentpostgres.DomainEvent{}, errors.New("agent domain correlation id must be canonical")
	}
	if input.CorrelationID != "" {
		correlation, parseErr := uuid.Parse(input.CorrelationID)
		if parseErr != nil || correlation.String() != strings.ToLower(input.CorrelationID) {
			return agentpostgres.DomainEvent{}, errors.New("agent domain correlation id must be a canonical UUID")
		}
	}

	stored, err := a.events.AppendEvent(ctx, tx, eventspostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType,
		AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion,
		CorrelationID: input.CorrelationID, Payload: append([]byte(nil), input.Payload...),
	})
	if err != nil {
		var conflict *eventspostgres.EventConflictError
		if errors.As(err, &conflict) {
			return agentpostgres.DomainEvent{}, fmt.Errorf("%w: agent domain event identity differs", jobs.ErrConflict)
		}
		return agentpostgres.DomainEvent{}, err
	}
	if stored.EventID != input.EventID || stored.ScopeID != input.ScopeID ||
		stored.AggregateType != input.AggregateType || stored.AggregateID != input.AggregateID ||
		stored.EventType != input.EventType || stored.SchemaVersion != input.SchemaVersion ||
		stored.CorrelationID != input.CorrelationID || stored.AggregateVersion <= 0 ||
		!sameCanonical(stored.Payload, input.Payload) {
		return agentpostgres.DomainEvent{}, fmt.Errorf("%w: agent domain event identity differs", jobs.ErrConflict)
	}
	return agentpostgres.DomainEvent{
		EventID: stored.EventID, ScopeID: stored.ScopeID, AggregateType: stored.AggregateType,
		AggregateID: stored.AggregateID, AggregateVersion: stored.AggregateVersion,
		EventType: stored.EventType, SchemaVersion: stored.SchemaVersion,
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
	return err == nil && bytes.Equal(la, ra)
}
