// Package releaseaudit composes the Release-owned audit port with Access's
// canonical PostgreSQL audit authority. Keeping this adapter in app prevents
// release persistence from importing the sibling access storage package while
// retaining one caller-owned transaction for the release mutation, audit row,
// and eventual commit.
package releaseaudit

import (
	"bytes"
	"context"
	"encoding/json"
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
func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

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
	if err := validateStored(stored, intent); err != nil {
		return releasepostgres.AuditEvent{}, err
	}
	return mapStored(stored), nil
}

func validateStored(stored accesspostgres.Event, expected access.AuditIntent) error {
	canonical, err := expected.Canonicalize()
	if err != nil {
		return err
	}
	digest, err := canonical.PayloadDigest()
	if err != nil {
		return err
	}
	if stored.AuditID != canonical.EventID || stored.DomainEventID != canonical.DomainEventID ||
		stored.ScopeID != canonical.ScopeID || stored.ActorID != canonical.ActorID ||
		stored.PrincipalID != canonical.PrincipalID || stored.Source != canonical.Source ||
		stored.Operation != canonical.Operation || stored.Action != canonical.Action ||
		stored.ResourceKind != canonical.ResourceKind || stored.ResourceID != canonical.ResourceID ||
		stored.Capability != canonical.Capability || stored.Outcome != canonical.Outcome ||
		stored.RequestID != canonical.RequestID || stored.RequestDigest != canonical.RequestDigest ||
		stored.CorrelationID != canonical.CorrelationID || stored.AggregateKey != canonical.AggregateKey ||
		stored.AggregateSequence != canonical.AggregateSequence || !sameJSON(stored.MetadataJSON, canonical.MetadataJSON) ||
		stored.IntentDigest != digest {
		return fmt.Errorf("%w: release audit canonical identity differs", releasepostgres.ErrConflict)
	}
	return nil
}

func sameJSON(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return false
	}
	la, err := json.Marshal(a)
	if err != nil {
		return false
	}
	ra, err := json.Marshal(b)
	return err == nil && bytes.Equal(la, ra)
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
