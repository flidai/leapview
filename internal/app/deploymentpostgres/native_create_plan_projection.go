package deploymentpostgres

import (
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// richPlanFromRequest is the persistence projection for the native plan. The
// compound execution evidence remains part of the immutable rich plan.
func richPlanFromRequest(request deployment.DeliveryPlanRequest, sourceOwner, planID, baseGeneration string, baseRevision int64) (deployment.DeliveryPlan, error) {
	projectID, err := projectgraph.NewResourceID(request.ProjectID)
	if err != nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	return deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: planID, ActorID: request.ActorID, SourceOwnerID: sourceOwner,
		TargetID: request.TargetID, ProjectID: projectID, Environment: request.Environment,
		Operation: request.Operation, SourceDigest: request.SourceDigest, ServingArtifactDigest: request.ServingArtifactDigest,
		BaseGenerationID: baseGeneration, BaseTargetRevision: baseRevision,
		Execution: request.Execution, Provenance: request.Provenance, Governance: request.Governance,
		Evidence: request.Evidence, Authorization: request.Authorization, PipelinePlan: request.PipelinePlan, CreatedAt: request.CreatedAt,
	})
}
