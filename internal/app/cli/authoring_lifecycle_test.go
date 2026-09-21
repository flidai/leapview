package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type fakeLocalRuntimeLifecycle struct {
	status     localruntime.LifecycleStatus
	resetPlan  localruntime.ResetPlan
	stopCalls  int
	resetCalls []string
}

func (*fakeLocalRuntimeLifecycle) Run(context.Context, bool) error { return nil }
func (runtime *fakeLocalRuntimeLifecycle) Status(context.Context) (localruntime.LifecycleStatus, error) {
	return runtime.status, nil
}
func (*fakeLocalRuntimeLifecycle) Logs(context.Context, int) ([]byte, error) {
	return []byte("ready\n"), nil
}
func (runtime *fakeLocalRuntimeLifecycle) Stop(context.Context) error {
	runtime.stopCalls++
	return nil
}
func (runtime *fakeLocalRuntimeLifecycle) PlanReset(context.Context) (localruntime.ResetPlan, error) {
	return runtime.resetPlan, nil
}
func (runtime *fakeLocalRuntimeLifecycle) Reset(_ context.Context, confirmation string) error {
	runtime.resetCalls = append(runtime.resetCalls, confirmation)
	return nil
}

func TestDevLifecycleCommandSurfaceMatchesAcceptedContract(t *testing.T) {
	command := NewCommand(t.Context())
	for _, test := range []struct {
		name, effect, confirmation string
		flags                      []string
	}{
		{name: "status", effect: "read", confirmation: "never", flags: []string{"format", "docker-context", "docker-host"}},
		{name: "logs", effect: "read", confirmation: "never", flags: []string{"tail", "docker-context", "docker-host"}},
		{name: "stop", effect: "destructive", confirmation: "conditional", flags: []string{"docker-context", "docker-host"}},
		{name: "reset", effect: "destructive", confirmation: "required", flags: []string{"confirm", "docker-context", "docker-host"}},
	} {
		child, remaining, err := command.Find([]string{"dev", test.name})
		require.NoError(t, err)
		require.Empty(t, remaining)
		require.Equal(t, test.effect, child.Annotations[documentationEffectAnnotation])
		require.Equal(t, test.confirmation, child.Annotations[documentationConfirmationAnnotation])
		for _, flag := range test.flags {
			require.NotNilf(t, child.Flags().Lookup(flag), "dev %s missing --%s", test.name, flag)
		}
	}
}

func TestDevStatusRejectsInvalidFormatBeforeDockerResolution(t *testing.T) {
	parent := &cobra.Command{Use: "dev"}
	resolveCalls := 0
	addLocalDevLifecycleCommands(t.Context(), parent, func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
		resolveCalls++
		return localdocker.Endpoint{}, nil
	}, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) {
		return &fakeLocalRuntimeLifecycle{}, nil
	}, nil)
	parent.SetArgs([]string{"status", "--format", "xml"})
	require.ErrorContains(t, parent.Execute(), "must be text or json")
	require.Zero(t, resolveCalls)
}

func TestDevLogsRejectsInvalidTailBeforeDockerResolution(t *testing.T) {
	parent := &cobra.Command{Use: "dev"}
	resolveCalls := 0
	addLocalDevLifecycleCommands(t.Context(), parent, func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
		resolveCalls++
		return localdocker.Endpoint{}, nil
	}, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) {
		return &fakeLocalRuntimeLifecycle{}, nil
	}, nil)
	parent.SetArgs([]string{"logs", "--tail", "0"})
	require.ErrorContains(t, parent.Execute(), "between 1 and 10000")
	require.Zero(t, resolveCalls)
}

func TestDevStatusReportsExactCheckoutAndAttachments(t *testing.T) {
	parent := &cobra.Command{Use: "dev"}
	runtime := &fakeLocalRuntimeLifecycle{status: localruntime.LifecycleStatus{
		Exists: true, RuntimeStatus: "applied", Phase: "ready", CheckoutRoot: "/checkout", CheckoutID: "sha256:checkout",
		ComposeProject: "leapview-checkout", OwnerID: "lvowner_checkout", URL: "http://127.0.0.1:8080", Services: map[string]string{"postgres": "running"},
		Attachments: []localruntime.AttachmentStatus{{ID: "attachment-one", PID: 42, HeartbeatAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}},
	}}
	addLocalDevLifecycleCommands(t.Context(), parent, localLifecycleTestResolver, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) { return runtime, nil }, nil)
	var output strings.Builder
	parent.SetOut(&output)
	parent.SetArgs([]string{"status"})
	require.NoError(t, parent.Execute())
	require.Contains(t, output.String(), "Checkout: /checkout")
	require.Contains(t, output.String(), "Owner: lvowner_checkout")
	require.Contains(t, output.String(), "attachment-one")
	require.Contains(t, output.String(), "Service postgres: running")
}

func TestDevStatusReportsRedactedDurableProfileApplication(t *testing.T) {
	parent := &cobra.Command{Use: "dev"}
	runtime := &fakeLocalRuntimeLifecycle{status: localruntime.LifecycleStatus{
		Exists: true, RuntimeStatus: "applied", Phase: "ready", CheckoutRoot: "/checkout", CheckoutID: "checkout-one",
		TargetName: "local-checkout", TargetID: "target-local", ProjectID: "project:one", Attachments: []localruntime.AttachmentStatus{},
	}}
	reader := func(_ context.Context, status localruntime.LifecycleStatus) (*localruntime.DevelopmentProfileStatus, error) {
		require.Equal(t, "local-checkout", status.TargetName)
		return &localruntime.DevelopmentProfileStatus{
			ApplicationID: "profile-one", Status: "incomplete", ProfileName: "local",
			RequiredConnections: 3, AppliedConnections: 1,
			IncompleteConnections: []string{"connection:inventory", "connection:commerce"},
			UpdatedAt:             "2026-09-15T12:00:00Z",
		}, nil
	}
	addLocalDevLifecycleCommands(t.Context(), parent, localLifecycleTestResolver, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) { return runtime, nil }, reader)
	var output strings.Builder
	parent.SetOut(&output)
	parent.SetArgs([]string{"status"})
	require.NoError(t, parent.Execute())
	require.Contains(t, output.String(), "profile-one (incomplete, profile local")
	require.Contains(t, output.String(), "1/3 applied")
	require.Contains(t, output.String(), "Last completed profile application: none")
	require.Contains(t, output.String(), "connection:inventory")
	require.NotContains(t, output.String(), "credential")
}

func TestDevelopmentProfileStatusUsesExactRetainedLocalLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer short-lived", request.Header.Get("Authorization"))
		require.Contains(t, request.URL.Path, "/projects/project:one/targets/target-local/development-profile-application")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"applicationId":"profile-one","status":"incomplete","profileName":"local","requiredConnectionCount":2,"appliedConnectionCount":1,"incompleteConnections":["connection:b"],"sourceDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","graphDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","profileDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":4,"updatedAt":"2026-09-15T12:00:00Z"}`))
	}))
	defer server.Close()
	resolver := &fakeAuthoringResolver{profile: cliapi.TargetProfile{Origin: server.URL, InstanceID: "target-local", ProjectID: "project:one"}}
	status, err := readDevelopmentProfileStatusWith(t.Context(), localruntime.LifecycleStatus{
		TargetName: "local-checkout", TargetID: "target-local", ProjectID: "project:one", URL: server.URL,
	}, resolver, server.Client())
	require.NoError(t, err)
	require.Equal(t, "local-checkout", resolver.name)
	require.Equal(t, "profile-one", status.ApplicationID)
	require.Equal(t, []string{"connection:b"}, status.IncompleteConnections)
}

func TestDevelopmentProfileStatusRejectsRetainedLoginIdentityMismatchBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	resolver := &fakeAuthoringResolver{profile: cliapi.TargetProfile{Origin: server.URL, InstanceID: "other-target", ProjectID: "project:one"}}
	_, err := readDevelopmentProfileStatusWith(t.Context(), localruntime.LifecycleStatus{
		TargetName: "local-checkout", TargetID: "target-local", ProjectID: "project:one", URL: server.URL,
	}, resolver, server.Client())
	require.ErrorContains(t, err, "identity disagrees")
	require.Zero(t, requests)
}

func TestDevResetPlansThenRequiresExactConfirmation(t *testing.T) {
	parent := &cobra.Command{Use: "dev"}
	confirmation := "sha256:" + strings.Repeat("a", 64)
	runtime := &fakeLocalRuntimeLifecycle{resetPlan: localruntime.ResetPlan{
		CheckoutRoot: "/checkout", CheckoutID: "sha256:checkout", Confirmation: confirmation,
		Resources: []localruntime.OwnedResource{{Kind: "volume", ID: "volume-one"}},
	}}
	addLocalDevLifecycleCommands(t.Context(), parent, localLifecycleTestResolver, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) { return runtime, nil }, nil)
	var output strings.Builder
	parent.SetOut(&output)
	parent.SetArgs([]string{"reset"})
	err := parent.Execute()
	require.ErrorContains(t, err, "reset requires --confirm")
	require.Contains(t, output.String(), "volume volume-one")
	require.Empty(t, runtime.resetCalls)

	parent.SetArgs([]string{"reset", "--confirm", confirmation})
	require.NoError(t, parent.Execute())
	require.Equal(t, []string{confirmation}, runtime.resetCalls)
}

func localLifecycleTestResolver(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
	return localdocker.Endpoint{}, nil
}
