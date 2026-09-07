// Package dashboardpublicationaudit adapts dashboard-publication audit intents
// to Access' canonical PostgreSQL audit authority.
package dashboardpublicationaudit

import (
	"context"
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

func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves this adapter is bound to the exact Access audit authority
// allocated by application composition.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditIntent persists a publication intent through the exact caller
// transaction. Access validates and reads back every immutable audit identity
// and payload field at its canonical boundary.
func (a *Adapter) RecordAuditIntent(ctx context.Context, tx publicationpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("dashboard publication audit adapter is not configured")
	}
	if tx == nil {
		return errors.New("dashboard publication audit transaction is required")
	}
	_, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: dashboard publication audit identity differs", publication.ErrConflict)
		}
		return err
	}
	return nil
}
