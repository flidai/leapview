package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

type nativePlanCoordinatorFake struct {
	created   deploymentmodule.NativeDeliveryPlan
	createErr error
	completed deploymentmodule.NativeDeliveryPlan
	finishErr error
}

func (f *nativePlanCoordinatorFake) CreatePlan(context.Context, deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	return f.created, f.createErr
}

func (f *nativePlanCoordinatorFake) CompleteNativePlanCommand(_ context.Context, plan deploymentmodule.NativeDeliveryPlan) error {
	f.completed = plan
	return f.finishErr
}

type nativeBuildCoordinatorFake struct {
	built     deploymentmodule.NativeDeliveryBuild
	buildErr  error
	completed deploymentmodule.NativeDeliveryBuild
	finishErr error
}

func (f *nativeBuildCoordinatorFake) BuildPlan(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	return f.built, f.buildErr
}

func (f *nativeBuildCoordinatorFake) CompleteNativeBuildCommand(_ context.Context, build deploymentmodule.NativeDeliveryBuild) error {
	f.completed = build
	return f.finishErr
}

func TestNativeDeliveryCoordinatorDelegatesPlanBuildAndCompletion(t *testing.T) {
	plan := &nativePlanCoordinatorFake{created: deploymentmodule.NativeDeliveryPlan{Status: "planned"}}
	build := &nativeBuildCoordinatorFake{built: deploymentmodule.NativeDeliveryBuild{Status: "sealed"}}
	coordinator, err := NewNativeDeliveryCoordinator(plan, build)
	if err != nil {
		t.Fatal(err)
	}
	created, err := coordinator.CreatePlan(t.Context(), deploymentmodule.NativeDeliveryPlanRequest{})
	if err != nil || created.Status != "planned" {
		t.Fatalf("CreatePlan() = (%#v, %v)", created, err)
	}
	built, err := coordinator.BuildPlan(t.Context(), deploymentmodule.NativeDeliveryBuildRequest{})
	if err != nil || built.Status != "sealed" {
		t.Fatalf("BuildPlan() = (%#v, %v)", built, err)
	}
	if err := coordinator.CompleteNativePlanCommand(t.Context(), created); err != nil || plan.completed.Status != created.Status {
		t.Fatalf("CompleteNativePlanCommand() = %v, completed %#v", err, plan.completed)
	}
	if err := coordinator.CompleteNativeBuildCommand(t.Context(), built); err != nil || build.completed.Status != built.Status {
		t.Fatalf("CompleteNativeBuildCommand() = %v, completed %#v", err, build.completed)
	}
}

func TestNativeDeliveryCoordinatorFailsClosedAndPropagatesErrors(t *testing.T) {
	if _, err := NewNativeDeliveryCoordinator(nil, &nativeBuildCoordinatorFake{}); err == nil {
		t.Fatal("constructor accepted nil plan coordinator")
	}
	var typedNil *nativeBuildCoordinatorFake
	if _, err := NewNativeDeliveryCoordinator(&nativePlanCoordinatorFake{}, typedNil); err == nil {
		t.Fatal("constructor accepted typed-nil build coordinator")
	}
	want := errors.New("coordinator failure")
	plan := &nativePlanCoordinatorFake{createErr: want, finishErr: want}
	build := &nativeBuildCoordinatorFake{buildErr: want, finishErr: want}
	coordinator, err := NewNativeDeliveryCoordinator(plan, build)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CreatePlan(t.Context(), deploymentmodule.NativeDeliveryPlanRequest{}); !errors.Is(err, want) {
		t.Fatalf("CreatePlan error = %v", err)
	}
	if _, err := coordinator.BuildPlan(t.Context(), deploymentmodule.NativeDeliveryBuildRequest{}); !errors.Is(err, want) {
		t.Fatalf("BuildPlan error = %v", err)
	}
	if err := coordinator.CompleteNativePlanCommand(t.Context(), deploymentmodule.NativeDeliveryPlan{}); !errors.Is(err, want) {
		t.Fatalf("CompleteNativePlanCommand error = %v", err)
	}
	if err := coordinator.CompleteNativeBuildCommand(t.Context(), deploymentmodule.NativeDeliveryBuild{}); !errors.Is(err, want) {
		t.Fatalf("CompleteNativeBuildCommand error = %v", err)
	}

	var nilCoordinator *NativeDeliveryCoordinator
	if _, err := nilCoordinator.CreatePlan(t.Context(), deploymentmodule.NativeDeliveryPlanRequest{}); !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("nil CreatePlan error = %v", err)
	}
	if _, err := nilCoordinator.BuildPlan(t.Context(), deploymentmodule.NativeDeliveryBuildRequest{}); !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("nil BuildPlan error = %v", err)
	}
}
