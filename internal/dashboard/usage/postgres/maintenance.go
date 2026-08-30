package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/dashboard/usage"
	usagedb "github.com/flidai/leapview/internal/dashboard/usage/postgres/internal/db"
)

// NewMaintenance constructs the bounded dashboard viewer-day retention
// facade over a separately authenticated maintenance handle.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// DeleteBefore removes at most batchSize viewer-day rows older than cutoff.
// Callers should repeat bounded batches until the returned count is below the
// requested limit.
func (m *Maintenance) DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return m.deleteBefore(ctx, mDB(m), cutoff, batchSize)
}

// DeleteBeforeTx executes one bounded batch on a caller-owned transaction and
// does not commit or roll back it.
func (m *Maintenance) DeleteBeforeTx(ctx context.Context, tx Tx, cutoff time.Time, batchSize int) (int64, error) {
	return m.deleteBefore(ctx, tx, cutoff, batchSize)
}

func (m *Maintenance) deleteBefore(ctx context.Context, db MaintenanceDBTX, cutoff time.Time, batchSize int) (int64, error) {
	if m == nil || db == nil {
		return 0, ErrUnavailable
	}
	if cutoff.IsZero() {
		return 0, fmt.Errorf("dashboard usage retention cutoff is required")
	}
	if batchSize < 1 || batchSize > maxRetentionBatch {
		return 0, fmt.Errorf("dashboard usage retention batch size must be between 1 and %d", maxRetentionBatch)
	}
	return usagedb.New(db).DeleteBefore(ctxOrBackground(ctx), usagedb.DeleteBeforeParams{
		CutoffDate: cutoff.UTC(), BatchSize: int32(batchSize),
	})
}

func mDB(m *Maintenance) MaintenanceDBTX {
	if m == nil {
		return nil
	}
	return m.db
}

var _ usage.Retainer = (*Maintenance)(nil)
