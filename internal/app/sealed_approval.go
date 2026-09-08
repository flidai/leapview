package app

import (
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
)

// validateSealedPublicationPlanBinding verifies the durable plan that a
// publication claims before the caller can take an approval bypass. The plan
// remains deployment-owned; this helper only reuses its existing canonical
// validation and digest authority.
func validateSealedPublicationPlanBinding(
	plan deployment.DeliveryPlan,
	binding sealedcontrol.SealBinding,
	publication deployment.PublicationIntent,
	now time.Time,
) error {
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("%w: delivery plan: %v", sealedcontrol.ErrSealUnverified, err)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: delivery plan verification time is required", sealedcontrol.ErrSealUnverified)
	}
	if plan.Status != deployment.DeliveryPlanPlanned {
		return fmt.Errorf("%w: delivery plan is not planned", sealedcontrol.ErrSealUnverified)
	}
	if plan.Expired(now) {
		return fmt.Errorf("%w: delivery plan has expired", deployment.ErrDeliveryPlanExpired)
	}
	if binding.DeploymentID != publication.ID ||
		binding.ProjectID != publication.ProjectID.String() ||
		binding.Environment != publication.Environment ||
		binding.TargetID != publication.TargetID ||
		binding.CandidateID != publication.CandidateID ||
		binding.GenerationID != publication.GenerationID ||
		binding.PlanDigest != publication.PlanDigest ||
		plan.ID != publication.PlanID ||
		plan.Digest != publication.PlanDigest ||
		plan.TargetID != publication.TargetID ||
		plan.ProjectID != publication.ProjectID ||
		plan.Environment != publication.Environment ||
		plan.BaseGenerationID != publication.ExpectedBaseGenerationID ||
		plan.BaseTargetRevision != publication.ExpectedTargetRevision {
		return fmt.Errorf("%w: publication does not bind its durable delivery plan", sealedcontrol.ErrSealUnverified)
	}
	return nil
}
