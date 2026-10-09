package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ActivationCommitAuthorizer must verify the exact persisted candidate pin
// and lock/recheck current actor, customer owner, binding and configuration
// authority on tx. Delivery must already hold its publication, lease and target
// fences in that order. The callback must not perform provider I/O or end tx;
// its locks remain held through the caller's outer commit.
type ActivationCommitAuthorizer func(context.Context, Tx, credential.PreparedActivation) error

// CommitActivationPublicationTx consumes the reserved receipt at delivery's
// final admission gate, immediately before its pointer CAS. It MUST run inside
// the delivery activation savepoint: publication and this operation/audit must
// either all commit or all roll back. Receipt freshness is sampled at the
// guarded update's transition trigger, not at the later physical outer COMMIT. An expiry after that
// gate does not revoke the authorized publication. This method releases only
// its nested savepoint; its return is not an outer-commit acknowledgment.
// Committed operations remain pending until runtime readiness is confirmed.
func (r *Repository) CommitActivationPublicationTx(
	ctx context.Context,
	tx pgx.Tx,
	operationID string,
	publication credential.ActivationPublication,
	authorize ActivationCommitAuthorizer,
) (result credential.PreparedActivation, err error) {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if !canonicalPreparationUUID(operationID) || !canonical(publication.TargetID, 255) {
		return credential.PreparedActivation{}, credential.ErrInvalid
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	released := false
	defer func() {
		if !released {
			if rollbackErr := savepoint.Rollback(context.Background()); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("rollback credential commit savepoint: %w", rollbackErr))
				// pgx closes a child handle even when RELEASE fails. In that case
				// ErrTxClosed does not prove its writes have been rolled back.
				if outerErr := tx.Rollback(context.Background()); outerErr != nil {
					err = errors.Join(err, fmt.Errorf("rollback credential commit caller transaction: %w", outerErr))
				}
			}
		}
	}()
	if err := requireReadCommitted(ctx, savepoint, publication.TargetID); err != nil {
		return credential.PreparedActivation{}, err
	}
	queries := credentialdb.New(savepoint)
	row, err := queries.GetActivationPreparation(ctx, credentialdb.GetActivationPreparationParams{DeploymentID: publication.TargetID, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	prepared, err := activationPreparationFromRow(row)
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	if prepared.SwitchingAt.IsZero() || !prepared.AbortedAt.IsZero() || !prepared.CommittedAt.IsZero() || !publication.Matches(prepared.Preparation) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err := authorize(ctx, savepoint, prepared); err != nil {
		return credential.PreparedActivation{}, err
	}
	intent, err := prepared.CommitAuditIntent(uuid.NewString())
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	committedAt, err := queries.CommitActivationPublication(ctx, credentialdb.CommitActivationPublicationParams{
		DeploymentID: publication.TargetID, OperationID: operationID,
		ReceiptID: prepared.Preparation.Receipt.ReceiptID, ActorID: publication.ActorID,
		ExpectedTargetRevision: publication.ExpectedTargetRevision, PredecessorGenerationID: publication.PredecessorGenerationID,
		CandidateID: publication.CandidateID, GenerationID: publication.GenerationID, PublicationID: publication.PublicationID,
		CreatedAt: prepared.CreatedAt, ExpectedSwitchingAt: pgtype.Timestamptz{Time: prepared.SwitchingAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err != nil {
		// The preliminary freshness check can precede a row-lock wait. The
		// trigger samples again after that wait; stale proof is a conflict,
		// while unrelated database/serialization errors retain their meaning.
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "40001" && pgError.ConstraintName == "activation_preparation_receipt_freshness" {
			return credential.PreparedActivation{}, credential.ErrConflict
		}
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if !committedAt.Valid || committedAt.Time.IsZero() {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	prepared.CommittedAt = committedAt.Time
	if prepared.Validate() != nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if err := r.audit.RecordAuditEvent(ctx, savepoint, intent); err != nil {
		return credential.PreparedActivation{}, err
	}
	if err := ctx.Err(); err != nil {
		return credential.PreparedActivation{}, err
	}
	if err := savepoint.Commit(ctx); err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	released = true
	return prepared, nil
}
