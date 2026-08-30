package postgres

import (
	"context"
	"time"

	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// Maintenance owns destructive managed-data upload-session retention. The
// runtime Repository intentionally has no prune method.
type Maintenance struct{ db MaintenanceDBTX }

// NewMaintenance constructs the bounded upload-session retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// PruneUploadSessions invokes the bounded SECURITY DEFINER maintenance
// function. Only completed cleanup evidence and terminal rows are eligible;
// the function itself enforces those lifecycle predicates.
func (m *Maintenance) PruneUploadSessions(ctx context.Context, before time.Time, limit int) (int64, error) {
	return m.pruneUploadSessions(ctx, mDB(m), before, limit)
}

// PruneUploadSessionsTx executes one bounded batch on a caller-owned
// transaction and does not commit or roll back it.
func (m *Maintenance) PruneUploadSessionsTx(ctx context.Context, tx Tx, before time.Time, limit int) (int64, error) {
	return m.pruneUploadSessions(ctx, tx, before, limit)
}

func (m *Maintenance) pruneUploadSessions(ctx context.Context, db MaintenanceDBTX, before time.Time, limit int) (int64, error) {
	if m == nil || db == nil {
		return 0, ErrInvalid
	}
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	return manageddb.New(db).PruneUploadSessions(contextOrBackground(ctx), manageddb.PruneUploadSessionsParams{
		Cutoff: pgtype.Timestamptz{Time: before.UTC(), Valid: true}, PLimit: int32(limit),
	})
}

func mDB(m *Maintenance) MaintenanceDBTX {
	if m == nil {
		return nil
	}
	return m.db
}
