package postgres

import (
	"context"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// Maintenance owns destructive managed-data upload-session retention. The
// runtime Repository intentionally has no prune method.
type Maintenance struct{ db MaintenanceDBTX }

// NewMaintenance constructs the bounded upload-session retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

// PostgreSQLMaintenanceAuthority prevents production composition from
// substituting the runtime repository for this separately authenticated
// capability.
func (*Maintenance) PostgreSQLMaintenanceAuthority() {}

// Configured reports whether the facade retains its caller-owned maintenance
// database handle.
func (m *Maintenance) Configured() bool {
	if m == nil || m.db == nil {
		return false
	}
	value := reflect.ValueOf(m.db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

// MarkUploadCleanupComplete records cleanup evidence through the separately
// authenticated maintenance connection.
func (m *Maintenance) MarkUploadCleanupComplete(ctx context.Context, id manageddata.UploadID) error {
	if m == nil || m.db == nil || id.String() == "" {
		return ErrInvalid
	}
	marked, err := manageddb.New(m.db).MarkUploadCleanup(contextOrBackground(ctx), id.String())
	if err != nil {
		return err
	}
	if !marked {
		return ErrConflict
	}
	return nil
}

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
