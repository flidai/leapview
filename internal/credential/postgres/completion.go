package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CompleteActivationTx releases the durable operation fence only after the
// caller has installed the exact committed runtime and closed predecessor
// handles. The callback takes the target fence and verifies that the committed
// publication remains current; it must not perform provider I/O inside tx.
// The caller must observe the outer commit before reopening provider admission.
func (r *Repository) CompleteActivationTx(ctx context.Context, tx Tx, prepared credential.PreparedActivation, authorize ActivationCommitAuthorizer) (credential.PreparedActivation, error) {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if prepared.Validate() != nil || prepared.CommittedAt.IsZero() || !prepared.CompletedAt.IsZero() {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	defer func() { _ = savepoint.Rollback(context.Background()) }()
	if err := requireReadCommitted(ctx, savepoint, prepared.Preparation.Receipt.Binding.DeploymentID); err != nil {
		return credential.PreparedActivation{}, err
	}
	if err := authorize(ctx, savepoint, prepared); err != nil {
		return credential.PreparedActivation{}, err
	}
	intent, err := prepared.CompletionAuditIntent(uuid.NewString())
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	at, err := credentialdb.New(savepoint).CompleteActivation(ctx, credentialdb.CompleteActivationParams{
		DeploymentID: prepared.Preparation.Receipt.Binding.DeploymentID, OperationID: prepared.Preparation.OperationID,
		ExpectedCommittedAt: prepared.CommittedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if !at.Valid || at.Time.IsZero() {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if err := r.audit.RecordAuditEvent(ctx, savepoint, intent); err != nil {
		return credential.PreparedActivation{}, err
	}
	prepared.CompletedAt = at.Time
	if err := prepared.Validate(); err != nil {
		return credential.PreparedActivation{}, err
	}
	if err := savepoint.Commit(ctx); err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	return prepared, nil
}
