package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentdb "github.com/flidai/leapview/internal/agent/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const MaxRetentionBatch = 1000

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated maintenance connection. Role grants, rather than a second
// client abstraction, enforce that this handle can only execute the bounded
// retention facade.
type MaintenanceDBTX interface {
	DBTX
}

// Maintenance owns destructive agent-history retention. Request-serving code
// constructs Repository, while this facade is built only around the
// separately authenticated maintenance pool.
type Maintenance struct {
	db MaintenanceDBTX
}

// RetentionResult is the durable evidence returned by one bounded batch.
// Floors are independent because archived conversations and their run-event
// streams can drain at different rates.
type RetentionResult struct {
	Before               time.Time
	Cutoff               time.Time
	RequestedLimit       int
	ConversationsDeleted int64
	MessagesDeleted      int64
	RunsDeleted          int64
	RunEventsDeleted     int64
	ConversationsFloorAt time.Time
	RunEventsFloorAt     time.Time
}

func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

type maintenanceBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Prune executes one bounded transaction over both agent retention classes.
// A zero cutoff is rejected so operators must supply an explicit policy
// boundary; PostgreSQL caps future cutoffs to its own clock.
func (m *Maintenance) Prune(ctx context.Context, before time.Time, limit int) (RetentionResult, error) {
	if m == nil || m.db == nil {
		return RetentionResult{}, errors.New("agent maintenance database is required")
	}
	if before.IsZero() || limit < 1 || limit > MaxRetentionBatch {
		return RetentionResult{}, errors.New("agent retention cutoff and batch limit are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := m.db.(maintenanceBeginner)
	if !ok {
		return RetentionResult{}, errors.New("agent maintenance requires a pgx transaction-capable DB")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	result, err := m.PruneTx(ctx, tx, before, limit)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return RetentionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(context.Background())
		return RetentionResult{}, err
	}
	return result, nil
}

// PruneTx executes the owner function on a caller-owned transaction and does
// not commit or roll back it.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, before time.Time, limit int) (RetentionResult, error) {
	if m == nil || tx == nil {
		return RetentionResult{}, errors.New("agent retention transaction is required")
	}
	if before.IsZero() || limit < 1 || limit > MaxRetentionBatch {
		return RetentionResult{}, errors.New("agent retention cutoff and batch limit are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// PostgreSQL timestamptz stores microsecond precision. Normalize before
	// sending so the echoed cutoff can be compared exactly as retention
	// evidence rather than silently accepting a rounded boundary.
	requested := before.UTC().Truncate(time.Microsecond)
	row, err := agentdb.New(tx).PruneArchivedAgentHistory(ctx, agentdb.PruneArchivedAgentHistoryParams{
		RequestedCutoff: pgtype.Timestamptz{Time: requested, Valid: true},
		BatchLimit:      int32(limit),
	})
	if err != nil {
		return RetentionResult{}, err
	}
	cutoff := row.Cutoff.Time.UTC()
	conversationsFloor := row.ConversationsFloor.Time.UTC()
	runEventsFloor := row.RunEventsFloor.Time.UTC()
	if !row.RequestedCutoff.Valid || !row.Cutoff.Valid || !row.ConversationsFloor.Valid || !row.RunEventsFloor.Valid ||
		!row.RequestedCutoff.Time.UTC().Equal(requested) || row.RequestedLimit != int32(limit) ||
		row.RequestedLimit < 1 || row.RequestedLimit > MaxRetentionBatch ||
		cutoff.IsZero() || conversationsFloor.IsZero() || runEventsFloor.IsZero() || cutoff.After(requested) ||
		row.ConversationsRemoved < 0 || row.MessagesRemoved < 0 || row.RunsRemoved < 0 || row.RunEventsRemoved < 0 ||
		row.ConversationsRemoved+row.MessagesRemoved+row.RunsRemoved+row.RunEventsRemoved > int64(limit) {
		return RetentionResult{}, fmt.Errorf("invalid agent retention evidence")
	}
	return RetentionResult{
		Before:               requested,
		Cutoff:               cutoff,
		RequestedLimit:       int(row.RequestedLimit),
		ConversationsDeleted: row.ConversationsRemoved,
		MessagesDeleted:      row.MessagesRemoved,
		RunsDeleted:          row.RunsRemoved,
		RunEventsDeleted:     row.RunEventsRemoved,
		ConversationsFloorAt: conversationsFloor,
		RunEventsFloorAt:     runEventsFloor,
	}, nil
}
