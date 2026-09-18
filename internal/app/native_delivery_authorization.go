package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
)

// nativeDeliveryAuthorization binds subject resolution to the same candidate
// snapshot used by native plan/build authorities.
func nativeDeliveryAuthorization(subjects func(context.Context, string) ([]access.SubjectRef, error)) appdeploymentpostgres.NativeDeliveryAuthorization {
	return func(ctx context.Context, principalID string, plan deployment.DeliveryAuthorizationPlan, snapshot accesssnapshot.AuthorizationSnapshot) (deployment.DeliveryAuthorizationExecution, error) {
		resolved, err := subjects(ctx, principalID)
		if err != nil {
			return deployment.DeliveryAuthorizationExecution{}, err
		}
		return deployment.EvaluateDeliveryAuthorizationPlan(plan, snapshot, resolved)
	}
}
