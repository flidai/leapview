package postgres

import (
	"context"
	"fmt"
	"time"

	sessiondb "github.com/flidai/leapview/internal/dashboard/session/postgres/internal/db"
)

// NewMaintenance constructs the bounded expired-session cleanup facade over
// a separately authenticated maintenance connection.
func NewMaintenance(db MaintenanceDBTX) *Maintenance {
	return &Maintenance{db: db, clock: time.Now}
}

// DeleteExpiredBatch removes at most batchSize expired sessions. Request
// serving uses Store.Load's expiry predicate and never calls this operation.
func (m *Maintenance) DeleteExpiredBatch(ctx context.Context, batchSize int) (int64, error) {
	return m.deleteExpiredBatch(ctx, mDB(m), batchSize)
}

// DeleteExpiredBatchTx executes one bounded cleanup batch on a caller-owned
// transaction and does not commit or roll back it.
func (m *Maintenance) DeleteExpiredBatchTx(ctx context.Context, tx Tx, batchSize int) (int64, error) {
	return m.deleteExpiredBatch(ctx, tx, batchSize)
}

func (m *Maintenance) deleteExpiredBatch(ctx context.Context, db MaintenanceDBTX, batchSize int) (int64, error) {
	if m == nil || db == nil {
		return 0, ErrUnavailable
	}
	if batchSize < 1 || batchSize > maxExpiredBatch {
		return 0, fmt.Errorf("dashboard session expiry batch size must be between 1 and %d", maxExpiredBatch)
	}
	now := time.Now
	if m.clock != nil {
		now = m.clock
	}
	return sessiondb.New(db).DeleteExpired(ctx, sessiondb.DeleteExpiredParams{
		Now: now().UTC(), BatchSize: int32(batchSize),
	})
}

func mDB(m *Maintenance) MaintenanceDBTX {
	if m == nil {
		return nil
	}
	return m.db
}
