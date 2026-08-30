// Package dashboardpublicationaudit adapts dashboard-publication audit intents
// to Access' canonical PostgreSQL audit authority.
package dashboardpublicationaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	publication "github.com/flidai/leapview/internal/dashboard/publication"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	"github.com/jackc/pgx/v5"
)

type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ publicationpostgres.AuditPort = (*Adapter)(nil)

func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves this adapter is bound to the exact Access audit authority
// allocated by application composition.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditIntent persists a publication intent through the exact caller
// transaction and verifies every immutable audit identity and payload field.
func (a *Adapter) RecordAuditIntent(ctx context.Context, tx publicationpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("dashboard publication audit adapter is not configured")
	}
	if tx == nil {
		return errors.New("dashboard publication audit transaction is required")
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: dashboard publication audit identity differs", publication.ErrConflict)
		}
		return err
	}
	if err := validateStored(stored, intent); err != nil {
		return fmt.Errorf("%w: dashboard publication audit canonical identity differs", publication.ErrConflict)
	}
	return nil
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
		return errors.New("stored audit projection differs")
	}
	return nil
}

func sameJSON(left, right string) bool {
	var l, r any
	if json.Unmarshal([]byte(left), &l) != nil || json.Unmarshal([]byte(right), &r) != nil {
		return false
	}
	lb, err := json.Marshal(l)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(r)
	return err == nil && bytes.Equal(lb, rb)
}
