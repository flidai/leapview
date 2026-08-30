// Package manageddataaudit bridges managed-data PostgreSQL transitions to the
// Access-owned audit authority at application composition time.
package manageddataaudit

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
)

// Adapter forwards audit intents through the caller-owned pgx transaction. It
// never opens, commits, or rolls back a transaction.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ manageddatapostgres.AuditIntentRecorder = (*Adapter)(nil)

func New() *Adapter { return &Adapter{audit: accesspostgres.New()} }

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

func (a *Adapter) RecordAuditIntent(ctx context.Context, tx manageddatapostgres.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("managed-data Access audit adapter is unavailable")
	}
	_, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	return err
}
