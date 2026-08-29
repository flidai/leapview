package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flidai/leapview/internal/app/config"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
)

// errPostgresProductionCompositionIncomplete is intentionally stable so the
// serve command can fail closed while the remaining capability adapters are
// being migrated.  A production process must never fall back to the legacy
// SQLite authority after PostgreSQL bootstrap has been requested.
var errPostgresProductionCompositionIncomplete = errors.New("production PostgreSQL control-plane adapters are not yet wired; refusing SQLite runtime")

// postgresControlPlaneLifecycle owns the runtime and optional readonly pools
// after the one-shot migrator pool has applied the baseline.  The migrator is
// closed immediately after commit and is therefore never available to request
// handlers.  Start re-pings the serving pools so readiness is tied to the
// exact connections retained by the process rather than to migration success.
type postgresControlPlaneLifecycle struct {
	pools    *platformpostgres.ControlPlanePools
	ducklake *platformpostgres.Pool
	stop     sync.Once
	err      error
}

func openPostgresControlPlane(ctx context.Context, cfg config.Config) (*postgresControlPlaneLifecycle, error) {
	if err := cfg.ValidatePostgresProduction(); err != nil {
		return nil, err
	}
	pools, err := platformpostgres.OpenControlPlane(ctx, cfg.PostgresControlPlaneConfig())
	if err != nil {
		return nil, err
	}
	if pools == nil || pools.Migrator == nil || pools.Runtime == nil {
		if pools != nil {
			pools.Close()
		}
		return nil, errors.New("PostgreSQL control-plane pools are incomplete")
	}
	migratorDatabase, err := postgresDatabaseName(ctx, pools.Migrator)
	if err != nil {
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL control migrator database: %w", err)
	}
	runtimeDatabase, err := postgresDatabaseName(ctx, pools.Runtime)
	if err != nil {
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL control runtime database: %w", err)
	}
	if migratorDatabase != runtimeDatabase {
		pools.Close()
		return nil, fmt.Errorf("PostgreSQL control migrator database %q differs from runtime database %q", migratorDatabase, runtimeDatabase)
	}
	if err := applyPostgresControlPlaneMigrations(ctx, pools.Migrator); err != nil {
		pools.Close()
		return nil, err
	}
	// Migration credentials are privileged and must not remain in the serving
	// process.  Closing this pool also releases any connection that assumed the
	// owner role during DDL.
	pools.Migrator.Close()
	pools.Migrator = nil
	ducklake, err := platformpostgres.Open(ctx, cfg.PostgresDuckLakeRuntimeConfig())
	if err != nil {
		pools.Close()
		return nil, fmt.Errorf("open PostgreSQL DuckLake runtime pool: %w", err)
	}
	ducklakeDatabase, err := postgresDatabaseName(ctx, ducklake)
	if err != nil {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL DuckLake runtime database: %w", err)
	}
	if ducklakeDatabase == runtimeDatabase {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("PostgreSQL control and DuckLake authorities resolve to the same database %q", runtimeDatabase)
	}
	return &postgresControlPlaneLifecycle{pools: pools, ducklake: ducklake}, nil
}

func postgresDatabaseName(ctx context.Context, pool *platformpostgres.Pool) (string, error) {
	if pool == nil {
		return "", errors.New("PostgreSQL pool is nil")
	}
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return "", err
	}
	if database == "" {
		return "", errors.New("PostgreSQL current_database() returned an empty identity")
	}
	return database, nil
}

func applyPostgresControlPlaneMigrations(ctx context.Context, migrator *platformpostgres.Pool) error {
	if migrator == nil {
		return errors.New("PostgreSQL control migrator pool is nil")
	}
	tx, err := migrator.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL control-plane migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := migrations.Apply(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL control-plane migrations: %w", err)
	}
	committed = true
	return nil
}

// Start validates the pools retained for serving.  It is safe to call more
// than once; each call rechecks the live database because a successful
// startup ping is not a perpetual readiness guarantee.
func (l *postgresControlPlaneLifecycle) Start(ctx context.Context) error {
	if l == nil || l.pools == nil || l.pools.Runtime == nil || l.ducklake == nil {
		return errors.New("PostgreSQL control-plane lifecycle is not initialized")
	}
	if err := l.pools.Runtime.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL control runtime pool: %w", err)
	}
	var revision int64
	var migrationID, checksum string
	if err := l.pools.Runtime.QueryRow(ctx, `
		SELECT revision, migration_id, checksum
		FROM platform.schema_revision
		WHERE revision = $1`, migrations.BaselineRevision).
		Scan(&revision, &migrationID, &checksum); err != nil {
		return fmt.Errorf("verify PostgreSQL control schema revision: %w", err)
	}
	if revision != migrations.BaselineRevision || migrationID != migrations.BaselineMigrationID || checksum != migrations.BaselineChecksum() {
		return fmt.Errorf("PostgreSQL control schema revision mismatch: got revision=%d migration=%q checksum=%q", revision, migrationID, checksum)
	}
	if l.pools.Readonly != nil {
		if err := l.pools.Readonly.Ping(ctx); err != nil {
			return fmt.Errorf("ping PostgreSQL control readonly pool: %w", err)
		}
	}
	if err := l.ducklake.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL DuckLake runtime pool: %w", err)
	}
	return nil
}

// Stop closes all serving pools and is idempotent.  It deliberately accepts a
// context to satisfy app.Lifecycle; pgxpool close itself is synchronous and
// does not require a cancellation path.
func (l *postgresControlPlaneLifecycle) Stop(context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.ducklake != nil {
			l.ducklake.Close()
		}
		if l.pools != nil {
			l.pools.Close()
		}
	})
	return l.err
}
