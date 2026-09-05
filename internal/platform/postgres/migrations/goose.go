package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	// BaselineRevision is the first clean-slate control-plane migration.
	BaselineRevision int64 = 1
	// BaselineMigrationID is the immutable source name of the baseline file.
	BaselineMigrationID = "001_control_plane"
	// AdvisoryLockKey serializes migration attempts across instances. Goose
	// owns acquisition and release of this session-level PostgreSQL lock. The
	// combined River+Goose path below uses the same key for one shared fence.
	AdvisoryLockKey int64 = 0x4c565f7067730001

	migrationLockTimeout       = 5 * time.Minute
	migrationLockRetryInterval = time.Second
	migrationUnlockTimeout     = time.Minute
)

// SQL migration files are the only schema source of truth. Keep the embedded
// filesystem private so callers cannot register ad-hoc migrations at runtime;
// new revisions must be reviewed and checked in under this directory.
//
//go:embed *.sql
var migrationFiles embed.FS

// MigrationFS returns a read-only view for tooling that needs to inspect the
// exact embedded migration set (for example, an upgrade preflight). The
// returned filesystem cannot mutate the checked-in assets.
func MigrationFS() fs.FS { return migrationFiles }

// NewProvider builds the standard PostgreSQL Goose provider. Goose owns the
// goose_db_version table, migration ordering, transaction boundaries, and the
// advisory session lock. No custom version store or checksum ledger is used.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	return newProvider(db, migrationFiles)
}

func newProvider(db *sql.DB, source fs.FS) (*goose.Provider, error) {
	return newProviderWithLock(db, source, true)
}

func newProviderWithoutLock(db *sql.DB, source fs.FS) (*goose.Provider, error) {
	return newProviderWithLock(db, source, false)
}

func newProviderWithLock(db *sql.DB, source fs.FS, withLock bool) (*goose.Provider, error) {
	if db == nil {
		return nil, errors.New("PostgreSQL Goose migration database is nil")
	}
	if source == nil {
		return nil, errors.New("PostgreSQL Goose migration filesystem is nil")
	}
	options := []goose.ProviderOption{goose.WithVerbose(false)}
	if withLock {
		gooseLock, err := lock.NewPostgresSessionLocker(
			lock.WithLockID(AdvisoryLockKey),
			lock.WithLockTimeout(1, 300),
			lock.WithUnlockTimeout(1, 60),
		)
		if err != nil {
			return nil, fmt.Errorf("configure PostgreSQL Goose advisory lock: %w", err)
		}
		options = append(options, goose.WithSessionLocker(gooseLock))
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		source,
		options...,
	)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL Goose provider: %w", err)
	}
	return provider, nil
}

// ApplyGoose runs all pending immutable SQL migrations. It is intended only
// for an explicit migration/upgrade operation; serving startup must call
// VerifyGoose instead.
func ApplyGoose(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("PostgreSQL Goose migration database is nil")
	}
	// Explicit CLI/admin migration boundary: normalize before running the
	// provider so every migration statement shares one operation context.
	if ctx == nil {
		ctx = context.Background()
	}
	provider, err := NewProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply PostgreSQL Goose migrations: %w", err)
	}
	return nil
}

// ApplyRiverAndGoose applies the upstream River schema and the product Goose
// baseline while one session-level advisory lock fences the complete
// operation. The optional afterGoose hook runs under that same fence, which
// lets callers reconcile dependent product policy without reopening a lock.
//
// This is the only combined initialization path. Goose's own session locker
// is deliberately disabled for the inner call because the fence is held on a
// separate session; enabling both would deadlock when the migrator pool has a
// single connection.
func ApplyRiverAndGoose(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, afterGoose func(context.Context, *sql.DB) error) error {
	if pool == nil {
		return errors.New("PostgreSQL River migration pool is nil")
	}
	if db == nil {
		return errors.New("PostgreSQL Goose migration database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return WithMigrationFence(ctx, pool, func() error {
		if err := ApplyRiver(ctx, pool); err != nil {
			return err
		}
		if err := applyGooseWithoutLock(ctx, db); err != nil {
			return err
		}
		if afterGoose != nil {
			if err := afterGoose(ctx, db); err != nil {
				return err
			}
		}
		return nil
	})
}

func applyGooseWithoutLock(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("PostgreSQL Goose migration database is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider, err := newProviderWithoutLock(db, migrationFiles)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply PostgreSQL Goose migrations: %w", err)
	}
	return nil
}

// WithMigrationFence runs fn while holding the advisory lock shared with
// Goose. The fence session is a short-lived independent pool so a one-
// connection River/Goose migrator pool remains usable by the operation.
func WithMigrationFence(ctx context.Context, pool *pgxpool.Pool, fn func() error) (err error) {
	if pool == nil {
		return errors.New("PostgreSQL migration fence pool is nil")
	}
	if fn == nil {
		return errors.New("PostgreSQL migration fence function is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fenceConfig := pool.Config()
	fenceConfig.MinConns = 1
	fenceConfig.MaxConns = 1
	fencePool, err := pgxpool.NewWithConfig(ctx, fenceConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL migration fence pool: %w", err)
	}
	defer fencePool.Close()
	conn, err := fencePool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire PostgreSQL migration fence connection: %w", err)
	}
	defer conn.Release()

	lockCtx, cancel := context.WithTimeout(ctx, migrationLockTimeout)
	defer cancel()
	if err := acquireMigrationFence(lockCtx, conn); err != nil {
		return fmt.Errorf("acquire PostgreSQL migration fence: %w", err)
	}
	defer func() {
		unlockErr := releaseMigrationFence(conn)
		if unlockErr != nil {
			if err == nil {
				err = fmt.Errorf("release PostgreSQL migration fence: %w", unlockErr)
			} else {
				err = errors.Join(err, fmt.Errorf("release PostgreSQL migration fence: %w", unlockErr))
			}
		}
	}()

	return fn()
}

func acquireMigrationFence(ctx context.Context, conn *pgxpool.Conn) error {
	for {
		var acquired bool
		// sqlc-exception: analyzer-incompatible. pg_try_advisory_lock is
		// connection-local migration-fence protocol and must run on this
		// dedicated session; it is not a generated capability query.
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, AdvisoryLockKey).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			return nil
		}
		timer := time.NewTimer(migrationLockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func releaseMigrationFence(conn *pgxpool.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), migrationUnlockTimeout)
	defer cancel()
	var released bool
	// sqlc-exception: analyzer-incompatible. pg_advisory_unlock is
	// connection-local migration-fence protocol and must run on the same
	// dedicated session that acquired the lock.
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, AdvisoryLockKey).Scan(&released); err != nil {
		return err
	}
	if !released {
		return errors.New("migration fence was not held by the current session")
	}
	return nil
}

// ApplyRiver installs River's upstream-owned schema through the same explicit
// migration/admin credential as the product Goose baseline.
func ApplyRiver(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("PostgreSQL River migration pool is nil")
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("construct River migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply River migrations: %w", err)
	}
	// River's tables are upstream-owned, but LeapView's durable owner role
	// installs the attempt-qualified worker-result trigger in the product
	// baseline. Grant only that DDL capability from the migration login which
	// owns the freshly installed River table.
	// sqlc-exception: schema-ddl. This conditional grant targets an upstream-owned
	// table during explicit migration, before the product baseline creates the trigger.
	if _, err := pool.Exec(ctx, `
DO $river_trigger_authority$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        EXECUTE 'GRANT TRIGGER ON TABLE public.river_job TO leapview_control_owner';
    END IF;
END
$river_trigger_authority$`); err != nil {
		return fmt.Errorf("grant River result-fence trigger authority: %w", err)
	}
	return nil
}

// VerifyRiver fails closed when the installed upstream River schema is
// missing, partial, or ahead of the version compiled into this binary.
func VerifyRiver(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("PostgreSQL River verification pool is nil")
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("construct River migrator: %w", err)
	}
	existing, err := migrator.ExistingVersions(ctx)
	if err != nil {
		return fmt.Errorf("read River schema versions: %w", err)
	}
	all := migrator.AllVersions()
	existingVersions := make([]int, len(existing))
	allVersions := make([]int, len(all))
	for i := range existing {
		existingVersions[i] = existing[i].Version
	}
	for i := range all {
		allVersions[i] = all[i].Version
	}
	if !slices.Equal(existingVersions, allVersions) {
		return fmt.Errorf("River schema versions %v do not match required versions %v", existingVersions, allVersions)
	}
	return nil
}

// VerifyGoose checks the authoritative Goose version table without applying
// anything. A missing, partial, out-of-order, or newer migration set fails
// closed and leaves migration execution to the explicit upgrade command.
func VerifyGoose(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("PostgreSQL Goose verification database is nil")
	}
	// Startup/upgrade verification boundary: normalize before reading Goose
	// status so all checks share one operation context.
	if ctx == nil {
		ctx = context.Background()
	}
	provider, err := NewProvider(db)
	if err != nil {
		return err
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("read PostgreSQL Goose schema version: %w", err)
	}
	if current != target {
		return fmt.Errorf("PostgreSQL Goose schema version %d does not match required version %d", current, target)
	}
	statuses, err := provider.Status(ctx)
	if err != nil {
		return fmt.Errorf("inspect PostgreSQL Goose migration status: %w", err)
	}
	for _, status := range statuses {
		if status == nil || status.State != goose.StateApplied {
			if status == nil || status.Source == nil {
				return errors.New("PostgreSQL Goose migration status is incomplete")
			}
			return fmt.Errorf("PostgreSQL Goose migration %d (%s) is not applied", status.Source.Version, status.Source.Path)
		}
	}
	return nil
}

// ReconcileRolePolicy reapplies the product-owned deny-by-default ACL policy
// in a separate transaction. Goose never provisions roles or broadens ACLs;
// role creation, passwords, and grants for the canonical authorities remain
// externally managed by the deployment/operator workflow.
func ReconcileRolePolicy(ctx context.Context, db *sql.DB, rolePolicySQL string) error {
	if db == nil {
		return errors.New("PostgreSQL role-policy database is nil")
	}
	if rolePolicySQL == "" {
		return errors.New("PostgreSQL role policy is empty")
	}
	// Explicit CLI/admin ACL reconciliation boundary: normalize before opening
	// the transaction and applying the complete role policy.
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin PostgreSQL role-policy transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	// Goose creates its standard version table as the migrator login before it
	// executes the first migration. Reconcile its read boundary while still
	// using that owning login, then assume LeapView's durable owner role for
	// the product-owned objects below.
	if _, err := tx.ExecContext(ctx, `
REVOKE ALL ON TABLE public.goose_db_version FROM PUBLIC,
    leapview_control_runtime, leapview_control_readonly, leapview_control_backup;
GRANT SELECT ON TABLE public.goose_db_version TO
    leapview_control_runtime, leapview_control_readonly, leapview_control_backup;
DO $river_policy$
DECLARE relation_name text;
BEGIN
    FOR relation_name IN
        SELECT c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public' AND c.relname LIKE 'river_%'
           AND c.relkind IN ('r', 'p')
    LOOP
        EXECUTE format('REVOKE ALL ON TABLE public.%I FROM PUBLIC, leapview_control_readonly', relation_name);
        EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.%I TO leapview_control_runtime', relation_name);
        EXECUTE format('GRANT SELECT ON TABLE public.%I TO leapview_control_backup', relation_name);
    END LOOP;
    FOR relation_name IN
        SELECT c.relname
          FROM pg_class c
          JOIN pg_namespace n ON n.oid = c.relnamespace
         WHERE n.nspname = 'public' AND c.relname LIKE 'river_%'
           AND c.relkind = 'S'
    LOOP
        EXECUTE format('REVOKE ALL ON SEQUENCE public.%I FROM PUBLIC, leapview_control_readonly', relation_name);
        EXECUTE format('GRANT USAGE, SELECT, UPDATE ON SEQUENCE public.%I TO leapview_control_runtime', relation_name);
        EXECUTE format('GRANT SELECT ON SEQUENCE public.%I TO leapview_control_backup', relation_name);
    END LOOP;
END
$river_policy$;
`); err != nil {
		return fmt.Errorf("reconcile PostgreSQL Goose version-table policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL ROLE leapview_control_owner`); err != nil {
		return fmt.Errorf("assume PostgreSQL role-policy owner role: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
GRANT USAGE ON SCHEMA public TO
    leapview_control_runtime, leapview_control_readonly, leapview_control_backup;
`); err != nil {
		return fmt.Errorf("reconcile PostgreSQL public-schema policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, rolePolicySQL); err != nil {
		return fmt.Errorf("apply PostgreSQL role policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit PostgreSQL role policy: %w", err)
	}
	committed = true
	return nil
}
