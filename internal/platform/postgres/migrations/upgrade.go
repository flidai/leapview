package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

// ApplyDemoUpgrade is the bounded 028 -> 030 control-schema execution primitive.
// It is NOT admission or a general migration command. The production caller
// must hold the stopped-target fence and supply authoritative artifact/target/
// frontier revalidation. No migration SQL is accepted from a file or caller.
// River and DuckLake changes are outside this upgrade and must be rejected by
// admission. A partial attempt requires paired provider recovery, not blind retry.
func ApplyDemoUpgrade(ctx context.Context, pool *pgxpool.Pool, db *sql.DB, admit func(context.Context) error, after func(context.Context, *sql.DB) error) error {
	if pool == nil || db == nil || admit == nil || after == nil {
		return errors.New("upgrade requires migration pools, authoritative admission and role-policy reconciliation")
	}
	if CurrentRevision != 30 {
		return errors.New("demo upgrade is qualified only for embedded schema 30")
	}
	if ctx == nil {
		return errors.New("upgrade context is required")
	}
	return WithMigrationFence(ctx, pool, func() error {
		if err := admit(ctx); err != nil {
			return fmt.Errorf("revalidate demo upgrade admission: %w", err)
		}
		// This path is deliberately incapable of upgrading River incidentally.
		if err := VerifyRiver(ctx, pool); err != nil {
			return fmt.Errorf("demo upgrade requires unchanged River schema: %w", err)
		}
		provider, err := newProviderWithoutLock(db, migrationFiles)
		if err != nil {
			return err
		}
		current, target, err := provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		if err := validateUpgradeBoundary(current, target, statuses); err != nil {
			return err
		}
		if _, err = provider.Up(ctx); err != nil {
			return fmt.Errorf("apply demo migrations; paired recovery required: %w", err)
		}
		if err = after(ctx, db); err != nil {
			return fmt.Errorf("reconcile upgraded role policy; paired recovery required: %w", err)
		}
		observed, available, err := provider.GetVersions(ctx)
		if err != nil {
			return err
		}
		if observed != 30 || available != 30 {
			return fmt.Errorf("upgraded schema is %d/%d, expected 30/30", observed, available)
		}
		statuses, err = provider.Status(ctx)
		if err != nil {
			return err
		}
		if len(statuses) != 30 {
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
	if current != 28 || target != 30 || len(statuses) != 30 {
		return fmt.Errorf("unsupported demo migration boundary %d -> %d; require complete 28 -> 30 history", current, target)
	}
	for index, s := range statuses {
		if s == nil || s.Source == nil || s.Source.Version != int64(index+1) {
			return errors.New("incomplete or out-of-order demo migration history")
		}
		expected := goose.StateApplied
		if index >= 28 {
			expected = goose.StatePending
		}
		if s.State != expected {
			return fmt.Errorf("unexpected migration %d state %s", s.Source.Version, s.State)
		}
	}
	return nil
}
