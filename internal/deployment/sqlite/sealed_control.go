package sqlite

// SQLite-only publication rollback adapter. The selected generation already
// owns an immutable catalog seal; this transaction changes lifecycle pointers
// and never opens, mutates, or deletes DuckLake/object-store state.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// Rollback performs an idempotent target-fenced selection of one retained
// generation. The existing delivery_publications table is used as the durable
// request/result record, so a lost response can be reconciled by request ID.
func (r *Repository) Rollback(ctx context.Context, request deployment.RollbackRequest) (deployment.RollbackResult, error) {
	if err := request.Validate(); err != nil {
		return deployment.RollbackResult{}, err
	}
	now := request.CreatedAt.UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	defer tx.Rollback()

	generation, err := deliveryGenerationByIDTx(ctx, tx, request.GenerationID)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if generation.TargetID != request.TargetID || generation.ProjectID != request.ProjectID || generation.Environment != request.Environment || generation.CandidateID != request.CandidateID {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback generation scope differs", deployment.ErrDeliveryConflict)
	}
	if generation.CatalogDigest != request.VerifiedSeal.CatalogDigest || generation.CatalogObjectKey != request.VerifiedSeal.CatalogObjectKey || generation.PhysicalPoolID != request.VerifiedSeal.PhysicalPoolID {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback generation does not point to exact seal", deployment.ErrDeliveryConflict)
	}
	seal, err := deliveryCatalogSealByIDTx(ctx, tx, request.VerifiedSeal.SealID)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if seal.Status != deployment.CatalogSealVerified || seal.ID != request.VerifiedSeal.SealID || seal.CatalogDigest != request.VerifiedSeal.CatalogDigest || seal.ObjectKey != request.VerifiedSeal.CatalogObjectKey || seal.ObjectSize != request.VerifiedSeal.ObjectSize || seal.PhysicalPoolID != request.VerifiedSeal.PhysicalPoolID || seal.ClosureDigest != request.VerifiedSeal.ClosureDigest || seal.QualificationDigest != request.VerifiedSeal.QualificationDigest {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback seal is not the exact verified artifact", deployment.ErrDeliveryConflict)
	}
	candidate, err := deliveryCandidateByIDTx(ctx, tx, generation.CandidateID)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if candidate.SealID != request.VerifiedSeal.SealID || candidate.QualificationDigest != request.VerifiedSeal.QualificationDigest || candidate.CatalogDigest != request.VerifiedSeal.CatalogDigest || candidate.CatalogObjectKey != request.VerifiedSeal.CatalogObjectKey || candidate.PhysicalPoolID != request.VerifiedSeal.PhysicalPoolID {
		return deployment.RollbackResult{}, fmt.Errorf("%w: candidate does not bind exact verified seal", deployment.ErrDeliveryConflict)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, generation.PlanID)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	// A rollback request still belongs to the plan's governance window. Check
	// expiry before the generation's retention window so an independently
	// expired plan reports the precise plan-expired sentinel rather than a
	// lower-level stale rollback-window error.
	if plan.Status != deployment.DeliveryPlanPlanned || plan.Expired(now) {
		return deployment.RollbackResult{}, deployment.ErrDeliveryPlanExpired
	}

	// Resolve an existing request before checking the current fence. A
	// committed retry must converge even though the target is no longer at the
	// original expected base/revision.
	var publication deployment.DeliveryPublication
	publication, publicationErr := deliveryPublicationByIDTx(ctx, tx, request.ID)
	if publicationErr == nil {
		if publication.RequestDigest != request.RequestDigest || publication.TargetID != request.TargetID || publication.ProjectID != request.ProjectID || publication.Environment != request.Environment || publication.GenerationID != request.GenerationID || publication.CandidateID != candidate.ID || publication.PlanID != generation.PlanID || publication.PlanDigest != generation.PlanDigest || publication.ExpectedBaseGenerationID != request.ExpectedBaseGenerationID || publication.ExpectedTargetRevision != request.ExpectedTargetRevision {
			return deployment.RollbackResult{}, fmt.Errorf("%w: rollback request identity conflict", deployment.ErrDeliveryConflict)
		}
		if publication.Status == deployment.DeliveryPublicationCommitted {
			if err := tx.Commit(); err != nil {
				return deployment.RollbackResult{}, err
			}
			return rollbackResult(publication, generation), nil
		}
		if publication.Status != deployment.DeliveryPublicationPending && publication.Status != deployment.DeliveryPublicationIndeterminate {
			return deployment.RollbackResult{}, fmt.Errorf("%w: rollback publication is %s", deployment.ErrDeliveryTransition, publication.Status)
		}
	} else if publicationErr != sql.ErrNoRows {
		return deployment.RollbackResult{}, publicationErr
	}

	target, err := deploydb.New(tx).GetDeliveryTargetRevision(ctx, request.TargetID)
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	targetProject, targetEnvironment, revision, active := target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	if targetProject != request.ProjectID.String() || targetEnvironment != request.Environment {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback target scope changed", deployment.ErrDeliveryConflict)
	}
	if active == request.GenerationID && revision == request.ExpectedTargetRevision+1 && publicationErr == sql.ErrNoRows {
		return deployment.RollbackResult{}, fmt.Errorf("%w: active pointer changed without rollback record", deployment.ErrDeliveryStale)
	}
	if active != request.ExpectedBaseGenerationID || revision != request.ExpectedTargetRevision {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback target fence changed", deployment.ErrDeliveryStale)
	}

	if generation.Status == deployment.DeliveryGenerationRetired {
		if _, err := generation.Rollback(now); err != nil {
			return deployment.RollbackResult{}, err
		}
	} else {
		return deployment.RollbackResult{}, fmt.Errorf("%w: retained generation is %s", deployment.ErrDeliveryTransition, generation.Status)
	}
	if publicationErr == sql.ErrNoRows {
		err = deploydb.New(tx).CreateDeliveryPublication(ctx, deploydb.CreateDeliveryPublicationParams{ID: request.ID, RequestDigest: request.RequestDigest, TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment, PlanID: plan.ID, PlanDigest: plan.Digest, CandidateID: candidate.ID, GenerationID: generation.ID, NULLIF: request.ExpectedBaseGenerationID, ExpectedTargetRevision: request.ExpectedTargetRevision, CreatedAt: deliveryTime(request.CreatedAt)})
		if err != nil {
			return deployment.RollbackResult{}, fmt.Errorf("%w: rollback request identity conflict", deployment.ErrDeliveryConflict)
		}
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(request.TargetID, request.RequestDigest, "rollback_requested", "rollback", request.ID), TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment,
		ActorID: eventActor(request.ActorID), EventKind: "rollback_requested", ObjectKind: "rollback", ObjectID: request.ID, RequestDigest: request.RequestDigest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"generation_id": generation.ID}, CreatedAt: request.CreatedAt,
	}); err != nil {
		return deployment.RollbackResult{}, err
	}
	if active != "" {
		res, err := deploydb.New(tx).RetireDeliveryGeneration(ctx, deploydb.RetireDeliveryGenerationParams{RetiredAt: presentString(deliveryTime(now)), ID: active})
		if err != nil {
			return deployment.RollbackResult{}, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return deployment.RollbackResult{}, fmt.Errorf("%w: prior generation retirement CAS failed", deployment.ErrDeliveryConflict)
		}
	}
	res, err := deploydb.New(tx).ActivateRetiredDeliveryGeneration(ctx, deploydb.ActivateRetiredDeliveryGenerationParams{ActivatedAt: presentString(deliveryTime(now)), ID: generation.ID})
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback generation activation CAS failed", deployment.ErrDeliveryConflict)
	}
	res, err = deploydb.New(tx).AdvanceDeliveryTargetRevision(ctx, deploydb.AdvanceDeliveryTargetRevisionParams{ActiveGenerationID: presentString(generation.ID), UpdatedAt: deliveryTime(now), TargetID: request.TargetID, TargetRevision: request.ExpectedTargetRevision, ActiveGenerationID_2: sql.NullString{}, ActiveGenerationID_3: presentString(request.ExpectedBaseGenerationID)})
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback target revision CAS failed", deployment.ErrDeliveryStale)
	}
	res, err = deploydb.New(tx).CommitDeliveryPublication(ctx, deploydb.CommitDeliveryPublicationParams{ResultTargetRevision: request.ExpectedTargetRevision + 1, CompletedAt: presentString(deliveryTime(now)), ID: request.ID})
	if err != nil {
		return deployment.RollbackResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.RollbackResult{}, fmt.Errorf("%w: rollback publication completion CAS failed", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(request.TargetID, request.RequestDigest, "rollback_committed", "rollback", request.ID), TargetID: request.TargetID, ProjectID: request.ProjectID.String(), Environment: request.Environment,
		ActorID: eventActor(request.ActorID), EventKind: "rollback_committed", ObjectKind: "rollback", ObjectID: request.ID, RequestDigest: request.RequestDigest, PlanDigest: plan.Digest, ResultDigest: generation.CatalogDigest, Outcome: "accepted", Details: map[string]any{"generation_id": generation.ID, "target_revision": request.ExpectedTargetRevision + 1}, CreatedAt: now,
	}); err != nil {
		return deployment.RollbackResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.RollbackResult{}, err
	}
	return deployment.RollbackResult{RequestDigest: request.RequestDigest, TargetID: request.TargetID, GenerationID: generation.ID, TargetRevision: request.ExpectedTargetRevision + 1, CatalogDigest: generation.CatalogDigest, CatalogObjectKey: generation.CatalogObjectKey, Status: string(deployment.DeliveryPublicationCommitted), CompletedAt: now}, nil
}

func rollbackResult(publication deployment.DeliveryPublication, generation deployment.DeliveryGeneration) deployment.RollbackResult {
	return deployment.RollbackResult{RequestDigest: publication.RequestDigest, TargetID: publication.TargetID, GenerationID: publication.GenerationID, TargetRevision: publication.ResultTargetRevision, CatalogDigest: generation.CatalogDigest, CatalogObjectKey: generation.CatalogObjectKey, Status: string(publication.Status), CompletedAt: publication.CompletedAt}
}

// RollbackGeneration is a descriptive alias used by control-plane adapters.
func (r *Repository) RollbackGeneration(ctx context.Context, request deployment.RollbackRequest) (deployment.RollbackResult, error) {
	return r.Rollback(ctx, request)
}
