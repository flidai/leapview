package deploymentpostgres

import (
	"context"
	"fmt"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/release"
)

// revalidateNativeCompoundAuthorization checks persisted execution evidence
// against the current candidate graph before admitting physical build work.
func (c *NativeBuildCoordinator) revalidateNativeCompoundAuthorization(ctx context.Context, principalID string, plan deploymentdomain.DeliveryPlan, inspected release.CandidateArtifactSet) error {
	if plan.Authorization == nil {
		if c.requireCompoundAuthorization {
			return fmt.Errorf("%w: persisted native plan compound authorization evidence is absent", deploymentdomain.ErrDeliveryConflict)
		}
		return nil
	}
	if c.authorizeDelivery == nil {
		return fmt.Errorf("%w: native build compound authorization resolver is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if err := inspected.AuthorizationSnapshot.ValidateBound(); err != nil {
		return fmt.Errorf("%w: current native delivery authorization snapshot is unavailable: %v", deploymentdomain.ErrDeliveryConflict, err)
	}
	authority, err := plan.AuthorizationPlan()
	if err != nil {
		return err
	}
	execution, err := c.authorizeDelivery(ctx, principalID, authority, inspected.AuthorizationSnapshot)
	if err != nil {
		return fmt.Errorf("revalidate native delivery authorization: %w", err)
	}
	if err := authority.ValidateExecution(execution); err != nil {
		return fmt.Errorf("revalidate native delivery authorization evidence: %w", err)
	}
	return nil
}
