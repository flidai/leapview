package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

type unpublishedTargetFence interface {
	WithUnpublishedTarget(context.Context, string, string, string, func(context.Context) error) error
}

func configureInitialReviewerBootstrap(module *accessmodule.Module, claims deploymentmodule.ProjectClaimReader, targets deliveryTargetReader, instanceID, environment string) {
	fence, ok := targets.(unpublishedTargetFence)
	if module == nil || claims == nil || !ok {
		return
	}
	module.SetInitialReviewerBootstrap(func(r *http.Request, nominate func(context.Context, access.AuthorizationPolicyScope, string) error) (bool, error) {
		// Existing published installations use ordinary grant authority, even
		// if they predate the durable first-project claim contract.
		target, err := targets.DeliveryTargetRevision(r.Context(), instanceID)
		if err != nil && !errors.Is(err, deployment.ErrNotFound) {
			return true, err
		}
		if err == nil {
			if target.TargetID != instanceID || target.Environment != environment {
				return true, deployment.ErrProjectClaimConflict
			}
			if target.ActiveGenerationID != "" || target.ActivePublicationID != "" {
				return false, nil
			}
		}
		claim, err := claims.GetProjectClaim(r.Context())
		if errors.Is(err, deployment.ErrProjectClaimNotFound) {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		if claim.Validate() != nil || string(claim.Environment) != environment {
			return true, deployment.ErrProjectClaimInvalid
		}
		scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: claim.ProjectID.String(), Environment: environment}
		err = fence.WithUnpublishedTarget(r.Context(), instanceID, scope.ProjectID, environment, func(ctx context.Context) error {
			return nominate(ctx, scope, claim.ClaimedBy)
		})
		if errors.Is(err, appdeploymentpostgres.ErrTargetAlreadyPublished) {
			return false, nil
		}
		return true, err
	})
}
