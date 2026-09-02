package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/app/postgresducklake"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// postgresControlPlaneLifecycle owns the runtime, required maintenance, and
// optional readonly control pools plus independent DuckLake runtime and
// maintenance pools after the one-shot migrator pool has applied the
// baseline. The migrator is closed immediately after commit and is therefore
// never available to request handlers. Start re-pings the serving pools so
// readiness is tied to the exact connections retained by the process rather
// than to migration success.
type postgresControlPlaneLifecycle struct {
	pools               *platformpostgres.ControlPlanePools
	ducklake            *platformpostgres.Pool
	ducklakeMaintenance *platformpostgres.Pool
	stop                sync.Once
	err                 error
}

func openPostgresControlPlane(ctx context.Context, cfg config.Config) (*postgresControlPlaneLifecycle, error) {
	if err := cfg.ValidatePostgresProduction(); err != nil {
		return nil, err
	}
	pools, err := platformpostgres.OpenControlPlane(ctx, cfg.PostgresControlPlaneConfig())
	if err != nil {
		return nil, err
	}
	if pools == nil || pools.Migrator == nil || pools.Runtime == nil || pools.Maintenance == nil {
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
	maintenanceDatabase, err := postgresDatabaseName(ctx, pools.Maintenance)
	if err != nil {
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL control maintenance database: %w", err)
	}
	if maintenanceDatabase != runtimeDatabase {
		pools.Close()
		return nil, fmt.Errorf("PostgreSQL control maintenance database %q differs from runtime database %q", maintenanceDatabase, runtimeDatabase)
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
	// The runtime URL is a credential selector, not the catalog identity
	// authority. Fail closed unless it lands on the dedicated DuckLake
	// database, and re-check both login and session roles through the existing
	// capability-owned static probe. This prevents a syntactically valid URL
	// from silently attaching a control or cross-environment database.
	ducklakeConfig := cfg.PostgresDuckLakeRuntimeConfig()
	ducklakeIdentity, err := postgresducklake.ReadDatabaseIdentity(ctx, ducklake)
	if err != nil {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL DuckLake runtime credentials: %w", err)
	}
	if err := validatePostgresDuckLakeRuntimeIdentity(ducklakeDatabase, ducklakeIdentity, ducklakeConfig.RuntimeRole); err != nil {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("validate PostgreSQL DuckLake runtime credentials: %w", err)
	}
	if ducklakeDatabase == runtimeDatabase {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("PostgreSQL control and DuckLake authorities resolve to the same database %q", runtimeDatabase)
	}
	ducklakeMaintenance, err := platformpostgres.Open(ctx, cfg.PostgresDuckLakeMaintenanceConfig())
	if err != nil {
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("open PostgreSQL DuckLake maintenance pool: %w", err)
	}
	ducklakeMaintenanceDatabase, err := postgresDatabaseName(ctx, ducklakeMaintenance)
	if err != nil {
		ducklakeMaintenance.Close()
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL DuckLake maintenance database: %w", err)
	}
	maintenanceIdentity, err := postgresducklake.ReadDatabaseIdentity(ctx, ducklakeMaintenance)
	if err != nil {
		ducklakeMaintenance.Close()
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("identify PostgreSQL DuckLake maintenance credentials: %w", err)
	}
	maintenanceConfig := cfg.PostgresDuckLakeMaintenanceConfig()
	if err := validatePostgresDuckLakeMaintenanceIdentity(ducklakeMaintenanceDatabase, maintenanceIdentity, maintenanceConfig.RuntimeRole); err != nil {
		ducklakeMaintenance.Close()
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("validate PostgreSQL DuckLake maintenance credentials: %w", err)
	}
	if ducklakeMaintenanceDatabase != ducklakeDatabase {
		ducklakeMaintenance.Close()
		ducklake.Close()
		pools.Close()
		return nil, fmt.Errorf("PostgreSQL DuckLake maintenance database %q differs from runtime database %q", ducklakeMaintenanceDatabase, ducklakeDatabase)
	}
	return &postgresControlPlaneLifecycle{pools: pools, ducklake: ducklake, ducklakeMaintenance: ducklakeMaintenance}, nil
}

// validatePostgresDuckLakeRuntimeIdentity is the production admission seam
// for the separately authenticated DuckLake pool. Both the pool's current
// database probe and PostgreSQL's login/session identity must agree with the
// dedicated catalog contract before serving composition proceeds.
func validatePostgresDuckLakeRuntimeIdentity(database string, identity postgresducklake.DatabaseIdentity, expectedRole string) error {
	if database != postgresducklake.DefaultDuckLakeDatabase {
		return fmt.Errorf("%w: PostgreSQL DuckLake runtime database %q differs from required database %q", postgresducklake.ErrWrongDatabaseCredential, database, postgresducklake.DefaultDuckLakeDatabase)
	}
	if identity.Database != database {
		return fmt.Errorf("%w: PostgreSQL DuckLake identity database %q differs from pool database %q", postgresducklake.ErrWrongDatabaseCredential, identity.Database, database)
	}
	return postgresducklake.ValidateDatabaseIdentity(identity, database, expectedRole)
}

// validatePostgresDuckLakeMaintenanceIdentity applies the same database and
// login/session-role checks as the runtime pool, but is kept as a distinct
// composition seam so maintenance credentials cannot be accidentally reused
// by ordinary serving code.
func validatePostgresDuckLakeMaintenanceIdentity(database string, identity postgresducklake.DatabaseIdentity, expectedRole string) error {
	return validatePostgresDuckLakeRuntimeIdentity(database, identity, expectedRole)
}

func postgresDatabaseName(ctx context.Context, pool *platformpostgres.Pool) (string, error) {
	if pool == nil {
		return "", errors.New("PostgreSQL pool is nil")
	}
	database, err := pool.CurrentDatabase(ctx)
	if err != nil {
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
	if err := postgresbaseline.Apply(ctx, tx); err != nil {
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
	if l == nil || l.pools == nil || l.pools.Runtime == nil || l.pools.Maintenance == nil || l.ducklake == nil || l.ducklakeMaintenance == nil {
		return errors.New("PostgreSQL control-plane lifecycle is not initialized")
	}
	if err := l.pools.Runtime.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL control runtime pool: %w", err)
	}
	if err := l.pools.Maintenance.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL control maintenance pool: %w", err)
	}
	if err := postgresbaseline.Verify(ctx, l.pools.Runtime); err != nil {
		return fmt.Errorf("verify PostgreSQL control schema revision: %w", err)
	}
	if l.pools.Readonly != nil {
		if err := l.pools.Readonly.Ping(ctx); err != nil {
			return fmt.Errorf("ping PostgreSQL control readonly pool: %w", err)
		}
	}
	if err := l.ducklake.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL DuckLake runtime pool: %w", err)
	}
	if err := l.ducklakeMaintenance.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL DuckLake maintenance pool: %w", err)
	}
	return nil
}

// MaintenancePool exposes the retained, independently authenticated
// one-connection control pool used by native maintenance coordinators. The
// lifecycle retains ownership and closes it from Stop.
func (l *postgresControlPlaneLifecycle) MaintenancePool() *platformpostgres.Pool {
	if l == nil || l.pools == nil {
		return nil
	}
	return l.pools.Maintenance
}

// RuntimePool exposes the retained ordinary control-plane pool to the
// application-owned authority graph. The lifecycle remains the sole owner and
// closes it during Stop.
func (l *postgresControlPlaneLifecycle) RuntimePool() *platformpostgres.Pool {
	if l == nil || l.pools == nil {
		return nil
	}
	return l.pools.Runtime
}

// DuckLakePool exposes only the separately authenticated external
// leapview_ducklake catalog pool to native runtime attach composition without
// transferring lifecycle ownership. It must not be used to construct
// PostgresAuthorityGraph or its control-plane DuckLake ledger.
func (l *postgresControlPlaneLifecycle) DuckLakePool() *platformpostgres.Pool {
	if l == nil {
		return nil
	}
	return l.ducklake
}

// DuckLakeMaintenancePool exposes the separately authenticated, one-
// connection DuckLake maintenance pool to the retention composition. The
// lifecycle retains ownership and closes it during Stop; callers must not
// transfer or reuse it as a runtime catalog pool.
func (l *postgresControlPlaneLifecycle) DuckLakeMaintenancePool() *platformpostgres.Pool {
	if l == nil {
		return nil
	}
	return l.ducklakeMaintenance
}

// Stop closes all serving pools and is idempotent.  It deliberately accepts a
// context to satisfy app.Lifecycle; pgxpool close itself is synchronous and
// does not require a cancellation path.
func (l *postgresControlPlaneLifecycle) Stop(context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.ducklakeMaintenance != nil {
			l.ducklakeMaintenance.Close()
		}
		if l.ducklake != nil {
			l.ducklake.Close()
		}
		if l.pools != nil {
			l.pools.Close()
		}
	})
	return l.err
}
