package postgres

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
)

// SaveValidation persists a successful observation for one exact immutable
// draft and appends its redacted audit intent in the same transaction. The
// insert query rechecks the complete saved binding and the database clock so
// callers cannot persist a receipt for a different draft or an expired proof.
func (r *Repository) SaveValidation(ctx context.Context, receipt credential.ValidationReceipt, intent access.AuditIntent) error {
	if r == nil || r.db == nil || r.audit == nil || ctx == nil {
		return credential.ErrUnavailable
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	canonicalIntent, err := credential.ValidateValidationAuditIntent(receipt, intent)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return normalizeDatabaseError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	binding := receipt.Binding
	inserted, err := credentialdb.New(tx).InsertValidationReceipt(ctx, credentialdb.InsertValidationReceiptParams{
		ReceiptID: receipt.ReceiptID, DeploymentID: binding.DeploymentID, VersionID: binding.VersionID,
		OwnerID: binding.OwnerID, ScopeKind: binding.ScopeKind, TargetID: binding.TargetID,
		ProjectID: binding.ProjectID, Environment: binding.Environment, ResourceID: binding.ResourceID,
		Purpose: binding.Purpose, Provider: binding.Provider, Destination: binding.Destination,
		ActorID: receipt.ActorID, BindingID: receipt.BindingID, BindingRevision: receipt.BindingRevision,
		ConfigurationDigest: receipt.ConfigurationDigest, ValidatedAt: receipt.ValidatedAt, ExpiresAt: receipt.ExpiresAt,
	})
	if err != nil {
		return normalizeDatabaseError(err)
	}
	if inserted != 1 {
		return credential.ErrConflict
	}
	if err := r.audit.RecordAuditEvent(ctx, tx, canonicalIntent); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return normalizeDatabaseError(err)
	}
	return nil
}

var _ credential.ValidationRepository = (*Repository)(nil)
