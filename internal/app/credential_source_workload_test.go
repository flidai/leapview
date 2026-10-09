package app

import (
	"context"
	"errors"
	"testing"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/workload"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type credentialBuildWorkloadMutations struct {
	sourceCredentialMutations
	build    func(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error)
	complete func(context.Context, deploymentmodule.NativeDeliveryBuild) error
}

func TestSourceCredentialBuildPreservesGovernedWorkloadAccounting(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		name := "standalone-control"
		if inherited {
			name = "inherited-refresh"
		}
		t.Run(name, func(t *testing.T) {
			controller, err := workload.New(workload.DefaultConfig())
			require.NoError(t, err)
			t.Cleanup(controller.Close)
			ctx := context.WithValue(t.Context(), sourceCredentialOperationKey{}, "operation")
			expected := workloadmodule.ControlRequest("candidate.prepare")
			if inherited {
				expected = workload.Request{Class: workload.Refresh, PrincipalID: "actor", GroupIDs: []string{"team"}, Operation: "refresh", EstimatedMemoryBytes: 64 << 20}
				outer, err := controller.Acquire(ctx, expected)
				require.NoError(t, err)
				t.Cleanup(outer.Release)
				ctx = outer.Context()
			}
			plan := deploymentmodule.NativeDeliveryPlan{ID: uuid.New(), PlanDigest: "plan-digest"}
			valid := deploymentmodule.NativeDeliveryBuild{PlanID: plan.ID, PlanDigest: plan.PlanDigest, CandidateID: uuid.New(), ServingStateID: uuid.New(), SealID: uuid.New()}
			check := func(ctx context.Context) {
				actual, ok := workload.CurrentRequest(ctx)
				require.True(t, ok, "physical build must carry real workload admission")
				require.Equal(t, expected, actual)
				require.Equal(t, "operation", ctx.Value(sourceCredentialOperationKey{}))
				require.Equal(t, 1, controller.Stats().Running)
			}
			source := &sourceCredentialActivation{config: sourceCredentialConfig{
				CandidateAdmission: candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare")),
				Mutations: credentialBuildWorkloadMutations{
					build: func(ctx context.Context, _ deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
						check(ctx)
						return valid, nil
					},
					complete: func(ctx context.Context, _ deploymentmodule.NativeDeliveryBuild) error { check(ctx); return nil },
				},
			}}
			_, err = source.buildNative(ctx, "actor", "operation", plan)
			require.NoError(t, err)
			if inherited {
				require.Equal(t, 1, controller.Stats().Running)
			} else {
				require.Zero(t, controller.Stats().Running)
			}
		})
	}
}

func (m credentialBuildWorkloadMutations) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
	return m.build(ctx, request)
}
func (m credentialBuildWorkloadMutations) CompleteNativeBuildCommand(ctx context.Context, build deploymentmodule.NativeDeliveryBuild) error {
	return m.complete(ctx, build)
}

type credentialBuildWorkloadLease struct {
	ctx      context.Context
	releases int
}

func (l *credentialBuildWorkloadLease) Context() context.Context { return l.ctx }
func (l *credentialBuildWorkloadLease) Release()                 { l.releases++ }

func TestSourceCredentialBuildRequiresWorkloadAdmission(t *testing.T) {
	denied := errors.New("workload denied")
	for _, scenario := range []string{"missing-admitter", "denied", "nil-lease", "nil-context"} {
		t.Run(scenario, func(t *testing.T) {
			lease := &credentialBuildWorkloadLease{}
			source := &sourceCredentialActivation{config: sourceCredentialConfig{Mutations: credentialBuildWorkloadMutations{
				build: func(context.Context, deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
					t.Fatal("unadmitted physical build")
					return deploymentmodule.NativeDeliveryBuild{}, nil
				},
			}}}
			expected := error(credentialmodule.ErrValidationUnavailable)
			if scenario != "missing-admitter" {
				source.config.CandidateAdmission = deploymentmodule.CandidatePreparationAdmitterFunc(func(context.Context) (deploymentmodule.CandidatePreparationLease, error) {
					switch scenario {
					case "denied":
						return nil, denied
					case "nil-lease":
						return nil, nil
					default:
						return lease, nil
					}
				})
			}
			if scenario == "denied" {
				expected = denied
			}
			_, err := source.buildNative(t.Context(), "actor", "operation", deploymentmodule.NativeDeliveryPlan{})
			require.ErrorIs(t, err, expected)
			if scenario == "nil-context" {
				require.Equal(t, 1, lease.releases)
			} else {
				require.Zero(t, lease.releases)
			}
		})
	}
}

func TestSourceCredentialBuildUsesAndReleasesAdmission(t *testing.T) {
	buildFailure, completionFailure := errors.New("build failed"), errors.New("completion failed")
	plan := deploymentmodule.NativeDeliveryPlan{ID: uuid.New(), ProjectID: "project:test", TargetID: "target:test", Environment: "prod", PlanDigest: "plan-digest", BaseGenerationID: uuid.New()}
	valid := deploymentmodule.NativeDeliveryBuild{PlanID: plan.ID, PlanDigest: plan.PlanDigest, CandidateID: uuid.New(), ServingStateID: uuid.New(), SealID: uuid.New(), BaseGenerationID: plan.BaseGenerationID}
	for _, scenario := range []string{"success", "build-error", "invalid-build", "completion-error", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			original := context.WithValue(t.Context(), sourceCredentialOperationKey{}, "operation")
			admitted, cancel := context.WithCancel(original)
			defer cancel()
			lease := &credentialBuildWorkloadLease{ctx: admitted}
			completed := false
			source := &sourceCredentialActivation{config: sourceCredentialConfig{
				CandidateAdmission: deploymentmodule.CandidatePreparationAdmitterFunc(func(ctx context.Context) (deploymentmodule.CandidatePreparationLease, error) {
					require.Same(t, original, ctx)
					return lease, nil
				}),
				Mutations: credentialBuildWorkloadMutations{
					build: func(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (deploymentmodule.NativeDeliveryBuild, error) {
						require.Same(t, admitted, ctx)
						require.Equal(t, "operation", ctx.Value(sourceCredentialOperationKey{}))
						require.Zero(t, lease.releases)
						require.Equal(t, deploymentmodule.NativeDeliveryBuildRequest{ProjectID: plan.ProjectID, TargetID: plan.TargetID, Environment: plan.Environment, PlanID: plan.ID, PrincipalID: "actor", IdempotencyKey: "credential-build-operation"}, request)
						switch scenario {
						case "build-error":
							return valid, buildFailure
						case "invalid-build":
							return deploymentmodule.NativeDeliveryBuild{}, nil
						case "cancellation":
							cancel()
							require.Zero(t, lease.releases)
							return valid, ctx.Err()
						}
						return valid, nil
					},
					complete: func(ctx context.Context, build deploymentmodule.NativeDeliveryBuild) error {
						require.Same(t, admitted, ctx)
						require.Zero(t, lease.releases)
						require.Equal(t, valid, build)
						completed = true
						if scenario == "completion-error" {
							return completionFailure
						}
						return nil
					},
				},
			}}
			result, err := source.buildNative(original, "actor", "operation", plan)
			switch scenario {
			case "success":
				require.NoError(t, err)
				require.Equal(t, valid, result)
			case "build-error":
				require.ErrorIs(t, err, buildFailure)
			case "invalid-build":
				require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
			case "completion-error":
				require.ErrorIs(t, err, completionFailure)
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, scenario == "success" || scenario == "completion-error", completed)
			require.Equal(t, 1, lease.releases)
		})
	}
}
