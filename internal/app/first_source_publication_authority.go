package app

import (
	"context"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
)

func firstSourcePublicationIntentMatches(i *firstSourcePublicationInvocation, stored credentialmodule.FirstSourceStoredPlanLink, publication deploymentpostgres.DeliveryPublication) bool {
	if i == nil {
		return false
	}
	intent, plan := stored.Preparation.Preparation.Intent, i.plan
	request := deploymentmodule.NativeDeliveryPlanRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PrincipalID: plan.ActorID, SourceOwnerID: plan.SourceOwnerID, Operation: string(plan.Operation), SourceDigest: plan.SourceDigest, SourceAttestationDigest: plan.Provenance.AttestationDigest, IdempotencyKey: intent.PlanIdempotencyKey, FirstSourcePreparationID: intent.PreparationID}
	return stored.Link == i.stored.Link && stored.Link.PlanID == plan.ID && stored.Link.BindingDigest == plan.Execution.BindingDigest && stored.Link.RequestDigest == intent.PlanRequestDigest &&
		firstSourcePlanIntentMatches(intent, request, plan.BaseTargetRevision) && firstSourcePlanMatches(plan, request, intent.ExpectedTargetRevision) && plan.Governance.RequiresApproval &&
		publication.PublicationID == i.publication.PublicationID && publication.TargetID == plan.TargetID && publication.GenerationID == i.publication.GenerationID && publication.CandidateID == i.publication.CandidateID && publication.SnapshotSealID == i.publication.SnapshotSealID && publication.RequestDigest == i.publication.RequestDigest && publication.ActorID == intent.PublisherID && publication.ExpectedBaseGenerationID == "" && publication.ExpectedTargetRevision == intent.ExpectedTargetRevision
}

// Called under the real publication target fence. The worker's surrounding
// native approval path owns publisher/reviewer credential admission; this adds
// current staged connection grant, binding, key version and receipt checks.
func (p *firstSourcePublication) validateTx(ctx context.Context, tx pgx.Tx, i *firstSourcePublicationInvocation) (credentialmodule.ActivationRequestRecord, error) {
	var empty credentialmodule.ActivationRequestRecord
	if p == nil || p.source == nil || i == nil || i.owner != p || !i.active.Load() {
		return empty, credentialmodule.ErrValidationConflict
	}
	delivery := p.source.config.Delivery
	target, err := delivery.TargetForUpdateTx(ctx, tx, i.publication.TargetID)
	if err != nil {
		return empty, err
	}
	stored, err := p.build.connections.plan.journal.StoredPlanLinkTx(ctx, tx, target.TargetID, i.plan.ID)
	if err != nil {
		return empty, err
	}
	publication, err := delivery.PublicationTx(ctx, tx, i.publication.PublicationID)
	if err != nil {
		return empty, err
	}
	if !firstSourcePublicationIntentMatches(i, stored, publication) {
		return empty, credentialmodule.ErrValidationConflict
	}
	row := stored.Preparation.Reservation
	if row.State == "committed" || row.State == "completed" {
		if row.PlanID != i.plan.ID || row.PublicationID != publication.PublicationID || row.GenerationID != publication.GenerationID || row.CandidateID != publication.CandidateID {
			return empty, credentialmodule.ErrValidationConflict
		}
		return row, p.source.committed(ctx, tx, row)
	}
	if (row.State != "preparing" && row.State != "prepared" && row.State != "switching") || publication.State != "pending" || target.ActiveGenerationID != "" || target.ActivePublicationID != "" || target.TargetRevision != publication.ExpectedTargetRevision || target.ProjectID != i.plan.ProjectID.String() || target.Environment != i.plan.Environment {
		return empty, credentialmodule.ErrValidationConflict
	}
	if row.State != "preparing" && (row.PlanID != i.plan.ID || row.PublicationID != publication.PublicationID || row.GenerationID != publication.GenerationID || row.CandidateID != publication.CandidateID) {
		return empty, credentialmodule.ErrValidationConflict
	}
	planAuthority := p.build.connections.plan
	binding, err := planAuthority.authority.lockAdmittedStateTx(ctx, tx, stored.Preparation.Admission)
	if err != nil {
		return empty, err
	}
	configuration, err := credentialValidationConfigurationDigest(binding, planAuthority.probePolicy())
	if err != nil {
		return empty, err
	}
	fingerprint, err := deployment.BindingFingerprint(firstSourceCredentialEvidence(binding, stored.Preparation.Preparation.Intent))
	if err != nil {
		return empty, err
	}
	if configuration != row.Receipt.ConfigurationDigest || fingerprint != stored.Link.BindingDigest || !row.Receipt.Equal(stored.Preparation.Preparation.Intent.Receipt) {
		return empty, credentialmodule.ErrValidationConflict
	}
	if err = p.source.config.Credentials.CheckActivationReceiptFreshTx(ctx, tx, target.TargetID, row.Request.OperationID); err != nil {
		return empty, err
	}
	generation, err := deploymentpostgres.New(tx).Generation(ctx, publication.GenerationID)
	if err != nil {
		return empty, err
	}
	candidate, err := delivery.CandidateTx(ctx, tx, publication.CandidateID)
	if err != nil {
		return empty, err
	}
	if generation.TargetID != target.TargetID || generation.PlanID != i.plan.ID || generation.PlanDigest != i.plan.Digest || generation.CandidateID != publication.CandidateID || generation.SnapshotSealID != publication.SnapshotSealID || candidate.PlanID != i.plan.ID || candidate.TargetID != target.TargetID || candidate.Status != "qualified" || candidate.SnapshotSealID != publication.SnapshotSealID {
		return empty, credentialmodule.ErrValidationConflict
	}
	return row, nil
}

func (a *firstSourcePublicationAuthority) Switch(ctx context.Context, actor string, record credentialmodule.ActivationRecord, receipt string) (credentialmodule.ActivationRecord, error) {
	i, err := a.invocation(ctx, actor, record.Resource)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if i == nil {
		return a.ActivationAuthority.Switch(ctx, actor, record, receipt)
	}
	p := a.publication
	var row credentialmodule.ActivationRequestRecord
	err = p.source.transaction(ctx, func(tx pgx.Tx) error {
		var err error
		row, err = p.validateTx(ctx, tx, i)
		if err != nil {
			return err
		}
		if row.ActivationRecord() != record || receipt != row.Request.ReceiptID {
			return credentialmodule.ErrValidationConflict
		}
		authorize := func(ctx context.Context, tx pgx.Tx) error { _, err := p.validateTx(ctx, tx, i); return err }
		if row.State == "preparing" {
			preparation := credentialmodule.ActivationPreparation{OperationID: row.Request.OperationID, Receipt: row.Receipt, ExpectedTargetRevision: i.publication.ExpectedTargetRevision, CandidateID: i.publication.CandidateID, GenerationID: i.publication.GenerationID, PublicationID: i.publication.PublicationID}
			if _, err := p.source.config.Credentials.PrepareActivationTx(ctx, tx, preparation, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.ActivationPreparation) error {
				return authorize(ctx, tx)
			}); err != nil {
				return err
			}
			next := row
			next.PlanID, next.CandidateID, next.GenerationID, next.PublicationID, next.State = i.plan.ID, i.publication.CandidateID, i.publication.GenerationID, i.publication.PublicationID, "prepared"
			row, err = p.source.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "", authorize)
			if err != nil {
				return err
			}
		}
		if row.State == "prepared" {
			if _, err := p.source.config.Credentials.BeginActivationSwitchingTx(ctx, tx, row.Receipt.Binding.TargetID, row.Request.OperationID, actor, func(ctx context.Context, tx pgx.Tx, _ credentialmodule.PreparedActivation, _ string) error {
				return authorize(ctx, tx)
			}); err != nil {
				return err
			}
			next := row
			next.State = "switching"
			row, err = p.source.config.Credentials.TransitionActivationRequestTx(ctx, tx, row, next, actor, "", authorize)
			return err
		}
		if row.State != "switching" {
			return credentialmodule.ErrValidationConflict
		}
		return nil
	})
	return row.ActivationRecord(), err
}

func (a *firstSourcePublicationAuthority) Commit(ctx context.Context, actor string, record credentialmodule.ActivationRecord) (credentialmodule.ActivationRecord, error) {
	i, err := a.invocation(ctx, actor, record.Resource)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if i == nil {
		return a.ActivationAuthority.Commit(ctx, actor, record)
	}
	p := a.publication
	row, err := p.source.config.Credentials.GetActivationRequest(ctx, record.Resource.TargetID, record.Request.OperationID)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if row.ActivationRecord() != record || row.State != "switching" || i.executed {
		return record, credentialmodule.ErrValidationConflict
	}
	repository, err := p.repository(row, func(ctx context.Context, tx pgx.Tx, prepared credentialmodule.PreparedActivation) error {
		current, err := p.validateTx(ctx, tx, i)
		if err != nil {
			return err
		}
		if current.ActivationRecord() != record || prepared.Preparation.OperationID != record.Request.OperationID || !prepared.Preparation.Receipt.Equal(current.Receipt) {
			return credentialmodule.ErrValidationConflict
		}
		return nil
	})
	if err != nil {
		return record, err
	}
	i.executed = true
	i.result, err = i.run(ctx, repository)
	if err != nil {
		return record, err
	}
	row, err = p.source.config.Credentials.GetActivationRequest(ctx, record.Resource.TargetID, record.Request.OperationID)
	if err == nil && row.State != "committed" {
		err = credentialmodule.ErrValidationConflict
	}
	return row.ActivationRecord(), err
}
