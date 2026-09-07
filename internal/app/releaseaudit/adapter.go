// Package releaseaudit composes the Release-owned audit port with Access's
// canonical PostgreSQL audit authority. Keeping this adapter in app prevents
// release persistence from importing the sibling access storage package while
// retaining one caller-owned transaction for the release mutation, audit row,
// and eventual commit.
package releaseaudit

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/jackc/pgx/v5"
)

// Adapter is stateless and safe to share between release requests.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ releasepostgres.AuditAppender = (*Adapter)(nil)

// New returns an adapter backed by Access's immutable PostgreSQL audit table.
// NewWithRepository binds the adapter to the exact Access audit authority
// allocated by application composition.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves this adapter retains the exact Access audit repository
// supplied by application composition rather than a sibling allocation.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditEvent appends and reads back the canonical audit intent using the
// exact transaction supplied by Release. It never begins, commits, or rolls
// back the transaction itself.
func (a *Adapter) RecordAuditEvent(ctx context.Context, tx releasepostgres.Tx, intent access.AuditIntent) (releasepostgres.AuditEvent, error) {
	if a == nil || a.audit == nil {
		return releasepostgres.AuditEvent{}, fmt.Errorf("release audit adapter is not configured")
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
			return releasepostgres.AuditEvent{}, fmt.Errorf("%w: release audit identity differs", releasepostgres.ErrConflict)
		}
		return releasepostgres.AuditEvent{}, err
	}
	return mapStored(stored), nil
}

func mapStored(stored accesspostgres.Event) releasepostgres.AuditEvent {
	return releasepostgres.AuditEvent{
		AuditID: stored.AuditID, DomainEventID: stored.DomainEventID, ScopeID: stored.ScopeID,
		ActorID: stored.ActorID, PrincipalID: stored.PrincipalID, Source: stored.Source,
		Operation: stored.Operation, Action: stored.Action, ResourceKind: stored.ResourceKind,
		ResourceID: stored.ResourceID, Capability: stored.Capability, Outcome: stored.Outcome,
		RequestID: stored.RequestID, RequestDigest: stored.RequestDigest, CorrelationID: stored.CorrelationID,
		AggregateKey: stored.AggregateKey, AggregateSequence: stored.AggregateSequence,
		MetadataJSON: stored.MetadataJSON, OccurredAt: stored.OccurredAt, IntentDigest: stored.IntentDigest,
	}
}
