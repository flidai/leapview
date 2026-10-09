package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/stretchr/testify/require"
)

func TestNativeActivationExecutionRetainsTransactionAndBoundsContinuation(t *testing.T) {
	f := newNativePGFixture(t)
	created, err := f.coordinator.Create(t.Context(), nativeCreateRequest(f, "coordinated-create"))
	require.NoError(t, err)
	request := apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "coordinated-activate"}
	interrupted := errors.New("interrupted before CAS")
	f.coordinator.beforeActivationCommit = func(context.Context, deploymentpostgres.Tx, deploymentpostgres.DeliveryPublication) error {
		return interrupted
	}
	var escaped NativeActivationContinuation
	f.coordinator.activationExecution = func(ctx context.Context, publication deploymentpostgres.DeliveryPublication, run NativeActivationContinuation) (apiadapter.Deployment, error) {
		require.Equal(t, created.ID, publication.PublicationID)
		require.Equal(t, request.Actor, publication.ActorID)
		escaped = run
		return run(ctx, nil)
	}
	_, err = f.coordinator.ActivateApprovedPublication(t.Context(), request.DeploymentID, request.Actor, request.IdempotencyKey)
	require.ErrorIs(t, err, interrupted)
	_, err = escaped(t.Context(), nil)
	require.Error(t, err)
	target, err := f.repo.Target(t.Context(), f.targetID)
	require.NoError(t, err)
	require.Empty(t, target.ActiveGenerationID)
	require.EqualValues(t, 1, target.TargetRevision)
	f.coordinator.beforeActivationCommit = nil
	active, err := f.coordinator.ActivateApprovedPublication(t.Context(), request.DeploymentID, request.Actor, request.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, apiadapter.StatusActive, active.Status)
	_, err = escaped(t.Context(), nil)
	require.Error(t, err)
	// Lost-ack replay still goes through the ordinary native operation record.
	replayed, err := f.coordinator.ActivateApprovedPublication(t.Context(), request.DeploymentID, request.Actor, request.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, active.ID, replayed.ID)
	target, err = f.repo.Target(t.Context(), f.targetID)
	require.NoError(t, err)
	require.EqualValues(t, 2, target.TargetRevision)
}

func TestNativeActivationExecutionRejectsUnexecutedAndRepeatedSuccess(t *testing.T) {
	f := newNativePGFixture(t)
	created, err := f.coordinator.Create(t.Context(), nativeCreateRequest(f, "unexecuted-create"))
	require.NoError(t, err)
	request := apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: created.ID}, Actor: "operator", IdempotencyKey: "unexecuted-activate"}
	f.coordinator.activationExecution = func(context.Context, deploymentpostgres.DeliveryPublication, NativeActivationContinuation) (apiadapter.Deployment, error) {
		return apiadapter.Deployment{ID: created.ID, Status: apiadapter.StatusActive}, nil
	}
	_, err = f.coordinator.ActivateApprovedPublication(t.Context(), request.DeploymentID, request.Actor, request.IdempotencyKey)
	require.Error(t, err)
	target, err := f.repo.Target(t.Context(), f.targetID)
	require.NoError(t, err)
	require.Empty(t, target.ActiveGenerationID)
	f.coordinator.activationExecution = func(ctx context.Context, _ deploymentpostgres.DeliveryPublication, run NativeActivationContinuation) (apiadapter.Deployment, error) {
		active, err := run(ctx, nil)
		require.NoError(t, err)
		_, repeated := run(ctx, nil)
		require.Error(t, repeated)
		return active, nil
	}
	_, err = f.coordinator.ActivateApprovedPublication(t.Context(), request.DeploymentID, request.Actor, request.IdempotencyKey)
	require.NoError(t, err)
}
