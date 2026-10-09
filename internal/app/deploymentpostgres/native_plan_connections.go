package deploymentpostgres

import (
	"context"
	"fmt"

	deployment "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

// NativePlanConnectionAuthorities keeps plan inspection and physical connection
// acquisition on the same explicitly selected credential authority.
type NativePlanConnectionAuthorities struct {
	BindingEvidence deployment.CandidateConnectionEvidenceResolver
	Connections     deployment.CandidateConnectionLeaser
}

// NativePlanConnectionSelector selects authority from an exact persisted plan.
// It must not infer a version or preparation from a current draft or request.
type NativePlanConnectionSelector func(context.Context, deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error)

func (c *NativeBuildCoordinator) selectPlanConnections(ctx context.Context, plan deployment.DeliveryPlan) (NativePlanConnectionAuthorities, error) {
	if c == nil {
		return NativePlanConnectionAuthorities{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	if c.planConnections == nil {
		return NativePlanConnectionAuthorities{BindingEvidence: c.bindingEvidence, Connections: c.connections}, nil
	}
	selected, err := c.planConnections(ctx, plan)
	if err != nil {
		return NativePlanConnectionAuthorities{}, fmt.Errorf("select exact plan connection authorities: %w", err)
	}
	if nativeBuildAuthorityNil(selected.BindingEvidence) || nativeBuildAuthorityNil(selected.Connections) {
		return NativePlanConnectionAuthorities{}, fmt.Errorf("%w: selected exact plan connection authorities are incomplete", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	return selected, nil
}
