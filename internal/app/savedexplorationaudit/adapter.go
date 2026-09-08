// Package savedexplorationaudit adapts Access's canonical PostgreSQL audit
// authority to the saved-exploration repository transaction boundary.
package savedexplorationaudit

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	savedpostgres "github.com/flidai/leapview/internal/analytics/exploration/saved/postgres"
	"github.com/jackc/pgx/v5"
)

// Adapter retains the exact Access audit repository allocated by application
// composition. It never opens, commits, or rolls back the transaction owned by
// the saved-exploration mutation.
type Adapter struct {
	audit *accesspostgres.AuditRepository
}

var _ savedpostgres.AuditRepository = (*Adapter)(nil)

// NewWithRepository binds the adapter to the canonical Access audit authority.
func NewWithRepository(audit *accesspostgres.AuditRepository) *Adapter {
	return &Adapter{audit: audit}
}

// Matches proves that this adapter retains the exact audit authority supplied
// by application composition rather than a sibling repository.
func (a *Adapter) Matches(audit *accesspostgres.AuditRepository) bool {
	return a != nil && a.audit != nil && a.audit == audit
}

// RecordAuditEvent appends one canonical Access audit event through the exact
// caller-owned pgx transaction.
func (a *Adapter) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	if a == nil || a.audit == nil {
		return errors.New("saved exploration audit authority is unavailable")
	}
	if tx == nil {
		return errors.New("saved exploration audit transaction is nil")
	}
	_, err := a.audit.RecordAuditEvent(ctx, tx, intent)
	return err
}
