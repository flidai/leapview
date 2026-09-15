// Package auditadapter contains the deliberately small mechanical bridge
// from app-owned audit ports to Access's canonical PostgreSQL audit authority.
// Domain adapters remain responsible for intent construction, event
// projection, transaction types, and error mapping.
package auditadapter

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/jackc/pgx/v5"
)

var ErrUnavailable = errors.New("access audit authority is unavailable")

// Authority retains the exact Access repository allocated by application
// composition. It is stateless and safe to embed in domain-specific adapters.
type Authority struct {
	repository *accesspostgres.AuditRepository
}

func New(repository *accesspostgres.AuditRepository) Authority {
	return Authority{repository: repository}
}

func (a Authority) Configured() bool { return a.repository != nil }

func (a Authority) Matches(repository *accesspostgres.AuditRepository) bool {
	return a.repository != nil && a.repository == repository
}

// Record delegates one intent through the caller-owned transaction. It never
// begins, commits, or rolls back that transaction.
func (a Authority) Record(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) (accesspostgres.Event, error) {
	if !a.Configured() {
		return accesspostgres.Event{}, ErrUnavailable
	}
	return a.repository.RecordAuditEvent(ctx, tx, intent)
}

// Get reads one immutable audit event through the caller-owned database
// handle. The domain adapter remains responsible for validating/projecting
// the returned Access event.
func (a Authority) Get(ctx context.Context, db accesspostgres.DBTX, auditID string) (accesspostgres.Event, error) {
	if !a.Configured() {
		return accesspostgres.Event{}, ErrUnavailable
	}
	return a.repository.GetAuditEvent(ctx, db, auditID)
}
