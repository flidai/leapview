package composectl

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type nativeNetworkContainerFixture struct {
	inspectOutput []byte
	inspectErr    error
	inspectArgs   []string
}

func (container *nativeNetworkContainerFixture) Name() string { return "qualification-app" }

func (*nativeNetworkContainerFixture) Exec(context.Context, io.Reader, ...string) ([]byte, error) {
	return nil, nil
}

func (*nativeNetworkContainerFixture) CopyTo(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (*nativeNetworkContainerFixture) Restart(context.Context) ([]byte, error) { return nil, nil }

func (*nativeNetworkContainerFixture) Kill(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (*nativeNetworkContainerFixture) Start(context.Context) ([]byte, error) { return nil, nil }

func (container *nativeNetworkContainerFixture) Inspect(_ context.Context, format string) ([]byte, error) {
	container.inspectArgs = append(container.inspectArgs, format)
	return container.inspectOutput, container.inspectErr
}

func (*nativeNetworkContainerFixture) Logs(context.Context, int) ([]byte, error) { return nil, nil }

func (*nativeNetworkContainerFixture) Remove(context.Context) ([]byte, error) { return nil, nil }

type nativeNetworkRuntimeFixture struct {
	container   *nativeNetworkContainerFixture
	existingIDs []string
}

func (*nativeNetworkRuntimeFixture) Start(context.Context, qualificationContainerRequest) (qualificationContainer, error) {
	return nil, errors.New("unexpected docker run")
}

func (runtime *nativeNetworkRuntimeFixture) Existing(name string) qualificationContainer {
	runtime.existingIDs = append(runtime.existingIDs, name)
	return runtime.container
}

func newNativeNetworkController(
	t *testing.T,
	project string,
	appEnvironment string,
	executor qualificationCommandExecutor,
	runtime qualificationContainerRuntime,
) *Controller {
	t.Helper()
	root := t.TempDir()
	deployment := "COMPOSE_PROJECT_NAME=" + project + "\nCOMPOSE_HTTPS=0\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(deployment), 0o600))
	if appEnvironment != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte(appEnvironment), 0o600))
	}
	controller, err := New(Options{
		Root:                    root,
		DockerBin:               "docker-probe",
		qualificationExecutor:   executor,
		qualificationContainers: runtime,
	})
	require.NoError(t, err)
	return controller
}

func TestPrepareQualificationNativePostgresNetworkUsesComposeLifecycle(t *testing.T) {
	project := "leapview-qualification"
	network := project + "_default"
	container := &nativeNetworkContainerFixture{
		inspectOutput: []byte(`{"` + network + `":{}}`),
	}
	runtime := &nativeNetworkRuntimeFixture{container: container}
	var requests [][]string
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		arguments := append([]string(nil), request.Arguments...)
		requests = append(requests, arguments)
		switch {
		case strings.HasSuffix(strings.Join(arguments, " "), "create --no-build leapview"):
			return []byte("created\n"), nil
		case strings.HasSuffix(strings.Join(arguments, " "), "ps --quiet leapview"):
			return []byte("app-id\n"), nil
		case strings.HasSuffix(strings.Join(arguments, " "), "rm --force --stop leapview"):
			return []byte("removed\n"), nil
		default:
			return nil, errors.New("unexpected command: " + strings.Join(arguments, " "))
		}
	})
	controller := newNativeNetworkController(t, project, "LEAPVIEW_IMAGE=seed\n", executor, runtime)

	got, err := controller.prepareQualificationNativePostgresNetwork(t.Context())
	require.NoError(t, err)
	require.Equal(t, network, got)
	require.Equal(t, [][]string{
		{"compose", "--project-directory", controller.root, "--env-file", filepath.Join(controller.root, deploymentEnvName), "--file", filepath.Join(controller.root, "compose.yaml"), "create", "--no-build", "leapview"},
		{"compose", "--project-directory", controller.root, "--env-file", filepath.Join(controller.root, deploymentEnvName), "--file", filepath.Join(controller.root, "compose.yaml"), "ps", "--quiet", "leapview"},
		{"compose", "--project-directory", controller.root, "--env-file", filepath.Join(controller.root, deploymentEnvName), "--file", filepath.Join(controller.root, "compose.yaml"), "rm", "--force", "--stop", "leapview"},
	}, requests)
	require.Equal(t, []string{"app-id"}, runtime.existingIDs)
	require.Equal(t, []string{qualificationComposeNetworkInspectFormat}, container.inspectArgs)
	for _, arguments := range requests {
		require.NotContains(t, strings.Join(arguments, " "), "network create")
	}
}

func TestPrepareQualificationNativePostgresNetworkRequiresFreshInputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		project string
		appEnv  string
		empty   bool
	}{
		{name: "missing app environment", project: "project"},
		{name: "empty app environment", project: "project", empty: true},
		{name: "missing project", project: ""},
		{name: "uppercase project", project: "Project"},
		{name: "noncanonical project", project: "project.name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			executor := qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
				requests++
				return nil, nil
			})
			controller := newNativeNetworkController(t, test.project, test.appEnv, executor, &nativeNetworkRuntimeFixture{})
			if test.empty {
				require.NoError(t, os.WriteFile(filepath.Join(controller.root, appEnvName), nil, 0o600))
			}
			_, err := controller.prepareQualificationNativePostgresNetwork(t.Context())
			require.Error(t, err)
			require.Zero(t, requests)
		})
	}
}

func TestPrepareQualificationNativePostgresNetworkCleansUpRejectedNetworks(t *testing.T) {
	for _, test := range []struct {
		name          string
		inspectOutput string
		createErr     error
		cleanupErr    error
		wantOriginal  string
		wantCleanup   bool
	}{
		{name: "wrong network", inspectOutput: `{"other_default":{}}`, wantCleanup: true},
		{name: "multiple networks", inspectOutput: `{"project_default":{},"other":{}}`, wantCleanup: true},
		{name: "create failure", createErr: errors.New("create failed"), wantOriginal: "create failed", wantCleanup: true},
		{name: "cleanup failure", inspectOutput: `{"project_default":{}}`, cleanupErr: errors.New("cleanup failed"), wantOriginal: "cleanup failed", wantCleanup: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := &nativeNetworkContainerFixture{inspectOutput: []byte(test.inspectOutput)}
			runtime := &nativeNetworkRuntimeFixture{container: container}
			var requests [][]string
			executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
				requests = append(requests, append([]string(nil), request.Arguments...))
				joined := strings.Join(request.Arguments, " ")
				switch {
				case strings.HasSuffix(joined, "create --no-build leapview") && test.createErr != nil:
					return nil, test.createErr
				case strings.HasSuffix(joined, "create --no-build leapview"):
					return nil, nil
				case strings.HasSuffix(joined, "ps --quiet leapview"):
					return []byte("app-id\n"), nil
				case strings.HasSuffix(joined, "rm --force --stop leapview"):
					return nil, test.cleanupErr
				default:
					return nil, errors.New("unexpected command")
				}
			})
			controller := newNativeNetworkController(t, "project", "LEAPVIEW_IMAGE=seed\n", executor, runtime)
			_, err := controller.prepareQualificationNativePostgresNetwork(t.Context())
			require.Error(t, err)
			if test.wantOriginal != "" {
				require.ErrorContains(t, err, test.wantOriginal)
			}
			require.Equal(t, test.wantCleanup, len(requests) > 0 && strings.HasSuffix(strings.Join(requests[len(requests)-1], " "), "rm --force --stop leapview"))
		})
	}
}
