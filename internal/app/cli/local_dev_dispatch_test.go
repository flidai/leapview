package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestBareDevSelectsLocalBeforeAmbientTargetResolution(t *testing.T) {
	t.Setenv("LEAPVIEW_TARGET", "production")
	var remoteCalls, resolveCalls, localCalls int
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(&remoteCalls),
		func(_ context.Context, options localdocker.Options) (localdocker.Endpoint, error) {
			resolveCalls++
			require.Empty(t, options.ExplicitContext)
			require.Empty(t, options.ExplicitHost)
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error {
			localCalls++
			return nil
		},
	)
	command.SetArgs(nil)
	require.NoError(t, command.Execute())
	require.Equal(t, 0, remoteCalls)
	require.Equal(t, 1, resolveCalls)
	require.Equal(t, 1, localCalls)
}

func TestExplicitTargetUsesRemoteWithoutDockerResolution(t *testing.T) {
	var remoteCalls, resolveCalls, localCalls int
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(&remoteCalls),
		func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
			resolveCalls++
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error {
			localCalls++
			return nil
		},
	)
	command.SetArgs([]string{"--target", "staging"})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, remoteCalls)
	require.Zero(t, resolveCalls)
	require.Zero(t, localCalls)
}

func TestDevRejectsConflictingLocalAndRemoteSelectionBeforeEitherPath(t *testing.T) {
	var remoteCalls, resolveCalls int
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(&remoteCalls),
		func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
			resolveCalls++
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error { return nil },
	)
	command.SetArgs([]string{"--target", "staging", "--docker-context", "desktop-linux"})
	err := command.Execute()
	require.ErrorContains(t, err, "cannot be combined")
	require.Zero(t, remoteCalls)
	require.Zero(t, resolveCalls)
}

func TestBareDevRejectsRemoteCredentialsBeforeDockerResolution(t *testing.T) {
	var remoteCalls, resolveCalls int
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(&remoteCalls),
		func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
			resolveCalls++
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error { return nil },
	)
	command.SetArgs([]string{"--token", "secret"})
	err := command.Execute()
	require.ErrorContains(t, err, "requires an explicit remote --target")
	require.NotContains(t, err.Error(), "secret")
	require.Zero(t, remoteCalls)
	require.Zero(t, resolveCalls)
}

func TestBareDevPassesExplicitDockerSelectionToResolver(t *testing.T) {
	var got localdocker.Options
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(new(int)),
		func(_ context.Context, options localdocker.Options) (localdocker.Endpoint, error) {
			got = options
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error { return nil },
	)
	command.SetArgs([]string{"--docker-host", "unix:///var/run/docker.sock"})
	require.NoError(t, command.Execute())
	require.Equal(t, "unix:///var/run/docker.sock", got.ExplicitHost)
	require.Empty(t, got.ExplicitContext)
}

func remoteDevCommandForDispatchTest(calls *int) *cobra.Command {
	command := &cobra.Command{
		Use:  "dev [source-root]",
		Args: cobra.MaximumNArgs(1),
		RunE: func(*cobra.Command, []string) error {
			*calls++
			return nil
		},
	}
	command.Flags().String("target", "", "target")
	command.Flags().String("token", "", "token")
	command.Flags().String("project-id", "", "project")
	command.Flags().String("source-root", "dashboards", "source")
	command.Flags().Bool("bootstrap", false, "bootstrap")
	return command
}

func TestExplicitEmptyRemoteTargetFailsClosed(t *testing.T) {
	command := dispatchLocalDevCommand(
		t.Context(), remoteDevCommandForDispatchTest(new(int)),
		func(context.Context, localdocker.Options) (localdocker.Endpoint, error) {
			t.Fatal("Docker resolution must not run")
			return localdocker.Endpoint{}, nil
		},
		func(context.Context, localdocker.Endpoint, *cobra.Command, []string) error { return nil },
	)
	command.SetArgs([]string{"--target", strings.Repeat(" ", 2)})
	require.ErrorContains(t, command.Execute(), "must not be empty")
}
