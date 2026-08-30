// Package agentaudit composes the Agent-owned audit intent port with Access's
// canonical PostgreSQL audit authority. Keeping this adapter in app
// composition prevents Agent persistence from importing Access storage while
// preserving one caller-owned transaction for the agent mutation and audit
// row.
package agentaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
)

// Adapter is stateless and safe to share between Agent requests.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ agentpostgres.AuditIntentRecorder = (*Adapter)(nil)

// New returns an adapter backed by Access's immutable PostgreSQL audit table.
func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

// NewWithRepository is useful to composition tests and keeps the dependency
// explicit without exposing Access's concrete event projection to Agent.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// RecordAuditIntent appends and reads back the canonical audit intent using
// the exact transaction supplied by Agent. It never begins, commits, or rolls
// back that transaction.
func (a *Adapter) RecordAuditIntent(ctx context.Context, tx agentpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("agent audit adapter is not configured")
	}
	stored, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		return err
	}
	if err := validateStored(stored, intent); err != nil {
		return err
	}
	return nil
}

// validateStored checks every immutable field and the canonical payload
// digest. Access already performs this check internally, but repeating it at
// the application boundary ensures a future authority implementation cannot
// silently substitute an identity before Agent links the row to its source
// mutation.
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
		return fmt.Errorf("agent audit canonical identity differs: %w", access.ErrAuditIntentConflict)
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
