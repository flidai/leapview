package app

import (
	"context"
	"errors"
	"sync/atomic"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
)

// Only the normal reviewer-approved publication worker enters this adapter.
// Its continuation still owns approval, native command replay and the CAS. The
// shared credential coordinator owns draining, runtime installation and restart.
type firstSourcePublication struct {
	source     *sourceCredentialActivation
	build      *firstSourceNativeBuild
	activation func() credentialmodule.ActivationService
	repository func(credentialmodule.ActivationRequestRecord, credentialmodule.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, error)
}

type firstSourcePublicationKey struct{}
type firstSourcePublicationInvocation struct {
	owner       *firstSourcePublication
	publication deploymentpostgres.DeliveryPublication
	plan        deployment.DeliveryPlan
	stored      credentialmodule.FirstSourceStoredPlanLink
	active      atomic.Bool
	run         deploymentmodule.NativeActivationContinuation
	result      apiadapter.Deployment
	executed    bool
}

func (p *firstSourcePublication) Execute(ctx context.Context, publication deploymentpostgres.DeliveryPublication, run deploymentmodule.NativeActivationContinuation) (apiadapter.Deployment, error) {
	if p == nil || p.source == nil || p.build == nil || p.activation == nil || p.repository == nil || run == nil {
		return apiadapter.Deployment{}, credentialmodule.ErrValidationUnavailable
	}
	generation, err := p.source.config.Reader.LoadGeneration(ctx, publication.GenerationID)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	plan, err := p.build.load(ctx, generation.PlanID)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	stored, err := p.build.connections.lookup(ctx, plan)
	if errors.Is(err, credentialmodule.ErrValidationNotFound) {
		return run(ctx, nil)
	}
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if generation.TargetID != publication.TargetID || generation.CandidateID != publication.CandidateID || generation.SnapshotSealID != publication.SnapshotSealID || generation.PlanDigest != plan.Digest || !plan.Governance.RequiresApproval || publication.ActorID != plan.ActorID || publication.ExpectedBaseGenerationID != "" {
		return apiadapter.Deployment{}, credentialmodule.ErrValidationConflict
	}
	invocation := &firstSourcePublicationInvocation{owner: p, publication: publication, plan: plan, stored: stored, run: run}
	invocation.active.Store(true)
	defer invocation.active.Store(false)
	ctx = context.WithValue(ctx, firstSourcePublicationKey{}, invocation)
	row := stored.Preparation.Reservation
	_, err = p.activation().RetryActivation(ctx, row.Receipt.ActorID, row.Resource(), row.Request.OperationID, row.Request.ReceiptID)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if !invocation.executed {
		// Completed/committed credential recovery does not call Commit again.
		// Still verify the exact ordinary native publication replay outcome.
		return run(ctx, nil)
	}
	return invocation.result, nil
}

type firstSourcePublicationAuthority struct {
	credentialmodule.ActivationAuthority
	publication *firstSourcePublication
}

func (a *firstSourcePublicationAuthority) invocation(ctx context.Context, actor string, resource credentialmodule.ValidationResource) (*firstSourcePublicationInvocation, error) {
	i, ok := ctx.Value(firstSourcePublicationKey{}).(*firstSourcePublicationInvocation)
	if !ok {
		return nil, nil
	}
	if i == nil || i.owner != a.publication || !i.active.Load() || actor != i.publication.ActorID || resource != i.stored.Preparation.Reservation.Resource() {
		return nil, credentialmodule.ErrValidationConflict
	}
	return i, nil
}

func (a *firstSourcePublicationAuthority) AuthorizeMutation(ctx context.Context, actor string, resource credentialmodule.ValidationResource) error {
	i, err := a.invocation(ctx, actor, resource)
	if err != nil {
		return err
	}
	if i == nil {
		if _, building := ctx.Value(firstSourceBuildInvocationKey{}).(*firstSourceBuildInvocation); building {
			// The inner build authority validates this private, bounded scope and
			// the publisher's real API credential before any provider work.
			return a.ActivationAuthority.AuthorizeMutation(ctx, actor, resource)
		}
		unpublished, err := a.publication.unpublished(ctx, resource)
		if err != nil {
			return err
		}
		if unpublished {
			return a.publication.publicRead(ctx, actor, resource, "", nil)
		}
		return a.ActivationAuthority.AuthorizeMutation(ctx, actor, resource)
	}
	return a.publication.source.transaction(ctx, func(tx pgx.Tx) error { _, err := a.publication.validateTx(ctx, tx, i); return err })
}

func (a *firstSourcePublicationAuthority) Read(ctx context.Context, actor string, resource credentialmodule.ValidationResource, operation string) (credentialmodule.ActivationRecord, error) {
	i, err := a.invocation(ctx, actor, resource)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if i == nil {
		unpublished, err := a.publication.unpublished(ctx, resource)
		if err != nil {
			return credentialmodule.ActivationRecord{}, err
		}
		if unpublished {
			var row credentialmodule.ActivationRecord
			err := a.publication.publicRead(ctx, actor, resource, operation, func(_ context.Context, _ pgx.Tx, stored credentialmodule.FirstSourceStoredPreparation) error {
				row = stored.Reservation.ActivationRecord()
				return nil
			})
			return row, err
		}
		return a.ActivationAuthority.Read(ctx, actor, resource, operation)
	}
	if operation != i.stored.Link.PreparationID {
		return credentialmodule.ActivationRecord{}, credentialmodule.ErrValidationConflict
	}
	row, err := a.publication.source.config.Credentials.GetActivationRequest(ctx, resource.TargetID, operation)
	return row.ActivationRecord(), err
}
