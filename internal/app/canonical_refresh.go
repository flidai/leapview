package app

import (
	"context"
	"fmt"

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
) func(context.Context, refreshrun.JobRecord) error {
	return func(ctx context.Context, job refreshrun.JobRecord) error {
		if mutations == nil || reader == nil || targetID == "" {
			return fmt.Errorf("canonical refresh delivery is unavailable")
		}
		active, err := reader.ActiveDeliveryGenerationForTarget(
			ctx, targetID, job.Identity.ProjectID.String(), job.Identity.Environment,
		)
		if err != nil {
			return fmt.Errorf("resolve canonical refresh base: %w", err)
		}
		if active.ServingStateID != job.Identity.GenerationID {
			return fmt.Errorf("canonical refresh base changed before execution: %w", refreshrun.ErrLeaseLost)
		}
		candidate, err := reader.DeliveryCandidateByID(ctx, active.CandidateID)
		if err != nil {
			return fmt.Errorf("resolve canonical refresh candidate: %w", err)
		}
		basePlan, err := reader.PlanByID(ctx, candidate.PlanID)
		if err != nil {
			return fmt.Errorf("resolve canonical refresh plan: %w", err)
		}
		planKey := "refresh-plan-" + job.RunID
		plan, err := mutations.CreatePlan(ctx, deploymentmodule.DeliveryPlanIntent{
			ProjectID:               job.Identity.ProjectID,
			PrincipalID:             job.PrincipalID,
			Environment:             job.Identity.Environment,
			TargetID:                targetID,
			Operation:               deployment.DeliveryOperationRestatement,
			SourceDigest:            basePlan.SourceDigest,
			SourceAttestationDigest: basePlan.Provenance.AttestationDigest,
		}, planKey)
		if err != nil {
			return fmt.Errorf("plan canonical refresh: %w", err)
		}
		attempt, err := mutations.BuildPlan(
			ctx, job.Identity.ProjectID.String(), plan.ID, job.PrincipalID, "refresh-build-"+job.RunID,
		)
		if err != nil {
			return fmt.Errorf("build canonical refresh: %w", err)
		}
		if attempt.CandidateID == "" || attempt.Status != deployment.DeliveryBuildSealed {
			return fmt.Errorf("canonical refresh build did not produce a sealed candidate")
		}
		publication, err := mutations.PublishCandidate(
			ctx, job.Identity.ProjectID.String(), attempt.CandidateID, job.PrincipalID, "refresh-publish-"+job.RunID,
		)
		if err != nil {
			return fmt.Errorf("publish canonical refresh: %w", err)
		}
		if publication.Status != deployment.DeliveryPublicationCommitted {
			return fmt.Errorf("canonical refresh publication ended in %q", publication.Status)
		}
		return nil
	}
}
