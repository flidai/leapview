package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapTransitionOperation prepares the revision-020 operation authority
// while Goose still records revision 019. Callers must use the explicit
// migrator connection before creating a transition operation. The normal
// ApplyRiverAndGoose path subsequently applies migration 020 and records its
// revision; this bootstrap never writes Goose's version table.
func BootstrapTransitionOperation(ctx context.Context, pool *pgxpool.Pool, db *sql.DB) error {
	if pool == nil || db == nil {
		return errors.New("transition bootstrap requires PostgreSQL migrator connections")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return WithMigrationFence(ctx, pool, func() error {
		provider, err := newProviderWithoutLock(db, migrationFiles)
		if err != nil {
			return err
		}
		current, _, err := provider.GetVersions(ctx)
		if err != nil {
			return fmt.Errorf("read transition bootstrap revision: %w", err)
		}
		if current < 19 {
			return fmt.Errorf("transition bootstrap requires revision 019 or later, got %d", current)
		}
		if current >= 20 {
			return verifyTransitionBootstrapTables(ctx, db)
		}
		statements, err := transitionBootstrapSQL()
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin transition bootstrap: %w", err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, statements); err != nil {
			return fmt.Errorf("apply migration-owned transition bootstrap: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit transition bootstrap: %w", err)
		}
		if err := verifyTransitionBootstrapTables(ctx, db); err != nil {
			return err
		}
		current, _, err = provider.GetVersions(ctx)
		if err != nil {
			return fmt.Errorf("read transition bootstrap revision after commit: %w", err)
		}
		if current != 19 {
			return fmt.Errorf("transition bootstrap changed Goose revision to %d", current)
		}
		return nil
	})
}

// Migration 020 is the sole DDL source for this authority. Bootstrap installs
// the same truncate guards under distinct names so migration 020 can still
// create its own non-idempotent triggers after the runner's migration phase.
func transitionBootstrapSQL() (string, error) {
	source, err := migrationFiles.ReadFile("020_release_transition_operation.sql")
	if err != nil {
		return "", err
	}
	up, _, found := strings.Cut(string(source), "-- +goose Down")
	if !found {
		return "", errors.New("transition migration has no Down boundary")
	}
	for _, guard := range []struct{ name, table string }{
		{"release_transition_operation_no_truncate", "release.release_transition_operation"},
		{"release_transition_phase_no_truncate", "release.release_transition_phase_result"},
		{"release_transition_fence_no_truncate", "release.release_transition_fence"},
	} {
		prefix := "CREATE TRIGGER " + guard.name + " BEFORE TRUNCATE "
		start := strings.Index(up, prefix)
		if start < 0 || strings.Count(up, prefix) != 1 {
			return "", fmt.Errorf("transition migration has no unique %s trigger", guard.name)
		}
		end := strings.IndexByte(up[start:], '\n')
		if end < 0 {
			return "", fmt.Errorf("transition migration has unterminated %s trigger", guard.name)
		}
		statement := up[start : start+end]
		bootstrapName := guard.name + "_bootstrap"
		bootstrapStatement := strings.Replace(statement, guard.name, bootstrapName, 1)
		// The transaction makes replacement atomic to other sessions on a retry.
		replacement := "DROP TRIGGER IF EXISTS " + bootstrapName + " ON " + guard.table + ";\n" + bootstrapStatement + "\n"
		up = up[:start] + replacement + up[start+end+1:]
	}
	return up, nil
}

func verifyTransitionBootstrapTables(ctx context.Context, db *sql.DB) error {
	for _, name := range []string{
		"release.release_transition_operation",
		"release.release_transition_phase_result",
		"release.release_transition_fence",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
			return fmt.Errorf("verify transition bootstrap table %s: %w", name, err)
		}
		if !exists {
			return fmt.Errorf("transition bootstrap table %s is missing", name)
		}
	}
	return nil
}
