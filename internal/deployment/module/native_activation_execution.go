package module

import (
	"context"
	"sync"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// NativeActivationContinuation retains native approval, operation, lease, CAS,
// audit and semantic admission. A composition-owned repository may add an
// exact credential operation's transactional admission. Nil retains the normal
// repository. The continuation is synchronous, single-use and invocation-bound.
type NativeActivationContinuation func(context.Context, *deploymentpostgres.Repository) (apiadapter.Deployment, error)

// NativeActivationExecution coordinates application-owned provider draining and
// runtime installation around the ordinary native activation transaction. It
// cannot turn a skipped/failed continuation into a successful activation.
type NativeActivationExecution func(context.Context, deploymentpostgres.DeliveryPublication, NativeActivationContinuation) (apiadapter.Deployment, error)

func (c *nativeCoordinator) executeCoordinatedActivation(ctx context.Context, request apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	if c == nil || c.activationExecution == nil {
		return c.Activate(ctx, request)
	}
	project, err := validateNativeScope(request.Scope)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	publication, err := c.repository.Publication(ctx, request.DeploymentID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	target, err := c.repository.Target(ctx, publication.TargetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if publication.TargetID != c.targetID || target.ProjectID != project.String() || target.Environment != c.instanceEnv || publication.ActorID != request.Actor {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	var serial sync.Mutex
	active, used := true, false
	defer func() { serial.Lock(); active = false; serial.Unlock() }()
	var result apiadapter.Deployment
	var activationErr error
	_, err = c.activationExecution(ctx, publication, func(scoped context.Context, repository *deploymentpostgres.Repository) (apiadapter.Deployment, error) {
		serial.Lock()
		defer serial.Unlock()
		if !active || used || scoped == nil {
			return apiadapter.Deployment{}, deployment.ErrConflict
		}
		used = true
		coordinator := *c
		if repository != nil {
			coordinator.repository = repository
		}
		result, activationErr = coordinator.Activate(scoped, request)
		return result, activationErr
	})
	serial.Lock()
	defer serial.Unlock()
	active = false
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if !used {
		return apiadapter.Deployment{}, deployment.ErrConflict
	}
	return result, activationErr
}
