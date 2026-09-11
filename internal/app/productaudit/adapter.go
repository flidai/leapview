// Package productaudit bridges the admin product PostgreSQL mutation boundary
// to Access's canonical immutable audit repository at application composition.
package productaudit

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	productpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	"github.com/flidai/leapview/internal/app/auditadapter"
	"github.com/jackc/pgx/v5"
)

type Adapter struct {
	authority auditadapter.Authority
}

var _ productpostgres.AuditPort = (*Adapter)(nil)

// NewWithRepository binds the adapter to the exact Access audit authority
// allocated by application composition.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{authority: auditadapter.New(audit)}
}

// Matches proves this adapter retains the exact Access audit repository
// supplied by application composition rather than a sibling allocation.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.authority.Matches(audit)
}

func (a *Adapter) RecordAuditEvent(ctx context.Context, tx pgx.Tx, input productpostgres.AuditInput) error {
	if a == nil || !a.authority.Configured() {
		return productpostgres.ErrAuditUnavailable
	}
	capability, err := access.ParseCapability(input.Capability)
	if err != nil {
		return fmt.Errorf("product audit capability: %w", err)
	}
	intent := access.AuditIntent{EventID: input.EventID, PrincipalID: input.PrincipalID, Source: input.Source, Operation: input.Operation, Action: input.Action, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, Capability: capability, Outcome: input.Outcome, RequestID: input.RequestID, CorrelationID: input.CorrelationID, AggregateKey: input.AggregateKey, AggregateSequence: input.AggregateSequence, RequestDigest: input.RequestDigest, MetadataJSON: input.MetadataJSON}
	_, err = a.authority.Record(ctx, tx, intent)
	return err
}
