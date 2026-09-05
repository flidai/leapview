package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// SQLStateSerializationFailure is PostgreSQL's serialization_failure code.
	SQLStateSerializationFailure = "40001"
	// SQLStateDeadlockDetected is PostgreSQL's deadlock_detected code.
	SQLStateDeadlockDetected = "40P01"
)

// IsTransactionRetryable reports whether err is a PostgreSQL transaction
// failure for which the caller may retry the complete transaction.  The
// transaction and its retry budget remain caller-owned; this helper only
// classifies SQLSTATEs and deliberately does not retry or inspect messages.
func IsTransactionRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr == nil {
		return false
	}
	switch pgErr.Code {
	case SQLStateSerializationFailure, SQLStateDeadlockDetected:
		return true
	default:
		return false
	}
}
