package app

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/jackc/pgx/v5"
)

func (p *firstSourcePublication) unpublished(ctx context.Context, resource credentialmodule.ValidationResource) (bool, error) {
	if p == nil || p.source == nil || p.build == nil || resource.Validate() != nil || resource.TargetID != p.source.config.TargetID || resource.Environment != p.source.config.Environment {
		return false, credentialmodule.ErrValidationForbidden
	}
	target, err := p.source.config.Delivery.Target(ctx, resource.TargetID)
	if err != nil {
		return false, err
	}
	if target.ProjectID != resource.ProjectID || target.Environment != resource.Environment {
		return false, credentialmodule.ErrValidationForbidden
	}
	return target.ActiveGenerationID == "" && target.ActivePublicationID == "", nil
}

func (p *firstSourcePublication) publicRead(ctx context.Context, actor string, resource credentialmodule.ValidationResource, operation string, use func(context.Context, pgx.Tx, credentialmodule.FirstSourceStoredPreparation) error) error {
	authority := p.build.connections.plan
	return authority.authority.WithAuthorization(ctx, actor, resource, access.ActionConnectionManage, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.FirstSourceAdmission) error {
		if operation == "" && use == nil {
			return nil
		}
		stored, err := authority.journal.StoredPreparationTx(ctx, tx, resource.TargetID, operation)
		if err != nil {
			return err
		}
		if stored.Reservation.Resource() != resource || stored.Reservation.Receipt.ActorID != actor {
			return credentialmodule.ErrValidationNotFound
		}
		return use(ctx, tx, stored)
	})
}

func (a *firstSourcePublicationAuthority) Abort(ctx context.Context, actor string, record credentialmodule.ActivationRecord) (credentialmodule.ActivationRecord, error) {
	unpublished, err := a.publication.unpublished(ctx, record.Resource)
	if err != nil {
		return record, err
	}
	if !unpublished {
		return a.ActivationAuthority.Abort(ctx, actor, record)
	}
	p := a.publication
	var result credentialmodule.ActivationRecord
	err = p.publicRead(ctx, actor, record.Resource, record.Request.OperationID, func(ctx context.Context, tx pgx.Tx, stored credentialmodule.FirstSourceStoredPreparation) error {
		row := stored.Reservation
		if row.ActivationRecord() != record || (row.State != "preparing" && row.State != "prepared" && row.State != "switching") {
			return credentialmodule.ErrValidationConflict
		}
		// publicRead already holds the outer unpublished-target mutation lock;
		// acquiring it again in this independent authority transaction deadlocks.
		target, err := p.source.config.Delivery.TargetTx(ctx, tx, record.Resource.TargetID)
		if err != nil {
			return err
		}
		if target.ActiveGenerationID != "" || target.ActivePublicationID != "" || target.TargetRevision != stored.Preparation.Intent.ExpectedTargetRevision {
			return credentialmodule.ErrValidationConflict
		}
		// The shared coordinator has drained provider work. The target/principal/
		// policy locks held by publicRead exclude publication and authority drift.
		_, err = p.source.config.Credentials.AbortActivationPreparationTx(ctx, tx, target.TargetID, row.Request.OperationID, actor, func(_ context.Context, _ pgx.Tx, prepared credentialmodule.PreparedActivation, actor string) error {
			if prepared.Preparation.PredecessorGenerationID != "" || prepared.Preparation.ExpectedTargetRevision != target.TargetRevision || prepared.Preparation.PublicationID != row.PublicationID || prepared.Preparation.GenerationID != row.GenerationID || prepared.Preparation.CandidateID != row.CandidateID || prepared.Preparation.Receipt.ActorID != actor {
				return credentialmodule.ErrValidationConflict
			}
			return nil
		})
		if err != nil && !(row.State == "preparing" && errors.Is(err, credentialmodule.ErrValidationNotFound)) {
			return err
		}
		next := row
		next.State = "aborted"
		row, err = p.source.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "credential.activation.aborted", func(context.Context, pgx.Tx) error { return nil })
		result = row.ActivationRecord()
		return err
	})
	return result, err
}
