package app

import (
	"context"
	"errors"
	"testing"

	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/workload"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/stretchr/testify/require"
)

func runSourceCredentialRuntime(t *testing.T, source *sourceCredentialActivation, path string, ctx context.Context, run func(context.Context) error) error {
	t.Helper()
	if path == "restore" {
		source.config.Restore = run
		return source.RestoreCurrent(ctx)
	}
	record := credentialmodule.ActivationRecord{Status: credentialmodule.ActivationStatus{State: "committed", GenerationID: "generation:test"}}
	source.config.Install = func(ctx context.Context, actual credentialmodule.ActivationRecord) error {
		require.Equal(t, record, actual)
		return run(ctx)
	}
	return source.installRuntime(ctx, record)
}

func TestSourceCredentialRuntimeUsesAndReleasesAdmission(t *testing.T) {
	failure := errors.New("runtime reconstruction failed")
	for _, path := range []string{"install", "restore"} {
		for _, outcome := range []string{"success", "failure", "cancel"} {
			t.Run(path+"/"+outcome, func(t *testing.T) {
				original := context.WithValue(t.Context(), sourceCredentialOperationKey{}, "operation")
				admitted, cancel := context.WithCancel(original)
				defer cancel()
				lease := &credentialBuildWorkloadLease{ctx: admitted}
				source := &sourceCredentialActivation{config: sourceCredentialConfig{CandidateAdmission: deploymentmodule.CandidatePreparationAdmitterFunc(func(ctx context.Context) (deploymentmodule.CandidatePreparationLease, error) {
					require.Same(t, original, ctx)
					return lease, nil
				})}}
				err := runSourceCredentialRuntime(t, source, path, original, func(ctx context.Context) error {
					require.Same(t, admitted, ctx)
					require.Equal(t, "operation", ctx.Value(sourceCredentialOperationKey{}))
					require.Zero(t, lease.releases)
					switch outcome {
					case "failure":
						return failure
					case "cancel":
						cancel()
						require.Zero(t, lease.releases)
						return ctx.Err()
					default:
						return nil
					}
				})
				switch outcome {
				case "failure":
					require.ErrorIs(t, err, failure)
				case "cancel":
					require.ErrorIs(t, err, context.Canceled)
				default:
					require.NoError(t, err)
				}
				require.Equal(t, 1, lease.releases)
			})
		}
	}
}

func TestSourceCredentialRuntimeRejectsUnavailableAdmission(t *testing.T) {
	denied := errors.New("workload denied")
	for _, path := range []string{"install", "restore"} {
		for _, unavailable := range []string{"missing-admitter", "denied", "nil-lease", "nil-context"} {
			t.Run(path+"/"+unavailable, func(t *testing.T) {
				lease := &credentialBuildWorkloadLease{}
				source := &sourceCredentialActivation{}
				if unavailable != "missing-admitter" {
					source.config.CandidateAdmission = deploymentmodule.CandidatePreparationAdmitterFunc(func(context.Context) (deploymentmodule.CandidatePreparationLease, error) {
						switch unavailable {
						case "denied":
							return nil, denied
						case "nil-lease":
							return nil, nil
						default:
							return lease, nil
						}
					})
				}
				err := runSourceCredentialRuntime(t, source, path, t.Context(), func(context.Context) error { t.Fatal("runtime reconstructed without admission"); return nil })
				if unavailable == "denied" {
					require.ErrorIs(t, err, denied)
				} else {
					require.ErrorIs(t, err, credentialmodule.ErrValidationUnavailable)
				}
				if unavailable == "nil-context" {
					require.Equal(t, 1, lease.releases)
				} else {
					require.Zero(t, lease.releases)
				}
			})
		}
	}
}

func TestSourceCredentialRuntimePreservesGovernedAccounting(t *testing.T) {
	for _, path := range []string{"install", "restore"} {
		for _, class := range []workload.Class{workload.Control, workload.Refresh} {
			t.Run(path+"/"+string(class), func(t *testing.T) {
				controller, err := workload.New(workload.DefaultConfig())
				require.NoError(t, err)
				t.Cleanup(controller.Close)
				expected := workloadmodule.ControlRequest("candidate.prepare")
				ctx := t.Context()
				if class == workload.Refresh {
					expected = workload.Request{Class: workload.Refresh, PrincipalID: "actor", GroupIDs: []string{"team"}, Operation: "refresh", EstimatedMemoryBytes: 64 << 20}
					outer, err := controller.Acquire(ctx, expected)
					require.NoError(t, err)
					t.Cleanup(outer.Release)
					ctx = outer.Context()
				}
				source := &sourceCredentialActivation{config: sourceCredentialConfig{CandidateAdmission: candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))}}
				err = runSourceCredentialRuntime(t, source, path, ctx, func(ctx context.Context) error {
					actual, ok := workload.CurrentRequest(ctx)
					require.True(t, ok, "snapshot reconstruction must carry real workload admission")
					require.Equal(t, expected, actual)
					require.Equal(t, 1, controller.Stats().Running)
					return nil
				})
				require.NoError(t, err)
				if class == workload.Refresh {
					require.Equal(t, 1, controller.Stats().Running)
				} else {
					require.Zero(t, controller.Stats().Running)
				}
			})
		}
	}
}
