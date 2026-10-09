package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ActivationPreparationAbortAuthorizer must acquire the current target fence,
// reauthorize actorID, verify the candidate was safely disposed, and confirm
// the runtime authority is still current, holding all actor, target, and
// runtime-authority locks through the caller's outer commit. It must not
// commit or roll back tx, or perform provider I/O. The store first provides an
// unlocked snapshot of the exact durable preparation, then calls this before
// taking the preparation row lock so lock ordering stays target-first.
type ActivationPreparationAbortAuthorizer func(context.Context, Tx, credential.PreparedActivation, string) error

// AbortActivationPreparationTx permanently records cancellation of an active
// preparation and a redacted audit event in a savepoint owned by tx. It does
// not expire, renew, or otherwise change the originally reserved validation
// receipt. The caller retains ownership of the outer transaction. If switching
// starts while authorization waits for the target fence, the expected-state
// update conflicts rather than auditing cancellation from a stale snapshot.
func (r *Repository) AbortActivationPreparationTx(
	ctx context.Context,
	tx pgx.Tx,
	deploymentID string,
	operationID string,
	actorID string,
	authorize ActivationPreparationAbortAuthorizer,
) (credential.PreparedActivation, error) {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if !canonical(deploymentID, 255) || !canonicalPreparationUUID(operationID) || !canonical(actorID, 255) {
		return credential.PreparedActivation{}, credential.ErrInvalid
	}

	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	completed := false
	defer func() {
		if !completed {
			_ = savepoint.Rollback(context.Background())
		}
	}()
	if err := requireReadCommitted(ctx, savepoint, deploymentID); err != nil {
		return credential.PreparedActivation{}, err
	}

	row, err := credentialdb.New(savepoint).GetActivationPreparation(ctx, credentialdb.GetActivationPreparationParams{
		DeploymentID: deploymentID, OperationID: operationID,
	})
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
	if !prepared.AbortedAt.IsZero() || !prepared.CommittedAt.IsZero() || prepared.AbortedBy != "" {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err := authorize(ctx, savepoint, prepared, actorID); err != nil {
		return credential.PreparedActivation{}, err
	}

	eventID := uuid.NewString()
	intent, err := prepared.AbortAuditIntent(eventID, actorID)
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	abort, err := credentialdb.New(savepoint).AbortActivationPreparation(ctx, credentialdb.AbortActivationPreparationParams{
		DeploymentID: deploymentID, OperationID: operationID,
		AbortedBy:           pgtype.Text{String: actorID, Valid: true},
		ExpectedSwitchingAt: pgtype.Timestamptz{Time: prepared.SwitchingAt, Valid: !prepared.SwitchingAt.IsZero()},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if !abort.AbortedAt.Valid || !abort.AbortedBy.Valid || abort.AbortedBy.String != actorID ||
		abort.AbortedAt.Time.Before(prepared.CreatedAt) {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	prepared.AbortedAt = abort.AbortedAt.Time
	prepared.AbortedBy = abort.AbortedBy.String
	if prepared.Validate() != nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if err := r.audit.RecordAuditEvent(ctx, savepoint, intent); err != nil {
		return credential.PreparedActivation{}, err
	}
	if err := savepoint.Commit(ctx); err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	completed = true
	return prepared, nil
}
