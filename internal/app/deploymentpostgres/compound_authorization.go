package deploymentpostgres

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

func (c *NativeCreatePlanCoordinator) authorizeCompoundPlan(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest, inspected release.CandidateArtifactSet, evidence []deployment.CandidateConnectionEvidence) (*deployment.DeliveryAuthorizationExecution, error) {
	if c.authorizeDelivery == nil {
		if c.requireCompoundAuthorization {
			return nil, fmt.Errorf("%w: native delivery compound authorization resolver is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
		}
		return nil, nil
	}
	if err := inspected.AuthorizationSnapshot.ValidateBound(); err != nil {
		return nil, fmt.Errorf("resolve native delivery authorization snapshot: %w", err)
	}
	bindings := make([]deployment.DeliveryConnectionBinding, 0, len(evidence))
	for _, item := range evidence {
		connection, err := access.NewResourceRef(item.ConnectionID, projectgraph.KindConnection)
		if err != nil {
			return nil, fmt.Errorf("build native delivery binding authority: %w", err)
		}
		bindings = append(bindings, deployment.DeliveryConnectionBinding{BindingID: item.BindingID, Connection: connection, EvidenceDigest: item.EndpointConfigHash})
	}
	plan, err := deployment.DeliveryAuthorizationPlanFromBundleWithBindings(
		request.ProjectID, request.TargetID, inspected.Compiler.Graph, inspected.Compiler.Plan, bindings,
		[]access.Action{access.ActionDeliveryPlan},
	)
	if err != nil {
		return nil, fmt.Errorf("build native delivery authorization plan: %w", err)
	}
	snapshot := inspected.AuthorizationSnapshot
	plan, err = plan.BindSnapshot(snapshot)
	if err != nil {
		return nil, fmt.Errorf("bind native delivery authorization snapshot: %w", err)
	}
	execution, err := c.authorizeDelivery(ctx, request.PrincipalID, plan, snapshot)
	if err != nil {
		return nil, fmt.Errorf("authorize native delivery plan: %w", err)
	}
	if err := plan.ValidateExecution(execution); err != nil {
		return nil, fmt.Errorf("validate native delivery authorization evidence: %w", err)
	}
	return &execution, nil
}
