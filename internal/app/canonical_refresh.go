package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

type canonicalRefreshDeliveryReader interface {
	deployment.DeliveryReader
	ActiveDeliveryGenerationForTarget(context.Context, string, string, string) (deployment.DeliveryGeneration, error)
}

func canonicalRefreshExecutor(
	mutations deploymentmodule.DeliveryMutationPort,
	reader canonicalRefreshDeliveryReader,
	targetID string,
) func(context.Context, refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
	return func(ctx context.Context, job refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
		if mutations == nil || reader == nil || targetID == "" {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh delivery is unavailable")
		}
		planKey := "refresh-plan-" + job.RunID
		expectedPlanID := deploymentmodule.CanonicalDeliveryPlanID(
			targetID, job.Identity.ProjectID, job.Identity.Environment,
			deployment.DeliveryOperationRestatement, planKey,
		)
		candidateID := "candidate-" + strings.TrimPrefix(deployment.CanonicalDeliveryDigest([]byte(expectedPlanID+"\x00refresh-build-"+job.RunID)), "sha256:")
		publicationID := "publication-" + strings.TrimPrefix(deployment.CanonicalDeliveryDigest([]byte("candidate-publication:"+candidateID)), "sha256:")
		if recovered, found, recoverErr := recoverCanonicalRefreshPublication(ctx, reader, job, targetID, expectedPlanID, candidateID, publicationID); recoverErr != nil {
			return refreshrun.CanonicalRefreshResult{}, recoverErr
		} else if found {
			return recovered, nil
		}
		active, err := reader.ActiveDeliveryGenerationForTarget(
			ctx, targetID, job.Identity.ProjectID.String(), job.Identity.Environment,
		)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve canonical refresh base: %w", err)
		}
		if active.ServingStateID != job.Identity.GenerationID {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh base changed before execution: %w", refreshrun.ErrRunStale)
		}
		candidate, err := reader.DeliveryCandidateByID(ctx, active.CandidateID)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve canonical refresh candidate: %w", err)
		}
		basePlan, err := reader.PlanByID(ctx, candidate.PlanID)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve canonical refresh plan: %w", err)
		}
		pipelinePlan := job.PipelinePlan
		if pipelinePlan == nil {
			pipelinePlan = basePlan.PipelinePlan
		}
		if pipelinePlan != nil {
			if pipelinePlan.ServingGenerationID != job.Identity.GenerationID {
				return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh pipeline plan generation changed: %w", refreshrun.ErrRunStale)
			}
			if pipelinePlan.PipelineID != job.PipelineID.String() {
				return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh pipeline plan pipeline identity changed")
			}
		}
		sourceOwnerID := basePlan.SourceOwnerID
		if sourceOwnerID == "" {
			sourceOwnerID = basePlan.ActorID
		}
		plan, err := mutations.CreatePlan(ctx, deploymentmodule.DeliveryPlanIntent{
			ProjectID:               job.Identity.ProjectID,
			PrincipalID:             job.PrincipalID,
			SourceOwnerID:           sourceOwnerID,
			Environment:             job.Identity.Environment,
			TargetID:                targetID,
			Operation:               deployment.DeliveryOperationRestatement,
			SourceDigest:            basePlan.SourceDigest,
			SourceAttestationDigest: basePlan.Provenance.AttestationDigest,
			PipelinePlan:            pipelinePlan,
		}, planKey)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, canonicalRefreshOperationError("plan canonical refresh", err)
		}
		if plan.ID != expectedPlanID {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh plan identity changed")
		}
		if plan.BaseGenerationID != active.ID {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh base changed during planning: %w", refreshrun.ErrRunStale)
		}
		attempt, err := mutations.BuildPlan(
			ctx, job.Identity.ProjectID.String(), plan.ID, job.PrincipalID, "refresh-build-"+job.RunID,
		)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, canonicalRefreshOperationError("build canonical refresh", err)
		}
		if attempt.CandidateID == "" || attempt.Status != deployment.DeliveryBuildSealed {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh build did not produce a sealed candidate")
		}
		if attempt.QualifiedSnapshotID <= 0 {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh build omitted qualified snapshot evidence")
		}
		publisher, ok := mutations.(deploymentmodule.RefreshFencedDeliveryMutationPort)
		if !ok {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh publication fence is unavailable")
		}
		publication, err := publisher.PublishCandidateFenced(
			ctx, job.Identity.ProjectID.String(), attempt.CandidateID, job.PrincipalID, "refresh-publish-"+job.RunID,
			deployment.RefreshPublicationFence{RunID: job.RunID, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision, TargetRevision: job.TargetRevision},
		)
		if err != nil {
			// Publication can be durably committed before the prepared-runtime
			// acknowledgement returns (for example, a lease/transport failure
			// after the target CAS). Treat that outcome as success so the refresh
			// workflow can converge through its idempotent completion path.
			if publication.Status != deployment.DeliveryPublicationCommitted {
				return refreshrun.CanonicalRefreshResult{}, canonicalRefreshOperationError("publish canonical refresh", err)
			}
		}
		if publication.Status != deployment.DeliveryPublicationCommitted {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh publication ended in %q", publication.Status)
		}
		generation, err := reader.DeliveryGenerationByID(ctx, publication.GenerationID)
		if err != nil {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("resolve canonical refresh publication generation: %w", err)
		}
		if generation.PlanID != plan.ID || generation.ServingStateID == "" {
			return refreshrun.CanonicalRefreshResult{}, fmt.Errorf("canonical refresh publication generation identity changed")
		}
		return refreshrun.CanonicalRefreshResult{PlanID: plan.ID, ServingStateID: generation.ServingStateID, SnapshotID: attempt.QualifiedSnapshotID}, nil
	}
}

func canonicalRefreshOperationError(operation string, err error) error {
	if errors.Is(err, deployment.ErrDeliveryStale) {
		return fmt.Errorf("%s: %w: %v", operation, refreshrun.ErrRunStale, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func recoverCanonicalRefreshPublication(
	ctx context.Context,
	reader canonicalRefreshDeliveryReader,
	job refreshrun.JobRecord,
	targetID, planID, candidateID, publicationID string,
) (refreshrun.CanonicalRefreshResult, bool, error) {
	publication, err := reader.DeliveryPublicationByID(ctx, publicationID)
	if errors.Is(err, deployment.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		return refreshrun.CanonicalRefreshResult{}, false, nil
	}
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication: %w", err)
	}
	if publication.Status != deployment.DeliveryPublicationCommitted {
		return refreshrun.CanonicalRefreshResult{}, false, nil
	}
	if publication.ID != publicationID || publication.TargetID != targetID || publication.ProjectID != job.Identity.ProjectID || publication.Environment != job.Identity.Environment || publication.PlanID != planID || publication.CandidateID != candidateID {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("canonical refresh publication identity changed: %w", refreshrun.ErrLeaseLost)
	}
	base, err := reader.DeliveryGenerationByID(ctx, publication.ExpectedBaseGenerationID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication base: %w", err)
	}
	generation, err := reader.DeliveryGenerationByID(ctx, publication.GenerationID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication generation: %w", err)
	}
	if base.ServingStateID != job.Identity.GenerationID || generation.ID != publication.GenerationID || generation.PlanID != planID || generation.CandidateID != candidateID || generation.ServingStateID == "" {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("canonical refresh publication generation identity changed: %w", refreshrun.ErrLeaseLost)
	}
	candidate, err := reader.DeliveryCandidateByID(ctx, candidateID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication candidate: %w", err)
	}
	seal, err := reader.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication seal: %w", err)
	}
	attempt, err := reader.DeliveryBuildAttemptByID(ctx, seal.AttemptID)
	if err != nil {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("resolve canonical refresh publication build: %w", err)
	}
	if candidate.ID != candidateID || candidate.PlanID != planID || candidate.Status != deployment.DeliveryCandidateReady || seal.ID != candidate.SealID || seal.AttemptID == "" || attempt.ID != seal.AttemptID || attempt.PlanID != planID || attempt.CandidateID != candidateID || attempt.Status != deployment.DeliveryBuildSealed || attempt.QualifiedSnapshotID <= 0 {
		return refreshrun.CanonicalRefreshResult{}, false, fmt.Errorf("canonical refresh publication snapshot evidence changed: %w", refreshrun.ErrLeaseLost)
	}
	return refreshrun.CanonicalRefreshResult{PlanID: planID, ServingStateID: generation.ServingStateID, SnapshotID: attempt.QualifiedSnapshotID}, true, nil
}
