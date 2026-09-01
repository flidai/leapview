package composectl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationComposeEnvironmentKeepsOperationSecretOutOfArguments(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, deploymentEnvName),
		[]byte("COMPOSE_HTTPS=0\n"),
		0o600,
	))
	const name = "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"
	const secret = "postgres://owner:secret@postgres/leapview_ducklake?sslmode=require"
	var captured qualificationCommandRequest
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe",
		qualificationExecutor: qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
			captured = request
			return []byte("ok"), nil
		}),
	})
	require.NoError(t, err)

	output, err := controller.qualificationComposeEnvironment(
		t.Context(), root, map[string]string{name: secret},
		"run", "--rm", "--env", name, "leapview", "admin", "delivery", "pool", "bootstrap",
	)
	require.NoError(t, err)
	require.Equal(t, []byte("ok"), output)
	require.NotContains(t, strings.Join(captured.Arguments, " "), secret)
	require.Contains(t, captured.Arguments, name)
	require.Equal(t, secret, environmentValues(strings.Join(captured.Environment, "\n"))[name])
}

func TestQualificationComposeEnvironmentRejectsInvalidNameBeforeExecution(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("COMPOSE_HTTPS=0\n"), 0o600))
	controller, err := New(Options{Root: root, DockerBin: "docker-probe"})
	require.NoError(t, err)
	_, err = controller.qualificationComposeEnvironment(t.Context(), root, map[string]string{"BAD=NAME": "secret"}, "config")
	require.ErrorContains(t, err, "environment name")
}
