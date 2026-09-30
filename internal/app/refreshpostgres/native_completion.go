package refreshpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformtypednil "github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
)

type nativeCompletionGenerationReader interface {
	LoadGeneration(context.Context, string) (deploymentpostgres.DeliveryGeneration, error)
}

type nativeCompletionRuntimeHost interface {
	PrepareSealedActivation(context.Context, string, string) (*runtimehost.Prepared, error)
	ActivatePreparedContext(context.Context, *runtimehost.Prepared, func() error) error
}

// NewNativeCanonicalCompletionCoordinator prepares the exact admitted native
// generation before asking the runtime host to durably complete and publish it.
// The callback remains the runtime host's activation callback so durable
// completion precedes the process-local runtime pointer switch.
func NewNativeCanonicalCompletionCoordinator(
	targetID string,
	reader nativeCompletionGenerationReader,
	validateOwnership func(context.Context, deploymentdomain.Deployment) error,
	host nativeCompletionRuntimeHost,
) (refreshrun.CanonicalCompletionCoordinator, error) {
	if strings.TrimSpace(targetID) == "" || targetID != strings.TrimSpace(targetID) {
		return nil, errors.New("native refresh completion target ID must be canonical")
	}
	if platformtypednil.IsNil(reader) {
		return nil, errors.New("native refresh completion generation reader is unavailable")
	}
	if validateOwnership == nil {
		return nil, errors.New("native refresh completion ownership validator is unavailable")
	}
	if platformtypednil.IsNil(host) {
		return nil, errors.New("native refresh completion runtime host is unavailable")
	}
	return func(completionCtx context.Context, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, complete func() error) error {
		if completionCtx == nil {
			return errors.New("canonical refresh completion context is required")
		}
		if complete == nil {
			return errors.New("canonical refresh completion callback is required")
		}
		if result.ServingStateID == "" || result.ServingStateID != result.NativeGenerationID {
			return errors.New("canonical refresh result has no exact native serving generation")
		}
		identity := projectgraph.ServingIdentity{
			ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment,
			GenerationID: result.ServingStateID,
		}
		if err := identity.Validate(); err != nil {
			return fmt.Errorf("canonical refresh result identity is invalid: %w", err)
		}
		generation, err := reader.LoadGeneration(completionCtx, result.NativeGenerationID)
		if err != nil {
			return fmt.Errorf("resolve canonical refresh generation: %w", err)
		}
		if generation.GenerationID != result.NativeGenerationID || generation.TargetID != targetID || generation.PlanID != result.PlanID || generation.CandidateID == "" {
			return errors.New("canonical refresh generation has no exact native candidate binding")
		}
		candidate := deploymentdomain.Deployment{ServingIdentity: identity}
		if err := validateOwnership(completionCtx, candidate); err != nil {
			return fmt.Errorf("validate canonical refresh dashboard publication ownership: %w", err)
		}
		prepared, err := host.PrepareSealedActivation(completionCtx, result.ServingStateID, generation.CandidateID)
		if err != nil {
			return fmt.Errorf("prepare canonical refresh runtime: %w", err)
		}
		if prepared == nil {
			return errors.New("prepare canonical refresh runtime returned no runtime")
		}
		if err := host.ActivatePreparedContext(completionCtx, prepared, complete); err != nil {
			return fmt.Errorf("activate canonical refresh runtime: %w", err)
		}
		return nil
	}, nil
}
