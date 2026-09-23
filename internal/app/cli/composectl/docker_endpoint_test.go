package composectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type pinnedEndpointStub struct {
	host      string
	verifyErr error
	verified  *int
}

func (endpoint pinnedEndpointStub) Host() string { return endpoint.host }

func (endpoint pinnedEndpointStub) Verify(context.Context) error {
	if endpoint.verified != nil {
		*endpoint.verified++
	}
	return endpoint.verifyErr
}

func (endpoint pinnedEndpointStub) DockerArguments(arguments ...string) []string {
	return append([]string{"--host", endpoint.host}, arguments...)
}

func (endpoint pinnedEndpointStub) Environment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		name, _, present := strings.Cut(entry, "=")
		if present && slices.Contains([]string{"DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"}, name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "DOCKER_HOST="+endpoint.host)
}

func TestPinnedDockerEndpointReachesEveryControllerProcess(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "remote")
	t.Setenv("DOCKER_HOST", "tcp://remote.example:2375")
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, deploymentEnvName),
		[]byte("COMPOSE_PROJECT_NAME=pinned-test\nCOMPOSE_HTTPS=0\n"),
		0o600,
	))
	var requests []qualificationCommandRequest
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		requests = append(requests, request)
		if slices.Contains(request.Arguments, "run") {
			return []byte("container-id\n"), nil
		}
		return []byte("ok\n"), nil
	})
	var verified int
	endpoint := pinnedEndpointStub{host: "unix:///run/user/1000/docker.sock", verified: &verified}
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", DockerEndpoint: endpoint,
		qualificationExecutor: executor,
	})
	require.NoError(t, err)

	_, err = controller.qualificationDocker(t.Context(), nil, "version")
	require.NoError(t, err)
	_, err = controller.qualificationCompose(t.Context(), root, "config")
	require.NoError(t, err)
	_, err = controller.qualificationContainers.Start(t.Context(), qualificationContainerRequest{
		Name: "pinned-container", Image: "example.invalid/image@sha256:test",
	})
	require.NoError(t, err)

	require.Len(t, requests, 3)
	require.Equal(t, 3, verified)
	for _, request := range requests {
		require.GreaterOrEqual(t, len(request.Arguments), 3)
		require.Equal(t, []string{"--host", endpoint.host}, request.Arguments[:2])
		values := environmentValues(strings.Join(request.Environment, "\n"))
		require.Equal(t, endpoint.host, values["DOCKER_HOST"])
		require.NotContains(t, values, "DOCKER_CONTEXT")
		require.NotEqual(t, "tcp://remote.example:2375", values["DOCKER_HOST"])
	}

	scoped, err := controller.scoped(t.TempDir(), os.Stdout)
	require.NoError(t, err)
	require.Equal(t, endpoint.host, scoped.dockerEndpoint.Host())
}

func TestChangedPinnedDaemonFailsBeforeControllerExecution(t *testing.T) {
	var requests int
	controller, err := New(Options{
		Root: t.TempDir(), DockerEndpoint: pinnedEndpointStub{
			host: "unix:///run/docker.sock", verifyErr: errors.New("daemon identity changed"),
		},
		qualificationExecutor: qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
			requests++
			return nil, nil
		}),
	})
	require.NoError(t, err)
	_, err = controller.qualificationDocker(t.Context(), nil, "pull", "example.invalid/image")
	require.ErrorContains(t, err, "daemon identity changed")
	require.Zero(t, requests)
}

func TestPinnedDockerEndpointMustHaveAHost(t *testing.T) {
	_, err := New(Options{Root: t.TempDir(), DockerEndpoint: pinnedEndpointStub{}})
	require.ErrorContains(t, err, "endpoint host")
}
