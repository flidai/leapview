package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

type ActivationRequestAuthorizer func(context.Context, Tx) error

// ReserveActivationRequestTx records intent before expensive candidate work.
// authorize must acquire the shared instance/target fence and recheck current
// actor, ownership and binding/configuration revision in the caller transaction.
func (r *Repository) ReserveActivationRequestTx(ctx context.Context, tx Tx, receipt credential.ValidationReceipt, request credential.ActivationRequest, authorize ActivationRequestAuthorizer) (result credential.ActivationRequestRecord, err error) {
	if receipt.Validate() != nil || request.Validate() != nil || request.VersionID != receipt.Binding.VersionID || request.ReceiptID != receipt.ReceiptID || request.ExpectedBindingRevision != receipt.BindingRevision {
		return result, credential.ErrInvalid
	}
	err = r.requestTransaction(ctx, tx, receipt.Binding.DeploymentID, authorize, func(q *credentialdb.Queries) error {
		existing, readErr := q.GetActivationRequest(ctx, credentialdb.GetActivationRequestParams{DeploymentID: receipt.Binding.DeploymentID, OperationID: request.OperationID})
		if readErr == nil {
			result, readErr = r.requestFromRow(ctx, q, existing)
			if readErr != nil {
				return readErr
			}
			if result.Request != request || !result.Receipt.Equal(receipt) {
				return credential.ErrConflict
			}
			return nil
		}
		if !errors.Is(readErr, pgx.ErrNoRows) {
			return normalizeDatabaseError(readErr)
		}
		if err := CheckNoPendingActivationTx(ctx, tx, receipt.Binding.DeploymentID); err != nil {
			return err
		}
		stored, err := q.GetValidationReceipt(ctx, credentialdb.GetValidationReceiptParams{DeploymentID: receipt.Binding.DeploymentID, ReceiptID: receipt.ReceiptID})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		if !validationReceiptFromRow(stored).Equal(receipt) {
			return credential.ErrConflict
		}
		row, err := q.InsertActivationRequest(ctx, credentialdb.InsertActivationRequestParams{OperationID: request.OperationID, DeploymentID: receipt.Binding.DeploymentID, ReceiptID: request.ReceiptID, VersionID: request.VersionID, ExpectedBindingRevision: request.ExpectedBindingRevision})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		result, err = r.requestFromRow(ctx, q, row)
		if err != nil {
			return err
		}
		if _, err = q.AppendActivationRequestReceipt(ctx, credentialdb.AppendActivationRequestReceiptParams{OperationID: request.OperationID, DeploymentID: receipt.Binding.DeploymentID, ReceiptID: request.ReceiptID}); err != nil {
			return normalizeDatabaseError(err)
		}
		intent, err := result.CommandAudit(receipt.ActorID, "credential.activation.requested")
		if err != nil {
			return err
		}
		return r.audit.RecordAuditEvent(ctx, tx, intent)
	})
	return result, err
}

// TransitionActivationRequestTx CASes a phase with its exact prepared runtime
// identities. A nonempty commandAction appends the public command audit in the
// same transaction. Source publication/configuration mutation uses this tx too.
func (r *Repository) TransitionActivationRequestTx(ctx context.Context, tx Tx, before, next credential.ActivationRequestRecord, actor, commandAction string, authorize ActivationRequestAuthorizer) (result credential.ActivationRequestRecord, err error) {
	if before.Validate() != nil || next.Request != before.Request || !next.Receipt.Equal(before.Receipt) || !canonical(actor, 255) {
		return result, credential.ErrInvalid
	}
	err = r.requestTransaction(ctx, tx, before.Receipt.Binding.DeploymentID, authorize, func(q *credentialdb.Queries) error {
		row, err := q.TransitionActivationRequest(ctx, credentialdb.TransitionActivationRequestParams{
			DeploymentID: before.Receipt.Binding.DeploymentID, OperationID: before.Request.OperationID, ExpectedRevision: before.Revision,
			State: next.State, PlanID: next.PlanID, CandidateID: next.CandidateID, GenerationID: next.GenerationID, PublicationID: next.PublicationID, ConfigurationRevision: next.ConfigurationRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return credential.ErrConflict
		}
		if err != nil {
			return normalizeDatabaseError(err)
		}
		result, err = r.requestFromRow(ctx, q, row)
		if err != nil {
			return err
		}
		if commandAction != "" {
			intent, err := result.CommandAudit(actor, commandAction)
			if err != nil {
				return err
			}
			return r.audit.RecordAuditEvent(ctx, tx, intent)
		}
		return nil
	})
	return result, err
}

func (r *Repository) GetActivationRequest(ctx context.Context, deploymentID, operationID string) (credential.ActivationRequestRecord, error) {
	if r == nil || ctx == nil || !canonical(deploymentID, 255) || !canonicalPreparationUUID(operationID) {
		return credential.ActivationRequestRecord{}, credential.ErrInvalid
	}
	q := credentialdb.New(r.db)
	row, err := q.GetActivationRequest(ctx, credentialdb.GetActivationRequestParams{DeploymentID: deploymentID, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.ActivationRequestRecord{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.ActivationRequestRecord{}, normalizeDatabaseError(err)
	}
	return r.requestFromRow(ctx, q, row)
}

func (r *Repository) GetPendingActivationRequest(ctx context.Context, deploymentID string) (credential.ActivationRequestRecord, error) {
	if r == nil || ctx == nil || !canonical(deploymentID, 255) {
		return credential.ActivationRequestRecord{}, credential.ErrInvalid
	}
	q := credentialdb.New(r.db)
	row, err := q.GetPendingActivationRequest(ctx, deploymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.ActivationRequestRecord{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.ActivationRequestRecord{}, normalizeDatabaseError(err)
	}
	return r.requestFromRow(ctx, q, row)
}

func (r *Repository) ReadValidationReceipt(ctx context.Context, deploymentID, receiptID string) (credential.ValidationReceipt, error) {
	if r == nil || ctx == nil || !canonical(deploymentID, 255) || !canonicalPreparationUUID(receiptID) {
		return credential.ValidationReceipt{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetValidationReceipt(ctx, credentialdb.GetValidationReceiptParams{DeploymentID: deploymentID, ReceiptID: receiptID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.ValidationReceipt{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.ValidationReceipt{}, normalizeDatabaseError(err)
	}
	receipt := validationReceiptFromRow(row)
	if err := receipt.Validate(); err != nil {
		return credential.ValidationReceipt{}, credential.ErrUnavailable
	}
	return receipt, nil
}

func (r *Repository) requestTransaction(ctx context.Context, tx Tx, deploymentID string, authorize ActivationRequestAuthorizer, mutate func(*credentialdb.Queries) error) error {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil {
		return credential.ErrUnavailable
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	defer func() { _ = savepoint.Rollback(context.Background()) }()
	if err := requireReadCommitted(ctx, savepoint, deploymentID); err != nil {
		return err
	}
	if err := authorize(ctx, savepoint); err != nil {
		return err
	}
	if err := mutate(credentialdb.New(savepoint)); err != nil {
		return err
	}
	return normalizeDatabaseError(savepoint.Commit(ctx))
}

func (r *Repository) requestFromRow(ctx context.Context, q *credentialdb.Queries, row credentialdb.CredentialActivationRequest) (credential.ActivationRequestRecord, error) {
	receipt, err := q.GetValidationReceipt(ctx, credentialdb.GetValidationReceiptParams{DeploymentID: row.DeploymentID, ReceiptID: row.ReceiptID})
	if err != nil {
		return credential.ActivationRequestRecord{}, normalizeDatabaseError(err)
	}
	result := credential.ActivationRequestRecord{Request: credential.ActivationRequest{OperationID: row.OperationID, VersionID: row.VersionID, ReceiptID: row.ReceiptID, ExpectedBindingRevision: row.ExpectedBindingRevision}, Receipt: validationReceiptFromRow(receipt), State: row.State, Revision: row.Revision,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, PlanID: row.PlanID, CandidateID: row.CandidateID, GenerationID: row.GenerationID, PublicationID: row.PublicationID, ConfigurationRevision: row.ConfigurationRevision}
	if result.Validate() != nil {
		return credential.ActivationRequestRecord{}, credential.ErrUnavailable
	}
	return result, nil
}

func validationReceiptFromRow(row credentialdb.CredentialValidationReceipt) credential.ValidationReceipt {
	return credential.ValidationReceipt{ReceiptID: row.ReceiptID, ActorID: row.ActorID, BindingID: row.BindingID, BindingRevision: row.BindingRevision, ConfigurationDigest: row.ConfigurationDigest, ValidatedAt: row.ValidatedAt, ExpiresAt: row.ExpiresAt,
		Binding: encryption.Binding{DeploymentID: row.DeploymentID, VersionID: row.VersionID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind, TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment, ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider, Destination: row.Destination},
	}
}
