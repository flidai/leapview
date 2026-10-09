package app

import (
	"context"
	"errors"
	"sync/atomic"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/typednil"
)

// The ordinary native coordinator still authorizes, builds and completes the
// command. This adapter only places an explicitly linked first-source build
// inside the existing serialized activation preparation admission interval.
type firstSourceNativeBuild struct {
	sourceCredentialMutations
	connections *firstSourceCredentialBuild
	load        func(context.Context, string) (deployment.DeliveryPlan, error)
	activation  func() credentialmodule.ActivationService
}

type firstSourceBuildInvocationKey struct{}
type firstSourceBuildInvocation struct {
	owner    *firstSourceNativeBuild
	plan     deployment.DeliveryPlan
	stored   credentialmodule.FirstSourceStoredPlanLink
	active   atomic.Bool
	executed atomic.Bool
	run      func(context.Context) error
}

func (b *firstSourceNativeBuild) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	if b == nil || ctx == nil || typednil.IsNil(b.sourceCredentialMutations) || b.connections == nil || b.load == nil || b.activation == nil {
		return deploymentmodule.NativeDeliveryBuild{}, credentialmodule.ErrValidationUnavailable
	}
	plan, err := b.load(ctx, request.PlanID.String())
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	stored, err := b.connections.lookup(ctx, plan)
	if errors.Is(err, credentialmodule.ErrValidationNotFound) {
		return b.sourceCredentialMutations.BuildPlan(ctx, request)
	}
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if plan.ID != request.PlanID.String() || plan.TargetID != request.TargetID || plan.ProjectID != request.ProjectID || plan.Environment != request.Environment || plan.ActorID != request.PrincipalID {
		return deploymentmodule.NativeDeliveryBuild{}, credentialmodule.ErrValidationConflict
	}
	if stored.Preparation.Reservation.State != "preparing" {
		// Native historical replay remains available. A new physical attempt
		// still reaches the configured selector, which rejects terminal intent.
		return b.sourceCredentialMutations.BuildPlan(ctx, request)
	}
	service := b.activation()
	if typednil.IsNil(service) {
		return deploymentmodule.NativeDeliveryBuild{}, credentialmodule.ErrValidationUnavailable
	}
	var result deploymentmodule.NativeDeliveryBuild
	invocation := &firstSourceBuildInvocation{owner: b, plan: plan, stored: stored}
	invocation.run = func(ctx context.Context) error {
		var err error
		result, err = b.sourceCredentialMutations.BuildPlan(ctx, request)
		if err != nil {
			return err
		}
		return b.sourceCredentialMutations.CompleteNativeBuildCommand(ctx, result)
	}
	invocation.active.Store(true)
	defer invocation.active.Store(false)
	ctx = context.WithValue(ctx, firstSourceBuildInvocationKey{}, invocation)
	row := stored.Preparation.Reservation
	_, err = service.StartActivation(ctx, request.PrincipalID, row.Resource(), row.Request)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if !invocation.executed.Load() {
		return deploymentmodule.NativeDeliveryBuild{}, credentialmodule.ErrValidationConflict
	}
	return result, nil
}

type firstSourceBuildActivation struct {
	credentialmodule.ActivationAuthority
	build *firstSourceNativeBuild
}

func (a *firstSourceBuildActivation) invocation(ctx context.Context, actor string, resource credentialmodule.ValidationResource) (*firstSourceBuildInvocation, error) {
	invocation, ok := ctx.Value(firstSourceBuildInvocationKey{}).(*firstSourceBuildInvocation)
	if !ok {
		return nil, nil
	}
	if invocation == nil || invocation.owner != a.build || !invocation.active.Load() || actor != invocation.plan.ActorID || resource != invocation.stored.Preparation.Reservation.Resource() {
		return nil, credentialmodule.ErrValidationConflict
	}
	_, _, err := a.build.connections.check(ctx, invocation.plan, invocation.stored.Link)
	return invocation, err
}

func (a *firstSourceBuildActivation) AuthorizeMutation(ctx context.Context, actor string, resource credentialmodule.ValidationResource) error {
	invocation, err := a.invocation(ctx, actor, resource)
	if err != nil {
		return err
	}
	if invocation != nil {
		return nil
	}
	return a.ActivationAuthority.AuthorizeMutation(ctx, actor, resource)
}

func (a *firstSourceBuildActivation) Prepare(ctx context.Context, actor string, resource credentialmodule.ValidationResource, request credentialmodule.ActivationRequest) (credentialmodule.ActivationRecord, error) {
	invocation, err := a.invocation(ctx, actor, resource)
	if err != nil {
		return credentialmodule.ActivationRecord{}, err
	}
	if invocation == nil {
		return a.ActivationAuthority.Prepare(ctx, actor, resource, request)
	}
	row := invocation.stored.Preparation.Reservation
	if request != row.Request || row.State != "preparing" || !invocation.executed.CompareAndSwap(false, true) || invocation.run == nil {
		return credentialmodule.ActivationRecord{}, credentialmodule.ErrValidationConflict
	}
	return row.ActivationRecord(), invocation.run(ctx)
}
