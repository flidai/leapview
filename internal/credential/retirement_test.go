package credential

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type retirementAuthorityFake struct {
	status   VersionStatus
	onRetire func()
	retired  bool
}

func (a *retirementAuthorityFake) InspectVersion(context.Context, string, Resource, string) (VersionStatus, error) {
	return a.status, nil
}
func (a *retirementAuthorityFake) RetireVersion(context.Context, string, Resource, string) (VersionStatus, error) {
	a.retired = true
	if a.onRetire != nil {
		a.onRetire()
	}
	a.status.State = "retired_local"
	return a.status, nil
}
func TestRetirementDoesNotResumeOverConcurrentPendingActivation(t *testing.T) {
	events := []string{}
	authority := &activationAuthorityFake{events: &events}
	runtime := &activationRuntimeFake{events: &events}
	gate := NewProviderAdmission()
	service, err := NewActivationCoordinator(authority, runtime, gate, time.Second)
	require.NoError(t, err)
	require.NoError(t, gate.Resume())
	retire := &retirementAuthorityFake{status: VersionStatus{State: "available"}, onRetire: func() { authority.record.Status.State = "prepared" }}
	require.NoError(t, service.ConfigureRetirement(retire))
	_, err = service.RetireVersion(t.Context(), "actor", Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}, uuid.NewString())
	require.True(t, retire.retired)
	require.ErrorIs(t, err, ErrConflict)
	require.False(t, gate.Ready(), "pending activation must keep provider admission paused")
}
func TestRetirementDrainsProviderWorkAndRestoresCurrentClients(t *testing.T) {
	events := []string{}
	authority := &activationAuthorityFake{events: &events}
	runtime := &activationRuntimeFake{events: &events}
	gate := NewProviderAdmission()
	service, err := NewActivationCoordinator(authority, runtime, gate, time.Second)
	require.NoError(t, err)
	require.NoError(t, gate.Resume())
	retire := &retirementAuthorityFake{status: VersionStatus{State: "available"}, onRetire: func() { require.False(t, gate.Ready()) }}
	require.NoError(t, service.ConfigureRetirement(retire))
	status, err := service.RetireVersion(t.Context(), "actor", Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, "retired_local", status.State)
	require.Equal(t, []string{"restore"}, events)
	require.True(t, gate.Ready())
	retire.status.Dependencies = []VersionDependency{{Kind: "release", ID: "release-1"}}
	retire.status.State = "available"
	retire.retired = false
	_, err = service.RetireVersion(t.Context(), "actor", Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}, uuid.NewString())
	require.True(t, errors.Is(err, ErrConflict))
	require.False(t, retire.retired)
	require.True(t, gate.Ready())
}

type retirementRuntimeWithFailure struct {
	*activationRuntimeFake
	restoreErr error
}

func (r *retirementRuntimeWithFailure) RestoreCurrent(ctx context.Context) error {
	if r.restoreErr != nil {
		return r.restoreErr
	}
	return r.activationRuntimeFake.RestoreCurrent(ctx)
}
func TestRetirementReplayRepairsAdmissionAfterCommittedRestoreFailure(t *testing.T) {
	events := []string{}
	authority := &activationAuthorityFake{events: &events}
	runtime := &retirementRuntimeWithFailure{activationRuntimeFake: &activationRuntimeFake{events: &events}, restoreErr: ErrUnavailable}
	gate := NewProviderAdmission()
	service, err := NewActivationCoordinator(authority, runtime, gate, time.Second)
	require.NoError(t, err)
	require.NoError(t, gate.Resume())
	retirement := &retirementAuthorityFake{status: VersionStatus{State: "available"}}
	require.NoError(t, service.ConfigureRetirement(retirement))
	resource := Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
	version := uuid.NewString()
	status, err := service.RetireVersion(t.Context(), "actor", resource, version)
	require.ErrorIs(t, err, ErrUnavailable)
	require.Equal(t, "retired_local", status.State)
	require.False(t, gate.Ready())
	runtime.restoreErr = nil
	status, err = service.RetireVersion(t.Context(), "actor", resource, version)
	require.NoError(t, err)
	require.Equal(t, "retired_local", status.State)
	require.True(t, gate.Ready(), "same-version replay must repair admission after committed retirement")
}
