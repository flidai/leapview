package sqlite

// SQLite PRAGMA statements are connection-local session controls rather than
// schema-backed queries, so sqlc cannot model them. Keep this capability
// helper in the authored persistence adapter instead of the generated db
// package, which is recreated during every build.

import (
	"context"
	"database/sql"
	"fmt"
)

func setBusyTimeout(ctx context.Context, tx *sql.Tx, milliseconds int) error {
	if tx == nil || milliseconds < 0 {
		return fmt.Errorf("invalid sqlite busy timeout")
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", milliseconds))
	return err
}
