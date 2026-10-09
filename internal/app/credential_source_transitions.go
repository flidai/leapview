package app

import (
	"context"
	"errors"
	"time"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *sourceCredentialActivation) Switch(ctx context.Context, actor string, record credentialmodule.ActivationRecord, receiptID string) (credentialmodule.ActivationRecord, error) {
	row, err := a.exact(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if row.State != "preparing" && row.State != "prepared" && row.State != "switching" {
		return row.ActivationRecord(), credentialmodule.ErrValidationConflict
	}
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		_, err := a.config.Credentials.RefreshActivationRequestReceiptTx(ctx, tx, row, receiptID, actor, a.auth(row, actor))
		return err
	})
	if err != nil {
		return row.ActivationRecord(), err
	}
	if row.State == "preparing" {
		result, err := a.prepareNative(ctx, actor, row)
		if err != nil {
			return result, err
		}
		row, err = a.exact(ctx, actor, record.Resource, record.Request.OperationID)
		if err != nil {
			return result, err
		}
	}
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		auth := a.auth(row, actor)
		if err := a.config.Credentials.CheckActivationReceiptFreshTx(ctx, tx, a.config.TargetID, row.Request.OperationID); err != nil {
			return err
		}
		if row.State == "prepared" {
			if _, err := a.config.Credentials.BeginActivationSwitchingTx(ctx, tx, a.config.TargetID, row.Request.OperationID, actor, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.PreparedActivation, _ string) error {
				return auth(ctx, tx)
			}); err != nil {
				return err
			}
		}
		next := row
		next.State = "switching"
		var err error
		row, err = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "credential.activation.retried", auth)
		return err
	})
	return row.ActivationRecord(), err
}

func (a *sourceCredentialActivation) Commit(ctx context.Context, actor string, record credentialmodule.ActivationRecord) (credentialmodule.ActivationRecord, error) {
	row, err := a.exact(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if row.State != "switching" {
		return row.ActivationRecord(), credentialmodule.ErrValidationConflict
	}
	repository, err := a.config.PublicationRepository(row.Request.OperationID, func(ctx context.Context, tx pgx.Tx, prepared credentialmodule.PreparedActivation) error {
		if prepared.Preparation.OperationID != row.Request.OperationID || !prepared.Preparation.Receipt.Equal(row.Receipt) || prepared.Preparation.PublicationID != row.PublicationID || prepared.Preparation.CandidateID != row.CandidateID || prepared.Preparation.GenerationID != row.GenerationID {
			return credentialmodule.ErrValidationConflict
		}
		if err := a.config.Credentials.CheckActivationReceiptFreshTx(ctx, tx, a.config.TargetID, row.Request.OperationID); err != nil {
			return err
		}
		return a.auth(row, actor)(ctx, tx)
	})
	if err != nil {
		return row.ActivationRecord(), err
	}
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		now, err := repository.DatabaseNowTx(ctx, tx)
		if err != nil {
			return err
		}
		lease, err := repository.AcquireLeaseTx(ctx, tx, deploymentpostgres.LeaseInput{LeaseID: uuid.NewString(), TargetID: a.config.TargetID, OwnerID: "credential-" + row.Request.OperationID, ExpiresAt: now.Add(5 * time.Minute)})
		if err != nil {
			return err
		}
		publication, err := repository.PublicationTx(ctx, tx, row.PublicationID)
		if err != nil {
			return err
		}
		if publication.State != "pending" || publication.GenerationID != row.GenerationID || publication.CandidateID != row.CandidateID || publication.TargetID != a.config.TargetID {
			return credentialmodule.ErrValidationConflict
		}
		_, err = repository.ActivateTxWithPreCommitHook(ctx, tx, deploymentpostgres.ActivationInput{PublicationID: publication.PublicationID, TargetID: publication.TargetID, GenerationID: publication.GenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision, RequestDigest: publication.RequestDigest, ActorID: actor, CorrelationID: row.Request.OperationID, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}, a.config.BeforeActivationCommit)
		if err != nil {
			return err
		}
		next := row
		next.State = "committed"
		row, err = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "", func(ctx context.Context, tx pgx.Tx) error { return a.committed(ctx, tx, next) })
		return err
	})
	return row.ActivationRecord(), err
}

func (a *sourceCredentialActivation) Complete(ctx context.Context, record credentialmodule.ActivationRecord) (credentialmodule.ActivationRecord, error) {
	row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, record.Request.OperationID)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if row.ActivationRecord() != record || row.State != "committed" {
		return row.ActivationRecord(), credentialmodule.ErrValidationConflict
	}
	prepared, err := a.config.Credentials.GetActivationPreparation(ctx, a.config.TargetID, row.Request.OperationID)
	if err != nil {
		return row.ActivationRecord(), err
	}
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		authority := func(ctx context.Context, tx pgx.Tx) error { return a.committed(ctx, tx, row) }
		if _, err := a.config.Credentials.CompleteActivationTx(ctx, tx, prepared, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.PreparedActivation) error {
			return authority(ctx, tx)
		}); err != nil {
			return err
		}
		next := row
		next.State = "completed"
		var err error
		row, err = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, row.Receipt.ActorID, "", authority)
		return err
	})
	return row.ActivationRecord(), err
}
func (a *sourceCredentialActivation) Abort(ctx context.Context, actor string, record credentialmodule.ActivationRecord) (credentialmodule.ActivationRecord, error) {
	row, err := a.exact(ctx, actor, record.Resource, record.Request.OperationID)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if row.State == "committed" || row.State == "completed" || row.State == "aborted" {
		return row.ActivationRecord(), credentialmodule.ErrValidationConflict
	}
	prepared, readErr := a.config.Credentials.GetActivationPreparation(ctx, a.config.TargetID, row.Request.OperationID)
	if readErr != nil && !errors.Is(readErr, credentialmodule.ErrValidationNotFound) {
		return row.ActivationRecord(), readErr
	}
	err = a.transaction(ctx, func(tx pgx.Tx) error {
		auth := a.auth(row, actor)
		if err := auth(ctx, tx); err != nil {
			return err
		}
		if readErr == nil {
			target, err := a.config.Delivery.TargetForUpdateTx(ctx, tx, a.config.TargetID)
			if err != nil {
				return err
			}
			if target.ActiveGenerationID != prepared.Preparation.PredecessorGenerationID || target.TargetRevision != prepared.Preparation.ExpectedTargetRevision {
				return credentialmodule.ErrValidationConflict
			}
			if _, err = a.config.Credentials.AbortActivationPreparationTx(ctx, tx, a.config.TargetID, row.Request.OperationID, actor, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.PreparedActivation, _ string) error {
				return auth(ctx, tx)
			}); err != nil {
				return err
			}
		}
		next := row
		next.State = "aborted"
		var err error
		row, err = a.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "credential.activation.aborted", auth)
		return err
	})
	return row.ActivationRecord(), err
}
