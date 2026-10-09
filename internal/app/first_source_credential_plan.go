package app

import (
	"context"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

type firstSourceReceiptFreshness interface {
	CheckActivationReceiptFreshTx(context.Context, pgx.Tx, string, string) error
}

// A native plan uses the validating operator's exact stored preparation and
// current request authority. The normal native coordinator also authorizes that
// actor as publisher and retains its independent semantic/reviewer policy.
type firstSourceCredentialPlan struct {
	authority   firstSourceCredentialAuthority
	journal     credentialmodule.FirstSourcePreparationMutations
	receipts    firstSourceReceiptFreshness
	probePolicy func() string
}

var _ appdeploymentpostgres.NativeFirstSourcePlanAuthority = (*firstSourceCredentialPlan)(nil)

func (p *firstSourceCredentialPlan) available() bool {
	return p != nil && p.authority.production && !typednil.IsNil(p.authority.pool) && !typednil.IsNil(p.authority.fence) && !typednil.IsNil(p.journal) && !typednil.IsNil(p.receipts) && p.probePolicy != nil
}

func (p *firstSourceCredentialPlan) Resolve(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentpostgres.DeliveryTarget, candidate deployment.CandidateConnectionRequest) (result []deployment.CandidateConnectionEvidence, err error) {
	if !p.available() || ctx == nil {
		return nil, credentialmodule.ErrValidationUnavailable
	}
	err = p.authority.fence.WithUnpublishedTarget(ctx, request.TargetID, request.ProjectID.String(), request.Environment, func(ctx context.Context) error {
		tx, err := p.authority.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx))
		stored, err := p.journal.StoredPreparationTx(ctx, tx, request.TargetID, request.FirstSourcePreparationID)
		if err != nil {
			return err
		}
		binding, err := p.checkIntentTx(ctx, tx, stored, request, target)
		if err != nil {
			return err
		}
		if candidate.TargetID != request.TargetID || candidate.Actor != request.PrincipalID || candidate.Identity.ProjectID != request.ProjectID || candidate.Identity.Environment != request.Environment || len(candidate.Requirements) != 1 {
			return credentialmodule.ErrValidationConflict
		}
		required := candidate.Requirements[0]
		if required.ConnectionID != binding.ConnectionID || required.ConnectorKind != "postgres" || required.Access != "" {
			return credentialmodule.ErrValidationConflict
		}
		for _, authored := range candidate.AuthoredConnections {
			if authored.ConnectionID != binding.ConnectionID || authored.ConnectorKind != "postgres" || authored.Access != "" {
				return credentialmodule.ErrValidationConflict
			}
		}
		result = firstSourceCredentialEvidence(binding, stored.Preparation.Intent)
		return tx.Commit(ctx)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (p *firstSourceCredentialPlan) checkIntentTx(ctx context.Context, tx pgx.Tx, stored credentialmodule.FirstSourceStoredPreparation, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentpostgres.DeliveryTarget) (connectionbinding.TargetBinding, error) {
	i := stored.Preparation.Intent
	if stored.Reservation.State != "preparing" || target.ActiveGenerationID != "" || target.ActivePublicationID != "" || target.TargetID != p.authority.targetID || target.TargetID != request.TargetID || target.ProjectID != request.ProjectID.String() || target.Environment != p.authority.environment || target.Environment != request.Environment || !firstSourcePlanIntentMatches(i, request, target.TargetRevision) {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationConflict
	}
	issuer, err := accessmodule.CredentialTransactionEvidence(ctx, i.Receipt.ActorID)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	grant, err := stored.Admission.Intent.Grant()
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	if err := accessmodule.RecheckCredentialAuthorityTx(ctx, tx, issuer, grant.Permissions); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	binding, err := p.authority.lockAdmittedStateTx(ctx, tx, stored.Admission)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	policy := p.probePolicy()
	if policy == "" {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationUnavailable
	}
	configuration, err := credentialValidationConfigurationDigest(binding, policy)
	if err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	if i.Receipt.Binding.Purpose != "connection-authentication" || i.Receipt.Binding.Destination != binding.Evidence().EndpointConfigHash || i.Receipt.ConfigurationDigest != configuration || !i.MatchesAdmission(stored.Admission) {
		return connectionbinding.TargetBinding{}, credentialmodule.ErrValidationConflict
	}
	if err := p.receipts.CheckActivationReceiptFreshTx(ctx, tx, request.TargetID, i.PreparationID); err != nil {
		return connectionbinding.TargetBinding{}, err
	}
	return binding, nil
}

func firstSourcePlanIntentMatches(i credentialmodule.FirstSourcePreparationIntent, request deploymentmodule.NativeDeliveryPlanRequest, revision int64) bool {
	digest, err := deploymentmodule.NativeDeliveryPlanRequestDigest(request)
	return err == nil && i.Validate() == nil && i.PublisherID == i.Receipt.ActorID && i.SourceOwnerID == i.Receipt.ActorID && i.PreparationID == request.FirstSourcePreparationID && i.PlanRequestDigest == digest && i.PlanOperation == request.Operation && i.PlanIdempotencyKey == request.IdempotencyKey && i.PublisherID == request.PrincipalID && i.SourceOwnerID == request.SourceOwnerID && i.SourceDigest == request.SourceDigest && i.SourceAttestationDigest == request.SourceAttestationDigest && i.ExpectedTargetRevision == revision && i.Receipt.Binding.ProjectID == request.ProjectID.String() && i.Receipt.Binding.TargetID == request.TargetID && i.Receipt.Binding.Environment == request.Environment
}

func firstSourceCredentialEvidence(binding connectionbinding.TargetBinding, i credentialmodule.FirstSourcePreparationIntent) []deployment.CandidateConnectionEvidence {
	return []deployment.CandidateConnectionEvidence{{BindingID: binding.ID.String(), ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind, Revision: binding.Revision, CredentialVersionID: i.Receipt.Binding.VersionID, EndpointConfigHash: binding.Evidence().EndpointConfigHash}}
}

func (p *firstSourceCredentialPlan) BindTx(ctx context.Context, tx pgx.Tx, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentpostgres.DeliveryTarget, plan deployment.DeliveryPlan) error {
	if !p.available() || ctx == nil || typednil.IsNil(tx) {
		return credentialmodule.ErrValidationUnavailable
	}
	stored, err := p.journal.StoredPreparationTx(ctx, tx, request.TargetID, request.FirstSourcePreparationID)
	if err != nil {
		return err
	}
	binding, err := p.checkIntentTx(ctx, tx, stored, request, target)
	if err != nil {
		return err
	}
	digest, err := deployment.BindingFingerprint(firstSourceCredentialEvidence(binding, stored.Preparation.Intent))
	if err != nil {
		return err
	}
	if !firstSourcePlanMatches(plan, request, target.TargetRevision) || plan.Execution.BindingDigest != digest {
		return credentialmodule.ErrValidationConflict
	}
	link := credentialmodule.FirstSourcePlanLink{TargetID: request.TargetID, PreparationID: request.FirstSourcePreparationID, PlanID: plan.ID, RequestDigest: stored.Preparation.Intent.PlanRequestDigest, BindingDigest: digest}
	return p.journal.LinkPlanTx(ctx, tx, link, func(ctx context.Context, tx pgx.Tx, intent credentialmodule.FirstSourcePreparationIntent, selected credentialmodule.FirstSourcePlanLink) error {
		if selected != link || intent.PreparationID != stored.Preparation.Intent.PreparationID {
			return credentialmodule.ErrValidationConflict
		}
		_, err := p.checkIntentTx(ctx, tx, stored, request, target)
		return err
	})
}

func firstSourcePlanMatches(plan deployment.DeliveryPlan, request deploymentmodule.NativeDeliveryPlanRequest, revision int64) bool {
	return plan.BaseGenerationID == "" && plan.BaseTargetRevision == revision && plan.ProjectID == request.ProjectID && plan.TargetID == request.TargetID && plan.Environment == request.Environment && plan.ActorID == request.PrincipalID && plan.SourceOwnerID == request.SourceOwnerID && string(plan.Operation) == request.Operation && plan.SourceDigest == request.SourceDigest && plan.Provenance.AttestationDigest == request.SourceAttestationDigest
}

func (p *firstSourceCredentialPlan) ValidateReplayTx(ctx context.Context, tx pgx.Tx, request deploymentmodule.NativeDeliveryPlanRequest, plan deployment.DeliveryPlan) error {
	if !p.available() || ctx == nil || typednil.IsNil(tx) {
		return credentialmodule.ErrValidationUnavailable
	}
	stored, err := p.journal.StoredPlanLinkTx(ctx, tx, request.TargetID, plan.ID)
	if err != nil {
		return err
	}
	i, link := stored.Preparation.Preparation.Intent, stored.Link
	if !firstSourcePlanIntentMatches(i, request, plan.BaseTargetRevision) || !firstSourcePlanMatches(plan, request, i.ExpectedTargetRevision) || link.PreparationID != request.FirstSourcePreparationID || link.PlanID != plan.ID || link.RequestDigest != i.PlanRequestDigest || link.BindingDigest != plan.Execution.BindingDigest {
		return credentialmodule.ErrValidationConflict
	}
	return nil
}
