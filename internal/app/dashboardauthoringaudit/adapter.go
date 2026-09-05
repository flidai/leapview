// Package dashboardauthoringaudit adapts the dashboard-authoring audit port
// to Access' canonical PostgreSQL audit authority. The source repository owns
// transaction boundaries; this adapter only appends and validates the row.
package dashboardauthoringaudit

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	authoring "github.com/flidai/leapview/internal/dashboard/authoring"
	authoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	"github.com/jackc/pgx/v5"
)

type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ authoringpostgres.AuditPort = (*Adapter)(nil)

func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves this adapter is bound to the exact Access audit authority
// allocated by application composition.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditIntent persists an authoring intent through the exact caller
// transaction. Access validates and reads back the complete immutable audit
// projection at its canonical boundary.
func (a *Adapter) RecordAuditIntent(ctx context.Context, tx authoringpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("dashboard authoring audit adapter is not configured")
	}
	if tx == nil {
		return errors.New("dashboard authoring audit transaction is required")
	}
	_, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	if err != nil {
		if errors.Is(err, access.ErrAuditIntentConflict) || errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: dashboard authoring audit identity differs", authoring.ErrConflict)
		}
		return err
	}
	return nil
}
