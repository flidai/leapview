package postgres

import (
	"context"
	"errors"
	"fmt"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// WithUnpublishedTarget serializes first-publication work with activation and
// other bootstrap work for the exact delivery target. The callback runs while
// the target row's mutation lock is held, so activation and other fences wait
// until it finishes while target foreign-key checks can proceed.
//
// The delivery transaction is always rolled back after the callback, including
// any temporary target row created before first planning. The callback owns
// its own authority transaction and must not rely on the delivery transaction
// for atomicity.
func (r *Repository) WithUnpublishedTarget(ctx context.Context, targetID, projectID, environment string, callback func(context.Context) error) error {
	if ctx == nil || callback == nil {
		return ErrInvalid
	}
	targetID, err := textID(targetID, "target id")
	if err != nil {
		return err
	}
	projectID, err = textID(projectID, "project id")
	if err != nil {
		return err
	}
	environment, err = textID(environment, "environment")
	if err != nil {
		return err
	}

	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	// A cancelled callback must still release the target lock. Rollback is
	// best-effort because the callback's result remains the useful error.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// The reviewer/bootstrap path can run before the first native plan has
	// durably created this target. Ensure a revision-1 row in this temporary
	// transaction, then lock and verify it. On conflict the insert is a no-op,
	// allowing an existing active target to be read and reported as such.
	queries := depdb.New(tx)
	if err := queries.InsertTarget(ctx, depdb.InsertTargetParams{TargetID: targetID, ProjectID: projectID, Environment: environment, TargetRevision: 1}); err != nil {
		return err
	}
	if err := queries.InsertTargetFence(ctx, targetID); err != nil {
		return err
	}
	_, err = depdb.New(tx).LockTargetForNoKeyUpdate(ctx, targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Read scope and both active pointers in a fresh statement after taking
	// the lock; a concurrent activation may have committed while we waited.
	target, err := loadTarget(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if target.TargetID != targetID || target.ProjectID != projectID || target.Environment != environment {
		return fmt.Errorf("%w: unpublished target scope differs", ErrConflict)
	}
	if target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
		return ErrAlreadyActive
	}

	return callback(ctx)
}
