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
		[]byte("COMPOSE_PROJECT_NAME=qualification-project\nCOMPOSE_HTTPS=0\nLEAPVIEW_IMAGE=file-image\n"),
		0o600,
	))
	const name = "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"
	const secret = "postgres://owner:secret@postgres/leapview_ducklake?sslmode=require"
	t.Setenv("COMPOSE_PROJECT_NAME", "unrelated-host-project")
	t.Setenv("COMPOSE_PROFILES", "unrelated-host-profile")
	t.Setenv("LEAPVIEW_IMAGE", "host-image")
	t.Setenv(name, "unrelated-host-secret")
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
	require.Contains(t, captured.Arguments, "qualification-project")
	require.Contains(t, captured.Arguments, name)
	processValues := environmentValues(strings.Join(captured.Environment, "\n"))
	require.Equal(t, secret, processValues[name])
	var operationEntries int
	for _, entry := range captured.Environment {
		if strings.HasPrefix(entry, name+"=") {
			operationEntries++
		}
	}
	require.Equal(t, 1, operationEntries)
	require.NotContains(t, processValues, "COMPOSE_PROJECT_NAME")
	require.NotContains(t, processValues, "COMPOSE_PROFILES")
	require.NotContains(t, processValues, "LEAPVIEW_IMAGE")
}

func TestQualificationComposeEnvironmentRejectsInvalidNameBeforeExecution(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("COMPOSE_PROJECT_NAME=qualification-project\nCOMPOSE_HTTPS=0\n"), 0o600))
	controller, err := New(Options{Root: root, DockerBin: "docker-probe"})
	require.NoError(t, err)
	_, err = controller.qualificationComposeEnvironment(t.Context(), root, map[string]string{"BAD=NAME": "secret"}, "config")
	require.ErrorContains(t, err, "environment name")
}

func TestCopyQualificationFileEnforcesRequestedFinalMode(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "nested", "destination")
	require.NoError(t, os.WriteFile(source, []byte("asset\n"), 0o600))
	require.NoError(t, copyQualificationFile(source, destination, 0o755))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}
