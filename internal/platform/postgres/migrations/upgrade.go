package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// ApplyUpgrade executes a preflight-bound forward control-schema transition.
// The caller fences writers and supplies admission under the canonical database
// lock. River must already match; its upgrades require a separate provider path.
func ApplyUpgrade(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, expected int64, admit func(context.Context) error, after func(context.Context, *sql.DB) error) error {
	return applyUpgrade(ctx, pool, db, migrationFiles, CurrentRevision, expected, admit, after)
}
func applyUpgrade(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, source fs.FS, revision, expected int64, admit func(context.Context) error, after func(context.Context, *sql.DB) error) error {
	if pool == nil || db == nil || admit == nil || after == nil {
		return errors.New("upgrade requires migration pools, authoritative admission and role-policy reconciliation")
	}
	if ctx == nil {
		return errors.New("upgrade context is required")
	}
	return WithMigrationFence(ctx, pool, func() error {
		if err := admit(ctx); err != nil {
			return fmt.Errorf("revalidate host upgrade admission: %w", err)
		}
		// This path is deliberately incapable of upgrading River incidentally.
		if err := VerifyRiver(ctx, pool); err != nil {
			return fmt.Errorf("host upgrade requires unchanged River schema: %w", err)
		}
		provider, err := newProviderWithoutLock(db, source)
		if err != nil {
			return err
		}
		current, target, err := provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		if target != revision {
			return errors.New("candidate schema and embedded migrations disagree")
		}
		if current != expected {
			return fmt.Errorf("live schema %d differs from admitted predecessor %d", current, expected)
		}
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		if err := validateUpgradeBoundary(current, target, statuses); err != nil {
			return err
		}
		if _, err = provider.Up(ctx); err != nil {
			return fmt.Errorf("apply host migrations; paired recovery required: %w", err)
		}
		if err = after(ctx, db); err != nil {
			return fmt.Errorf("reconcile upgraded role policy; paired recovery required: %w", err)
		}
		observed, available, err := provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		if observed != revision || available != revision {
			return fmt.Errorf("upgraded schema is %d/%d, expected %d", observed, available, revision)
		}
		statuses, err = provider.Status(ctx)
		if err != nil {
			return err
		}
		if len(statuses) != int(revision) {
			return errors.New("incomplete upgraded migration history")
		}
		for _, s := range statuses {
			if s == nil || s.State != goose.StateApplied {
				return errors.New("unapplied migration after upgrade")
			}
		}
		return nil
	})
}
func validateUpgradeBoundary(current, target int64, statuses []*goose.MigrationStatus) error {
	if current < BaselineRevision || target < current || len(statuses) != int(target) {
		return fmt.Errorf("unsupported migration boundary %d -> %d", current, target)
	}
	for index, s := range statuses {
		if s == nil || s.Source == nil || s.Source.Version != int64(index+1) {
			return errors.New("incomplete or out-of-order host migration history")
		}
		expected := goose.StateApplied
		if int64(index) >= current {
			expected = goose.StatePending
		}
		if s.State != expected {
			return fmt.Errorf("unexpected migration %d state %s", s.Source.Version, s.State)
		}
	}
	return nil
}
