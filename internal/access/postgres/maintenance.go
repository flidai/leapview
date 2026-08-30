package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// MaintenanceDBTX is the native surface accepted by the separately
// authenticated control-plane maintenance role.  It intentionally does not
// expose a Repository or any runtime mutation methods.
type MaintenanceDBTX interface {
	DBTX
}

// RetentionClass is the policy category encoded by generated audit envelopes.
// Legacy events without an envelope are treated as standard by the owner
// function; unknown categories are never eligible for routine pruning.
type RetentionClass string

const (
	RetentionShort    RetentionClass = "short"
	RetentionStandard RetentionClass = "standard"
	RetentionSecurity RetentionClass = "security"
)

// AuditRetentionResult is the durable evidence returned by one bounded owner
// function call.  RequestedCutoff/RequestedLimit prove the policy input while
// Cutoff/RetainedFloor report the database-clock-capped decision and the
// evidence boundary after the bounded batch.
type AuditRetentionResult struct {
	RetentionClass  RetentionClass
	RequestedCutoff time.Time
	Cutoff          time.Time
	RequestedLimit  int
	RemovedCount    int64
	RetainedFloor   time.Time
}

// Maintenance owns destructive audit retention.  Construct it with the
// separately authenticated maintenance pool; the database grants deny the
// runtime role both DELETE and EXECUTE on the owner function.
type Maintenance struct {
	db MaintenanceDBTX
}

// NewMaintenance constructs the bounded audit-retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance {
	return &Maintenance{db: db}
}

// Prune runs one bounded retention batch for class.  The owner function caps
// future cutoffs to the database clock and returns the durable floor evidence.
func (m *Maintenance) Prune(ctx context.Context, class RetentionClass, before time.Time, limit int) (AuditRetentionResult, error) {
	if m == nil || isNilMaintenanceDB(m.db) {
		return AuditRetentionResult{}, errors.New("audit retention maintenance repository is nil")
	}
	return pruneAuditEvents(ctx, m.db, class, before, limit)
}

// PruneTx runs the same bounded owner function on a caller-owned transaction.
// It does not commit or roll back the transaction.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, class RetentionClass, before time.Time, limit int) (AuditRetentionResult, error) {
	if m == nil || isNilMaintenanceDB(tx) {
		return AuditRetentionResult{}, errors.New("audit retention maintenance repository is nil")
	}
	return pruneAuditEvents(ctx, tx, class, before, limit)
}

func pruneAuditEvents(ctx context.Context, db DBTX, class RetentionClass, before time.Time, limit int) (AuditRetentionResult, error) {
	if isNilMaintenanceDB(db) {
		return AuditRetentionResult{}, errors.New("audit retention PostgreSQL connection is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	class = RetentionClass(strings.TrimSpace(string(class)))
	if class != RetentionShort && class != RetentionStandard && class != RetentionSecurity {
		return AuditRetentionResult{}, errors.New("audit retention class must be short, standard, or security")
	}
	if before.IsZero() {
		return AuditRetentionResult{}, errors.New("audit retention cutoff is required")
	}
	if limit < 1 || limit > 1000 {
		return AuditRetentionResult{}, errors.New("audit retention batch limit must be between 1 and 1000")
	}
	row, err := accessdb.New(db).PruneAuditEvents(ctx, accessdb.PruneAuditEventsParams{
		RetentionClass:  string(class),
		RequestedCutoff: pgtype.Timestamptz{Time: before.UTC(), Valid: true},
		BatchLimit:      int32(limit),
	})
	if err != nil {
		return AuditRetentionResult{}, err
	}
	requestedCutoff := before.UTC()
	// PostgreSQL timestamptz stores microseconds. Compare the database echo at
	// that precision while retaining the caller's original UTC value in the Go
	// result for policy/audit evidence.
	databaseCutoff := requestedCutoff.Truncate(time.Microsecond)
	if row.RetentionClass != string(class) || row.RequestedLimit != int32(limit) ||
		!row.RequestedCutoff.Valid || !row.Cutoff.Valid || !row.RetainedFloor.Valid ||
		!row.RequestedCutoff.Time.UTC().Equal(databaseCutoff) {
		return AuditRetentionResult{}, fmt.Errorf("invalid audit retention owner evidence")
	}
	if row.RemovedCount < 0 || row.RemovedCount > int64(limit) {
		return AuditRetentionResult{}, fmt.Errorf("invalid audit retention removal count %d", row.RemovedCount)
	}
	if row.RetainedFloor.Time.UTC().After(row.Cutoff.Time.UTC()) {
		return AuditRetentionResult{}, fmt.Errorf("invalid audit retention floor %s after cutoff %s", row.RetainedFloor.Time.UTC(), row.Cutoff.Time.UTC())
	}
	return AuditRetentionResult{
		RetentionClass:  RetentionClass(row.RetentionClass),
		RequestedCutoff: requestedCutoff,
		Cutoff:          row.Cutoff.Time.UTC(),
		RequestedLimit:  int(row.RequestedLimit),
		RemovedCount:    row.RemovedCount,
		RetainedFloor:   row.RetainedFloor.Time.UTC(),
	}, nil
}

func isNilMaintenanceDB(db DBTX) bool {
	if db == nil {
		return true
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
