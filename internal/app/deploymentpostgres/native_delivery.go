package deploymentpostgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

type nativePlanMutationCoordinator interface {
	CreatePlan(context.Context, deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error)
	CompleteNativePlanCommand(context.Context, deploymentmodule.NativeDeliveryPlan) error
}

type nativeBuildMutationCoordinator interface {
	BuildPlan(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error)
	CompleteNativeBuildCommand(context.Context, deploymentmodule.NativeDeliveryBuild) error
}

// NativeDeliveryCoordinator is the production plan/build mutation port. Plan
// authoring and physical build orchestration retain separate authorities and
// transaction boundaries; this value only presents their complete interface
// to the deployment HTTP module, including generated-command completion.
type NativeDeliveryCoordinator struct {
	plan  nativePlanMutationCoordinator
	build nativeBuildMutationCoordinator
}

var _ deploymentmodule.NativeDeliveryMutationPort = (*NativeDeliveryCoordinator)(nil)
var _ deploymentmodule.NativeDeliveryCommandCompleter = (*NativeDeliveryCoordinator)(nil)

// NewNativeDeliveryCoordinator composes the already-validated plan and build
// coordinators without granting either one the other's authorities.
func NewNativeDeliveryCoordinator(plan nativePlanMutationCoordinator, build nativeBuildMutationCoordinator) (*NativeDeliveryCoordinator, error) {
	if nativeBuildAuthorityNil(plan) || nativeBuildAuthorityNil(build) {
		return nil, errors.New("native delivery plan and build coordinators are required")
	}
	return &NativeDeliveryCoordinator{plan: plan, build: build}, nil
}

// WithCandidateConnectionAuthorities returns an execution-scoped coordinator
// whose planning and build binding evidence and connection leases come from
// the same caller-owned authorities. The coordinator structs contain only
// immutable configuration and service dependencies; per-command state stays
// local to their methods, so replacing these two dependencies on copies does
// not mutate the process-wide coordinator.
func (c *NativeDeliveryCoordinator) WithCandidateConnectionAuthorities(
	connections deployment.CandidateConnectionLeaser,
	evidence deployment.CandidateConnectionEvidenceResolver,
) (*NativeDeliveryCoordinator, error) {
	if c == nil || nativeBuildAuthorityNil(connections) || nativeBuildAuthorityNil(evidence) {
		return nil, deploymentmodule.ErrDeliveryInputUnavailable
	}
	plan, ok := c.plan.(*NativeCreatePlanCoordinator)
	if !ok || plan == nil {
		return nil, deploymentmodule.ErrDeliveryInputUnavailable
	}
	build, ok := c.build.(*NativeBuildCoordinator)
	if !ok || build == nil {
		return nil, deploymentmodule.ErrDeliveryInputUnavailable
	}
	planCopy := *plan
	planCopy.bindingEvidence = evidence
	buildCopy := *build
	buildCopy.connections = connections
	buildCopy.bindingEvidence = evidence
	return &NativeDeliveryCoordinator{plan: &planCopy, build: &buildCopy}, nil
}

func (c *NativeDeliveryCoordinator) CreatePlan(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	if c == nil || nativeBuildAuthorityNil(c.plan) {
		return deploymentmodule.NativeDeliveryPlan{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	return c.plan.CreatePlan(ctx, request)
}

func (c *NativeDeliveryCoordinator) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	if c == nil || nativeBuildAuthorityNil(c.build) {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	return c.build.BuildPlan(ctx, request)
}

func (c *NativeDeliveryCoordinator) CompleteNativePlanCommand(ctx context.Context, plan deploymentmodule.NativeDeliveryPlan) error {
	if c == nil || nativeBuildAuthorityNil(c.plan) {
		return deploymentmodule.ErrDeliveryInputUnavailable
	}
	return c.plan.CompleteNativePlanCommand(ctx, plan)
}

func (c *NativeDeliveryCoordinator) CompleteNativeBuildCommand(ctx context.Context, build deploymentmodule.NativeDeliveryBuild) error {
	if c == nil || nativeBuildAuthorityNil(c.build) {
		return deploymentmodule.ErrDeliveryInputUnavailable
	}
	return c.build.CompleteNativeBuildCommand(ctx, build)
}
