package postgres

import (
	"context"

	cachedb "github.com/flidai/leapview/internal/analytics/cache/postgres/internal/db"
)

// Prune removes only durable cache-coordination rows outside the caller's
// retention boundary. Manifest rows remain lifecycle evidence and are never
// deleted. A bounded batch keeps vacuum and lock impact small.
func (m *Maintenance) Prune(ctx context.Context, opts PruneOptions) (PruneStats, error) {
	return m.prune(ctx, mDB(m), opts)
}

// PruneTx runs one bounded retention batch on a caller-owned transaction. It
// does not commit or roll back that transaction.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, opts PruneOptions) (PruneStats, error) {
	return m.prune(ctx, tx, opts)
}

func (m *Maintenance) prune(ctx context.Context, db MaintenanceDBTX, opts PruneOptions) (PruneStats, error) {
	if m == nil || db == nil || opts.Before.IsZero() {
		return PruneStats{}, ErrInvalid
	}
	if opts.Limit < 1 || opts.Limit > maxPruneBatch {
		return PruneStats{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row, err := cachedb.New(db).PruneCoordination(ctx, cachedb.PruneCoordinationParams{
		Before: dbTime(&opts.Before), LimitCount: int32(opts.Limit),
	})
	if err != nil {
		return PruneStats{}, err
	}
	if row.Invalidations < 0 || row.ExpiredLeases < 0 || row.Invalidations+row.ExpiredLeases > int64(opts.Limit) {
		return PruneStats{}, ErrConflict
	}
	return PruneStats{Invalidations: row.Invalidations, ExpiredLeases: row.ExpiredLeases}, nil
}

func mDB(m *Maintenance) MaintenanceDBTX {
	if m == nil {
		return nil
	}
	return m.db
}
