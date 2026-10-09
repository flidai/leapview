package deploymentpostgres

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

// NativeFirstSourcePlanAuthority binds an explicit preparation to the ordinary
// delivery plan. It does not authorize the publisher or replace semantic review.
// Resolve returns non-secret evidence before the control transaction; BindTx
// must recheck live admission, operator, reservation and receipt authority using
// the supplied transaction after the native target and policy fences are held.
// ValidateReplayTx checks only the immutable stored relationship, including
// after publication. A replay must never reserve, renew or revive preparation.
type NativeFirstSourcePlanAuthority interface {
	Resolve(context.Context, deploymentmodule.NativeDeliveryPlanRequest, deploymentnative.DeliveryTarget, deployment.CandidateConnectionRequest) ([]deployment.CandidateConnectionEvidence, error)
	BindTx(context.Context, deploymentnative.Tx, deploymentmodule.NativeDeliveryPlanRequest, deploymentnative.DeliveryTarget, deployment.DeliveryPlan) error
	ValidateReplayTx(context.Context, deploymentnative.Tx, deploymentmodule.NativeDeliveryPlanRequest, deployment.DeliveryPlan) error
}

func (c *NativeCreatePlanCoordinator) resolvePlanBindings(ctx context.Context, request deploymentmodule.NativeDeliveryPlanRequest, target deploymentnative.DeliveryTarget, bindings deployment.CandidateConnectionRequest) ([]deployment.CandidateConnectionEvidence, string, error) {
	if request.FirstSourcePreparationID == "" {
		return resolveNativeCandidateBindingEvidence(ctx, c.bindingEvidence, bindings)
	}
	if nativeBuildAuthorityNil(c.firstSource) {
		return nil, "", deploymentmodule.ErrDeliveryInputUnavailable
	}
	if target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
		return nil, "", fmt.Errorf("%w: first-source planning requires an unpublished target", deployment.ErrDeliveryConflict)
	}
	evidence, err := c.firstSource.Resolve(ctx, request, target, bindings)
	if err != nil {
		return nil, "", err
	}
	if err := validateNativeCandidateBindingEvidence(bindings, evidence); err != nil {
		return nil, "", err
	}
	digest, err := deployment.BindingFingerprint(evidence)
	if err != nil {
		return nil, "", err
	}
	return append([]deployment.CandidateConnectionEvidence(nil), evidence...), digest, nil
}

func (c *NativeCreatePlanCoordinator) validateFirstSourcePlanReplay(ctx context.Context, tx deploymentnative.Tx, request deploymentmodule.NativeDeliveryPlanRequest, plan deployment.DeliveryPlan) error {
	if request.FirstSourcePreparationID == "" {
		return nil
	}
	if nativeBuildAuthorityNil(c.firstSource) {
		return deploymentmodule.ErrDeliveryInputUnavailable
	}
	return c.firstSource.ValidateReplayTx(ctx, tx, request, plan)
}
