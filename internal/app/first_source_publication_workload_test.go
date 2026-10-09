package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/workload"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
	"github.com/stretchr/testify/require"
)

func TestFirstSourcePublicationRuntimePreservesApprovedJobAdmission(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	request := workloadmodule.ControlRequest("delivery.approval.activate")
	request.PrincipalID = "independent-reviewer"
	outer, err := controller.Acquire(t.Context(), request)
	require.NoError(t, err)
	defer outer.Release()
	source := &sourceCredentialActivation{config: sourceCredentialConfig{CandidateAdmission: candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))}}
	invocation := &firstSourcePublicationInvocation{owner: &firstSourcePublication{source: source}}
	invocation.active.Store(true)
	ctx := context.WithValue(outer.Context(), firstSourcePublicationKey{}, invocation)
	err = source.withRuntimeAdmission(ctx, func(ctx context.Context) error {
		actual, ok := workload.CurrentRequest(ctx)
		require.True(t, ok)
		require.Equal(t, request, actual)
		require.Equal(t, 1, controller.Stats().Running)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, controller.Stats().Running)
	invocation.active.Store(false)
	require.Error(t, source.withRuntimeAdmission(ctx, func(context.Context) error { t.Fatal("expired publication scope reached runtime"); return nil }))
	invocation.active.Store(true)
	invocation.owner.source = &sourceCredentialActivation{}
	require.Error(t, source.withRuntimeAdmission(ctx, func(context.Context) error { t.Fatal("foreign publication scope reached runtime"); return nil }))
}
