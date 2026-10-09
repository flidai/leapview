package app

import (
	"context"
	"errors"
	"testing"
	"time"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type firstSourceBuildTestMutations struct {
	sourceCredentialMutations
	build    func(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error)
	complete func(context.Context, deploymentmodule.NativeDeliveryBuild) error
}

func (m firstSourceBuildTestMutations) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	return m.build(ctx, request)
}
func (m firstSourceBuildTestMutations) CompleteNativeBuildCommand(ctx context.Context, build deploymentmodule.NativeDeliveryBuild) error {
	return m.complete(ctx, build)
}

type firstSourceBuildTestRuntime struct {
	credentialmodule.ActivationRuntime
}

func TestFirstSourceNativeBuildUsesBoundedCoordinatorAdmission(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	gate := credentialmodule.NewProviderAdmission()
	_, _, err := gate.Acquire(f.ctx)
	require.Error(t, err, "ordinary work must stay paused before first publication")
	request := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PlanID: uuid.MustParse(plan.ID), PrincipalID: f.actor, IdempotencyKey: "first-source-native-build"}
	want := deploymentmodule.NativeDeliveryBuild{PlanID: request.PlanID, ActorID: f.actor}
	var escaped context.Context
	var calls, completed int
	native := firstSourceBuildTestMutations{build: func(ctx context.Context, got deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
		calls++
		require.Equal(t, request, got)
		require.False(t, gate.Ready())
		work, release, err := gate.Acquire(ctx)
		require.NoError(t, err)
		defer release()
		require.NoError(t, work.Err())
		escaped = ctx
		return want, nil
	}, complete: func(ctx context.Context, got deploymentmodule.NativeDeliveryBuild) error {
		completed++
		require.Equal(t, want, got)
		_, release, err := gate.Acquire(ctx)
		require.NoError(t, err, "completion remains in the same admission interval")
		release()
		return nil
	}}
	build := &firstSourceNativeBuild{sourceCredentialMutations: native, connections: &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets}, load: func(context.Context, string) (deployment.DeliveryPlan, error) { return plan, nil }}
	authority := &firstSourceBuildActivation{build: build}
	coordinator, err := credentialmodule.NewActivationCoordinator(authority, firstSourceBuildTestRuntime{}, gate, time.Second)
	require.NoError(t, err)
	build.activation = func() credentialmodule.ActivationService { return coordinator }
	got, err := build.BuildPlan(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, completed)
	require.False(t, gate.Ready(), "a successful build is not publication or runtime readiness")
	_, _, err = gate.Acquire(escaped)
	require.Error(t, err, "escaped preparation contexts cannot acquire later work")
	require.Error(t, authority.AuthorizeMutation(escaped, f.actor, f.resource))
	bounded, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, gate.Pause(bounded), "all provider handles closed before the build returned")
	tx, err := f.pool.Begin(t.Context())
	require.NoError(t, err)
	stored, err := f.service.preparations.StoredPlanLinkTx(t.Context(), tx, plan.TargetID, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "preparing", stored.Preparation.Reservation.State)
	require.Empty(t, stored.Preparation.Reservation.GenerationID)
	require.NoError(t, tx.Rollback(t.Context()))
	require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
	_, err = build.BuildPlan(f.ctx, request)
	require.Error(t, err)
	require.Equal(t, 1, calls, "revocation prevents reusing the exact prepared plan")
}

func TestFirstSourceNativeBuildFailureKeepsAdmissionClosed(t *testing.T) {
	f := newFirstSourcePlanAuthorityFixture(t)
	plan := f.linkedPlan(t)
	gate := credentialmodule.NewProviderAdmission()
	failure := errors.New("native completion failed")
	native := firstSourceBuildTestMutations{build: func(ctx context.Context, _ deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
		_, release, err := gate.Acquire(ctx)
		if err != nil {
			return deploymentmodule.NativeDeliveryBuild{}, err
		}
		defer release()
		return deploymentmodule.NativeDeliveryBuild{}, nil
	}, complete: func(context.Context, deploymentmodule.NativeDeliveryBuild) error { return failure }}
	build := &firstSourceNativeBuild{sourceCredentialMutations: native, connections: &firstSourceCredentialBuild{plan: f.authority, targets: f.service.targets}, load: func(context.Context, string) (deployment.DeliveryPlan, error) { return plan, nil }}
	coordinator, err := credentialmodule.NewActivationCoordinator(&firstSourceBuildActivation{build: build}, firstSourceBuildTestRuntime{}, gate, time.Second)
	require.NoError(t, err)
	build.activation = func() credentialmodule.ActivationService { return coordinator }
	_, err = build.BuildPlan(f.ctx, deploymentmodule.NativeDeliveryBuildRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PlanID: uuid.MustParse(plan.ID), PrincipalID: f.actor, IdempotencyKey: "failed-first-source-build"})
	require.ErrorIs(t, err, failure)
	require.False(t, gate.Ready())
	require.NoError(t, gate.Pause(t.Context()))
}
