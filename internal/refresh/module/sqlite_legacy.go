package module

import (
	"context"
	"database/sql"

	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
)

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
