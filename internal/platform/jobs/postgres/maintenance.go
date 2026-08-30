package postgres

import (
	"context"
	"fmt"
	"time"

	jobdb "github.com/flidai/leapview/internal/platform/jobs/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxMaintenanceBatch = 1000

// Prune removes at most limit terminal jobs whose completion time is at or
// before before. The generated query invokes the SECURITY DEFINER capability
// function; only a separately authenticated maintenance role is granted
// EXECUTE on that function.
func (m *Maintenance) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	return m.prune(ctx, mDB(m), before, limit)
}

// PruneTx runs one bounded retention batch on a caller-owned transaction. It
// deliberately does not commit or roll back the transaction.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, before time.Time, limit int) (int64, error) {
	return m.prune(ctx, tx, before, limit)
}

func (m *Maintenance) prune(ctx context.Context, db MaintenanceDBTX, before time.Time, limit int) (int64, error) {
	if m == nil || db == nil {
		return 0, fmt.Errorf("jobs maintenance database is required")
	}
	if before.IsZero() || limit < 1 || limit > maxMaintenanceBatch {
		return 0, fmt.Errorf("job prune cutoff and limit must be between 1 and %d", maxMaintenanceBatch)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return jobdb.New(db).Prune(ctx, jobdb.PruneParams{
		Before:     pgtype.Timestamptz{Time: before.UTC(), Valid: true},
		BatchLimit: int32(limit),
	})
}

func mDB(m *Maintenance) MaintenanceDBTX {
	if m == nil {
		return nil
	}
	return m.db
}
