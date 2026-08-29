package module

import (
	"context"
	"database/sql"

	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
)

// Recover runs terminal-state recovery through an injected capability.  The
// module boundary does not expose database handles or select an engine.
func Recover(ctx context.Context, recovery TerminalRunRecovery, environment string) error {
	return RecoverWithPersistence(ctx, recovery, environment)
}

// RecoverSQLite is the explicit development/test adapter retained while the
// application composition migrates to PostgreSQL persistence injection.
func RecoverSQLite(ctx context.Context, database *sql.DB, environment string) error {
	return recoverSQLite(ctx, database, environment)
}

func recoverSQLite(ctx context.Context, database *sql.DB, environment string) error {
	if database == nil || environment == "" {
		return nil
	}
	constructor := refreshsqlite.NewSQLRunRepository
	return constructor(database).FailRunsForTerminalServingStates(ctx, environment, "refresh did not complete")
}
