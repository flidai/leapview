package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
)

type repeatableReadBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// ReadRecoveryFrontierSnapshot reads a recovery set and its selected
// validation evidence from one read-only repeatable-read snapshot. Non-
// published sets are returned without attempt/result data so callers can
// classify their lifecycle state without racing a later owner read.
func (r *Repository) ReadRecoveryFrontierSnapshot(ctx context.Context, setID string) (recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult, error) {
	if r == nil || r.db == nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, recoveryset.ErrInvalid
	}
	beginner, ok := r.db.(repeatableReadBeginner)
	if !ok {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, errors.New("recovery frontier snapshot requires a transaction-capable PostgreSQL pool")
	}
	tx, err := beginner.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	snapshot := New(tx)
	set, err := snapshot.ReadExact(ctx, setID)
	if err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	if set.Status != recoveryset.StatusPublished {
		if err := tx.Commit(ctx); err != nil {
			return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
		}
		return set, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, nil
	}
	attempt, err := snapshot.ValidationAttempt(ctx, set.PublishedValidationAttemptID)
	if err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	result, err := snapshot.ValidationResult(ctx, set.PublishedValidationAttemptID)
	if err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return recoveryset.RecoverySet{}, recoveryset.ValidationAttempt{}, recoveryset.ValidationResult{}, err
	}
	return set, attempt, result, nil
}
