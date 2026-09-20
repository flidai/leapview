// Package accesslifecycle contains the small PostgreSQL boundary shared by
// product persistence authorities that create objects owned by an access
// principal. It deliberately has no dependency on the access capability so
// capability adapters can use the same lifecycle serialization without
// importing one another.
package accesslifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the caller-owned PostgreSQL transaction surface. The helper never
// begins, commits, or rolls back the transaction supplied by its caller.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// LockOwnedPrincipal serializes a principal-owned object writer with access
// offboarding and verifies that the principal is still active. Callers must
// invoke it on the same transaction immediately before inserting or mutating
// an owned root. The global authority lock is transaction-scoped and shared
// with access' lifecycle operations.
func LockOwnedPrincipal(ctx context.Context, db DBTX, principalID string) error {
	if db == nil {
		return errors.New("principal ownership transaction is nil")
	}
	id, err := uuid.Parse(principalID)
	if err != nil {
		return fmt.Errorf("principal id must be a canonical UUID: %w", err)
	}
	canonicalID := id.String()
	if _, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('leapview.platform-role-authority', 0))`); err != nil {
		return fmt.Errorf("lock principal ownership authority: %w", err)
	}
	var active bool
	err = db.QueryRow(ctx, `
		SELECT status = 'active' AND disabled_at IS NULL AND blocked_at IS NULL
		FROM access.principal
		WHERE id = $1::uuid AND revoked_at IS NULL
		FOR UPDATE`, canonicalID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return fmt.Errorf("principal %q is not active", canonicalID)
	}
	if err != nil {
		return fmt.Errorf("lock principal lifecycle: %w", err)
	}
	return nil
}
