package deploymentpostgres

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

func validateNativeCreatePlanRequest(request deploymentmodule.NativeDeliveryPlanRequest) error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", deployment.ErrDeliveryInvalid, err)
	}
	for label, value := range map[string]string{
		"target": request.TargetID, "environment": request.Environment, "principal": request.PrincipalID,
		"source digest": request.SourceDigest, "source attestation digest": request.SourceAttestationDigest,
		"idempotency key": request.IdempotencyKey,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s is required and canonical", deployment.ErrDeliveryInvalid, label)
		}
	}
	if request.SourceOwnerID != strings.TrimSpace(request.SourceOwnerID) {
		return fmt.Errorf("%w: source owner is not canonical", deployment.ErrDeliveryInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(request.SourceDigest); err != nil {
		return fmt.Errorf("%w: source digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if err := platformdigest.ValidateSHA256Identity(request.SourceAttestationDigest); err != nil {
		return fmt.Errorf("%w: source attestation digest: %v", deployment.ErrDeliveryInvalid, err)
	}
	if request.PipelinePlan != nil {
		canonical := request.PipelinePlan.Canonical()
		if err := canonical.Validate(); err != nil {
			return fmt.Errorf("%w: pipeline plan: %v", deployment.ErrDeliveryInvalid, err)
		}
		if canonical.ProjectID != request.ProjectID.String() || canonical.Environment != request.Environment || canonical.ArtifactDigest != request.SourceDigest {
			return fmt.Errorf("%w: pipeline plan identity differs from native delivery request", deployment.ErrDeliveryConflict)
		}
	}
	switch deployment.DeliveryOperationKind(request.Operation) {
	case deployment.DeliveryOperationCodeChange, deployment.DeliveryOperationRestatement, deployment.DeliveryOperationBindingChange, deployment.DeliveryOperationPolicyChange:
	default:
		return fmt.Errorf("%w: unsupported delivery operation %q", deployment.ErrDeliveryInvalid, request.Operation)
	}
	return nil
}
