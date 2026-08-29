// Package connectionbindingaudit composes the analytics connection-binding
// audit port with Access's canonical PostgreSQL audit authority.
package connectionbindingaudit

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
)

// Adapter appends connection-administration audit intents through the exact
// transaction supplied by the connection-binding authority. It never owns
// commit or rollback.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ connectionbindingpostgres.AuditRepository = (*Adapter)(nil)

// New constructs the app-composition adapter for the Access-owned audit log.
func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

// RecordAuditEvent persists and validates the canonical audit intent in tx.
func (a *Adapter) RecordAuditEvent(ctx context.Context, tx connectionbindingpostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return connectionbinding.ErrAdministrationAuditUnavailable
	}
	_, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	return err
}
