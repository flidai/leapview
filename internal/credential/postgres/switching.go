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

// ActivationSwitchingAuthorizer must acquire the delivery target fence before
// operation-row mutation, then lock and recheck current actor/owner/binding,
// configuration, target revision and predecessor authority for the exact stored
// intent. The original receipt actor must still be authorized. It must not do
// provider I/O or commit/roll back tx; its locks live through the outer commit.
type ActivationSwitchingAuthorizer func(context.Context, Tx, credential.PreparedActivation, string) error

// BeginActivationSwitchingTx records the one-time precommit transition and its
// audit inside a caller-owned READ COMMITTED transaction. The coordinator must
// confirm the outer commit before pausing source work. A returned record is not
// evidence of drain, publication, readiness, or authority to resume after a crash.
// A repeated transition conflicts; recovery reads inspect a lost acknowledgment
// without renewing the receipt or repeating the audit.
func (r *Repository) BeginActivationSwitchingTx(
	ctx context.Context,
	tx pgx.Tx,
	deploymentID, operationID, actorID string,
	authorize ActivationSwitchingAuthorizer,
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
	queries := credentialdb.New(savepoint)
	row, err := queries.GetActivationPreparation(ctx, credentialdb.GetActivationPreparationParams{
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
	if !prepared.SwitchingAt.IsZero() || !prepared.AbortedAt.IsZero() {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if actorID != prepared.Preparation.Receipt.ActorID {
		return credential.PreparedActivation{}, credential.ErrForbidden
	}
	// The snapshot above is unlocked. Authorization acquires the target first;
	// the conditional UPDATE below rejects a switch/abort that won meanwhile.
	if err := authorize(ctx, savepoint, prepared, actorID); err != nil {
		return credential.PreparedActivation{}, err
	}
	intent, err := prepared.SwitchingAuditIntent(uuid.NewString())
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	switchingAt, err := queries.BeginActivationSwitching(ctx, credentialdb.BeginActivationSwitchingParams{
		DeploymentID: deploymentID, OperationID: operationID, ActorID: actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if !switchingAt.Valid || switchingAt.Time.IsZero() {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	prepared.SwitchingAt = switchingAt.Time
	if prepared.Validate() != nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if err := r.audit.RecordAuditEvent(ctx, savepoint, intent); err != nil {
		return credential.PreparedActivation{}, err
	}
	fresh, err := queries.ActivationPreparationReceiptIsFresh(ctx, credentialdb.ActivationPreparationReceiptIsFreshParams{
		DeploymentID: deploymentID, OperationID: operationID,
	})
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if !fresh {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err := savepoint.Commit(ctx); err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	completed = true
	return prepared, nil
}

// GetPendingActivation discovers the sole unfinished operation for recovery.
// It includes an expired original receipt and never extends its authority.
// Absence is only a database observation, not permission to open admission.
func (r *Repository) GetPendingActivation(ctx context.Context, deploymentID string) (credential.PreparedActivation, error) {
	if r == nil || r.db == nil || ctx == nil || !canonical(deploymentID, 255) {
		return credential.PreparedActivation{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetPendingActivation(ctx, deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	return activationPreparationFromRow(credentialdb.GetActivationPreparationRow(row))
}
