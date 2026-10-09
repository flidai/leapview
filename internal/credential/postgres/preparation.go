package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PrepareActivationTx reserves one exact, still-fresh validation receipt for
// an activation intent. After a READ COMMITTED guard, authorization runs in a
// savepoint nested inside the caller-owned transaction and before insertion;
// the preparation row and its audit event are then committed or rolled back
// together without taking ownership of the outer transaction. The authorize
// callback must use the supplied transaction to recheck and lock current
// actor, owner, binding, configuration, target and predecessor authority,
// holding those locks until the caller commits. It must not commit or roll
// back the supplied transaction; a plain unlocked read is not sufficient
// authorization for a later caller-owned mutation.
func (r *Repository) PrepareActivationTx(
	ctx context.Context,
	tx pgx.Tx,
	input credential.ActivationPreparation,
	authorize func(context.Context, Tx, credential.ActivationPreparation) error,
) (credential.PreparedActivation, error) {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	if err := input.Validate(); err != nil {
		return credential.PreparedActivation{}, err
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
	if err := requireReadCommitted(ctx, savepoint, input.Receipt.Binding.DeploymentID); err != nil {
		return credential.PreparedActivation{}, err
	}

	if err := authorize(ctx, savepoint, input); err != nil {
		return credential.PreparedActivation{}, err
	}
	intent, err := input.AuditIntent()
	if err != nil {
		return credential.PreparedActivation{}, err
	}
	createdAt, err := credentialdb.New(savepoint).InsertActivationPreparation(ctx, activationPreparationParams(input))
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrConflict
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	if err := r.audit.RecordAuditEvent(ctx, savepoint, intent); err != nil {
		return credential.PreparedActivation{}, err
	}
	fresh, err := credentialdb.New(savepoint).ActivationPreparationReceiptIsFresh(ctx, credentialdb.ActivationPreparationReceiptIsFreshParams{
		DeploymentID: input.Receipt.Binding.DeploymentID, OperationID: input.OperationID,
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
	return credential.PreparedActivation{Preparation: input, CreatedAt: createdAt}, nil
}

func activationPreparationParams(input credential.ActivationPreparation) credentialdb.InsertActivationPreparationParams {
	receipt := input.Receipt
	binding := receipt.Binding
	return credentialdb.InsertActivationPreparationParams{
		OperationID: input.OperationID, ExpectedTargetRevision: input.ExpectedTargetRevision,
		PredecessorGenerationID: input.PredecessorGenerationID,
		CandidateID:             input.CandidateID, GenerationID: input.GenerationID, PublicationID: input.PublicationID,
		ReceiptID: receipt.ReceiptID, DeploymentID: binding.DeploymentID, VersionID: binding.VersionID,
		OwnerID: binding.OwnerID, ScopeKind: binding.ScopeKind, TargetID: binding.TargetID,
		ProjectID: binding.ProjectID, Environment: binding.Environment, ResourceID: binding.ResourceID,
		Purpose: binding.Purpose, Provider: binding.Provider, Destination: binding.Destination,
		ActorID: receipt.ActorID, BindingID: receipt.BindingID, BindingRevision: receipt.BindingRevision,
		ConfigurationDigest: receipt.ConfigurationDigest, ValidatedAt: receipt.ValidatedAt, ExpiresAt: receipt.ExpiresAt,
	}
}

func canonicalPreparationUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// GetActivationPreparation returns the recorded preparation and its original
// receipt for recovery. Receipt expiry is intentionally not filtered here: a
// recovered row explains what was prepared, but does not renew authority.
func (r *Repository) GetActivationPreparation(ctx context.Context, deploymentID, operationID string) (credential.PreparedActivation, error) {
	if r == nil || r.db == nil || ctx == nil || !canonical(deploymentID, 255) || !canonicalPreparationUUID(operationID) {
		return credential.PreparedActivation{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetActivationPreparation(ctx, credentialdb.GetActivationPreparationParams{
		DeploymentID: deploymentID, OperationID: operationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.PreparedActivation{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.PreparedActivation{}, normalizeDatabaseError(err)
	}
	return activationPreparationFromRow(row)
}

func activationPreparationFromRow(row credentialdb.GetActivationPreparationRow) (credential.PreparedActivation, error) {
	preparation := credential.ActivationPreparation{
		OperationID: row.OperationID, ExpectedTargetRevision: row.ExpectedTargetRevision,
		PredecessorGenerationID: row.PredecessorGenerationID,
		CandidateID:             row.CandidateID, GenerationID: row.GenerationID, PublicationID: row.PublicationID,
		Receipt: credential.ValidationReceipt{
			ReceiptID: row.ReceiptID,
			Binding: encryption.Binding{
				DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind,
				TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment,
				ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider,
				Destination: row.Destination, VersionID: row.VersionID,
			},
			ActorID: row.ActorID, BindingID: row.BindingID, BindingRevision: row.BindingRevision,
			ConfigurationDigest: row.ConfigurationDigest, ValidatedAt: row.ValidatedAt, ExpiresAt: row.ExpiresAt,
		},
	}
	if row.AbortedAt.Valid != (row.AbortedBy != "") ||
		(row.AbortedAt.Valid && row.AbortedAt.Time.IsZero()) ||
		(row.CommittedAt.Valid && row.CommittedAt.Time.IsZero()) ||
		(row.SwitchingAt.Valid && row.SwitchingAt.Time.IsZero()) {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	prepared := credential.PreparedActivation{Preparation: preparation, CreatedAt: row.CreatedAt, AbortedBy: row.AbortedBy}
	if row.SwitchingAt.Valid {
		prepared.SwitchingAt = row.SwitchingAt.Time
	}
	if row.CommittedAt.Valid {
		prepared.CommittedAt = row.CommittedAt.Time
	}
	if row.CompletedAt.Valid {
		prepared.CompletedAt = row.CompletedAt.Time
	}
	if row.AbortedAt.Valid {
		prepared.AbortedAt = row.AbortedAt.Time
	}
	if prepared.Validate() != nil {
		return credential.PreparedActivation{}, credential.ErrUnavailable
	}
	return prepared, nil
}
