package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/stretchr/testify/require"
)

type fakeLocalSessionAuthority struct {
	profile       cliapi.TargetProfile
	profileErr    error
	resolveErr    error
	loginRequest  accesscli.LoginRequest
	loginCalls    int
	notified      bool
	reboundOrigin string
}

func (authority *fakeLocalSessionAuthority) Profile(string) (cliapi.TargetProfile, error) {
	return authority.profile, authority.profileErr
}

func (authority *fakeLocalSessionAuthority) Resolve(context.Context, string) (accesscli.ResolvedCredential, error) {
	return accesscli.ResolvedCredential{SessionID: "session-retained"}, authority.resolveErr
}

func (authority *fakeLocalSessionAuthority) RebindLoopbackOrigin(_ string, expected cliapi.TargetProfile, origin string) error {
	authority.profile = expected
	authority.profile.Origin = origin
	authority.reboundOrigin = origin
	return nil
}

func (authority *fakeLocalSessionAuthority) Login(_ context.Context, request accesscli.LoginRequest, notify func(accesscli.DeviceChallenge)) (accesscli.LoginResult, error) {
	authority.loginCalls++
	authority.loginRequest = request
	notify(accesscli.DeviceChallenge{UserCode: "ABCD-EFGH", VerificationURI: request.Origin + "/device"})
	authority.notified = true
	return accesscli.LoginResult{SessionID: "session-local"}, nil
}

func TestEstablishLocalAuthoringSessionsUsesNormalScopedDeviceAuthority(t *testing.T) {
	request := localruntime.SessionRequest{TargetName: "local-checkout", Origin: "http://127.0.0.1:54321", InstanceID: "instance-local", Environment: "dev", ProjectID: "lvproject_test"}
	authority := &fakeLocalSessionAuthority{profileErr: cliapi.ErrProfileNotFound, resolveErr: errors.New("not signed in")}
	var output strings.Builder
	result, err := establishLocalAuthoringSessionsWith(t.Context(), authority, request, &output)
	require.NoError(t, err)
	require.Equal(t, "session-local", result.SessionID)
	require.Equal(t, request.TargetName, result.TargetName)
	require.True(t, authority.notified)
	require.Equal(t, request.TargetName, authority.loginRequest.Name)
	require.Equal(t, request.Origin, authority.loginRequest.Origin)
	require.Equal(t, request.InstanceID, authority.loginRequest.InstanceID)
	require.Equal(t, request.ProjectID, authority.loginRequest.ProjectID)
	require.False(t, authority.loginRequest.Headless)
	require.Equal(t, []string{"RESOURCE_USE", "RESOURCE_READ", "RESOURCE_EDIT", "RESOURCE_PUBLISH"}, authority.loginRequest.Capabilities)
	require.Contains(t, output.String(), "ABCD-EFGH")
}

func TestEstablishLocalAuthoringSessionsReusesExactCredential(t *testing.T) {
	request := localruntime.SessionRequest{TargetName: "local-checkout", Origin: "http://127.0.0.1:54321", InstanceID: "instance-local", Environment: "dev", ProjectID: "lvproject_test"}
	authority := &fakeLocalSessionAuthority{profile: cliapi.TargetProfile{Origin: request.Origin, InstanceID: request.InstanceID, Environment: request.Environment, ProjectID: request.ProjectID}}
	result, err := establishLocalAuthoringSessionsWith(t.Context(), authority, request, io.Discard)
	require.NoError(t, err)
	require.Equal(t, request.TargetName, result.TargetName)
	require.Equal(t, "session-retained", result.SessionID)
	require.Zero(t, authority.loginCalls)
}

func TestEstablishLocalAuthoringSessionsRejectsConflictingProfile(t *testing.T) {
	request := localruntime.SessionRequest{TargetName: "local-checkout", Origin: "http://127.0.0.1:54321", InstanceID: "instance-local", Environment: "dev", ProjectID: "lvproject_test"}
	authority := &fakeLocalSessionAuthority{profile: cliapi.TargetProfile{Origin: "https://production.example", InstanceID: "production", Environment: "prod", ProjectID: "production"}}
	_, err := establishLocalAuthoringSessionsWith(t.Context(), authority, request, io.Discard)
	require.ErrorContains(t, err, "different runtime")
	require.Zero(t, authority.loginCalls)
}

func TestEstablishLocalAuthoringSessionsRebindsVerifiedLoopbackPortBeforeReuse(t *testing.T) {
	request := localruntime.SessionRequest{TargetName: "local-checkout", Origin: "http://127.0.0.1:54321", InstanceID: "instance-local", Environment: "dev", ProjectID: "lvproject_test"}
	authority := &fakeLocalSessionAuthority{profile: cliapi.TargetProfile{Origin: "http://127.0.0.1:8080", InstanceID: request.InstanceID, Environment: request.Environment, ProjectID: request.ProjectID}}
	result, err := establishLocalAuthoringSessionsWith(t.Context(), authority, request, io.Discard)
	require.NoError(t, err)
	require.Equal(t, request.TargetName, result.TargetName)
	require.Equal(t, request.Origin, authority.reboundOrigin)
	require.Zero(t, authority.loginCalls)
}
