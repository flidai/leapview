package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// RefreshActivationRequestReceiptTx appends fresh validation without rewriting
// the original intent. A receipt can belong to only one activation operation.
// Exact actor, version, ownership, destination and configuration must match.
func (r *Repository) RefreshActivationRequestReceiptTx(ctx context.Context, tx Tx, record credential.ActivationRequestRecord, receiptID, actor string, authorize ActivationRequestAuthorizer) (receipt credential.ValidationReceipt, err error) {
	if record.Validate() != nil || !canonicalPreparationUUID(receiptID) || actor != record.Receipt.ActorID {
		return receipt, credential.ErrInvalid
	}
	err = r.requestTransaction(ctx, tx, record.Receipt.Binding.DeploymentID, authorize, func(q *credentialdb.Queries) error {
		row, err := q.GetValidationReceipt(ctx, credentialdb.GetValidationReceiptParams{DeploymentID: record.Receipt.Binding.DeploymentID, ReceiptID: receiptID})
		if errors.Is(err, pgx.ErrNoRows) {
			return credential.ErrNotFound
		}
		if err != nil {
			return normalizeDatabaseError(err)
		}
		receipt = validationReceiptFromRow(row)
		original := record.Receipt
		if receipt.Binding != original.Binding || receipt.ActorID != actor || receipt.BindingID != original.BindingID || receipt.BindingRevision != original.BindingRevision || receipt.ConfigurationDigest != original.ConfigurationDigest {
			return credential.ErrConflict
		}
		_, err = q.AppendActivationRequestReceipt(ctx, credentialdb.AppendActivationRequestReceiptParams{DeploymentID: record.Receipt.Binding.DeploymentID, OperationID: record.Request.OperationID, ReceiptID: receiptID})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		reserved, err := q.GetActivationReceiptReservation(ctx, credentialdb.GetActivationReceiptReservationParams{DeploymentID: record.Receipt.Binding.DeploymentID, ReceiptID: receiptID})
		if err != nil {
			return normalizeDatabaseError(err)
		}
		if reserved != record.Request.OperationID {
			return credential.ErrConflict
		}
		return nil
	})
	return receipt, err
}

// CheckActivationReceiptFreshTx is evaluated after the shared mutation fence
// immediately before pointer/configuration CAS. An old receipt is never renewed
// by reading the journal; only an appended, exact successful probe can qualify.
func (r *Repository) CheckActivationReceiptFreshTx(ctx context.Context, tx Tx, deploymentID, operationID string) error {
	if r == nil || ctx == nil || tx == nil || !canonical(deploymentID, 255) || !canonicalPreparationUUID(operationID) {
		return credential.ErrInvalid
	}
	if err := requireReadCommitted(ctx, tx, deploymentID); err != nil {
		return err
	}
	fresh, err := credentialdb.New(tx).CheckActivationReceiptFresh(ctx, credentialdb.CheckActivationReceiptFreshParams{DeploymentID: deploymentID, OperationID: operationID})
	if err != nil {
		return normalizeDatabaseError(err)
	}
	if !fresh {
		return credential.ErrConflict
	}
	return nil
}

func (r *Repository) CurrentActivationReceipt(ctx context.Context, deploymentID, operationID string) (credential.ValidationReceipt, error) {
	if r == nil || ctx == nil || !canonical(deploymentID, 255) || !canonicalPreparationUUID(operationID) {
		return credential.ValidationReceipt{}, credential.ErrInvalid
	}
	row, err := credentialdb.New(r.db).GetActivationRequestReceipt(ctx, credentialdb.GetActivationRequestReceiptParams{DeploymentID: deploymentID, OperationID: operationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.ValidationReceipt{}, credential.ErrNotFound
	}
	if err != nil {
		return credential.ValidationReceipt{}, normalizeDatabaseError(err)
	}
	receipt := validationReceiptFromRow(row)
	if receipt.Validate() != nil {
		return credential.ValidationReceipt{}, credential.ErrUnavailable
	}
	return receipt, nil
}
