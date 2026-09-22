package composectl

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationMultiNodeURLUsesReadOnlyMountedCertificate(t *testing.T) {
	raw := "postgres://runtime:password@postgres/leapview_control?sslmode=verify-full&sslrootcert=%2Fold%2Fca.pem"
	value, err := qualificationMultiNodeURL(raw)
	require.NoError(t, err)
	require.Equal(t, qualificationMultiNodeRootCertificate, mustQualificationURLQuery(t, value, "sslrootcert"))
	require.Equal(t, "verify-full", mustQualificationURLQuery(t, value, "sslmode"))
	require.NotContains(t, value, "/old/ca.pem")
}

func TestQualificationMultiNodeEnvironmentRewritesPostgresURLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), appEnvName)
	require.NoError(t, os.WriteFile(path, []byte(
		"LEAPVIEW_ADDR=:8080\n"+
			"LEAPVIEW_HOME=/var/lib/leapview/home\n"+
			"LEAPVIEW_MANAGED_DATA_BACKEND=local\n"+
			"LEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data\n"+
			"LEAPVIEW_POSTGRES_CONTROL_URL=postgres://runtime:secret@postgres/leapview_control?sslmode=verify-full\n"+
			"LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://runtime:secret@postgres/leapview_ducklake?sslmode=verify-full\n",
	), 0o600))

	values, err := qualificationMultiNodeEnvironment(path)
	require.NoError(t, err)
	require.Equal(t, ":8080", values["LEAPVIEW_ADDR"])
	require.Equal(t, "/var/lib/leapview/home", values["LEAPVIEW_HOME"])
	require.Equal(t, "/var/lib/leapview/home/managed-data", values["LEAPVIEW_MANAGED_DATA_DIR"])
	for _, key := range []string{"LEAPVIEW_POSTGRES_CONTROL_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_URL"} {
		require.Equal(t, qualificationMultiNodeRootCertificate, mustQualificationURLQuery(t, values[key], "sslrootcert"))
	}
}

func mustQualificationURLQuery(t *testing.T, raw, key string) string {
	t.Helper()
	value, err := qualificationURLQuery(raw, key)
	require.NoError(t, err)
	return value
}

func qualificationURLQuery(raw, key string) (string, error) {
	value, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return value.Query().Get(key), nil
}

func TestQualificationMultiNodeProcessExercisesLossAndRollingRestart(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(
		"COMPOSE_PROJECT_NAME=multi-node-qualification\nCOMPOSE_HTTPS=0\n",
	), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte(
		"LEAPVIEW_ADDR=:8080\nLEAPVIEW_HOME=/var/lib/leapview/home\nLEAPVIEW_PUBLIC_URL=https://localhost\n"+
			"LEAPVIEW_MANAGED_DATA_BACKEND=local\nLEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data\n"+
			"LEAPVIEW_POSTGRES_CONTROL_URL=postgres://runtime:secret@postgres/leapview_control?sslmode=verify-full\n",
	), 0o600))
	secretDir := filepath.Join(root, "secrets")
	require.NoError(t, os.Mkdir(secretDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "ca.pem"), []byte("certificate"), 0o644))

	primary := &multiNodeContainerFixture{name: "primary", identity: `{"id":"instance_abc","canonicalOrigin":"https://localhost","environment":"evaluation"}`}
	secondary := &multiNodeContainerFixture{name: "secondary", identity: `{"id":"instance_abc","canonicalOrigin":"https://localhost","environment":"evaluation"}`}
	runtime := &multiNodeRuntimeFixture{secondary: secondary}
	topology := &qualificationNativePostgresTopology{
		Container: &multiNodeContainerFixture{name: "postgres", execOutput: []byte("generation_abc\n")},
		secretDir: secretDir,
	}
	controller, err := New(Options{
		Root: root, DockerBin: "docker",
		qualificationContainers: runtime,
		qualificationExecutor: qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
			arguments := strings.Join(request.Arguments, " ")
			if strings.Contains(arguments, "run --rm --entrypoint id") {
				return []byte("999\n"), nil
			}
			if strings.Contains(arguments, "update --restart=no") {
				return nil, nil
			}
			return nil, errors.New("unexpected qualification command")
		}),
	})
	require.NoError(t, err)

	report, err := controller.runQualificationMultiNode(t.Context(), qualificationMultiNodeOptions{
		Image: "leapview:test@sha256:" + strings.Repeat("a", 64), ComposeProject: "multi-node-qualification",
		ComposeNetwork: "multi-node-qualification_default", TargetID: "instance_abc", APIToken: "qualification-token",
		Topology: topology, Primary: primary,
	})
	require.NoError(t, err)
	require.Equal(t, qualificationMultiNodeReport{
		NodeCount: 2, GenerationID: "generation_abc", AbruptNodeLoss: true, Recovery: true, RollingRestart: true, DurableConvergence: true,
	}, report)
	require.NotEmpty(t, runtime.request.Volumes)
	require.True(t, runtime.request.ReadOnly)
	require.Equal(t, "/var/lib/leapview/home", runtime.request.Environment["LEAPVIEW_HOME"])
	require.Equal(t, "multi-node-qualification_leapview-state", runtime.request.Volumes[0].Source)
	require.Equal(t, "/var/lib/leapview/home/managed-data", runtime.request.Volumes[0].Target)
	require.Equal(t, "home/managed-data", runtime.request.Volumes[0].Subpath)
	require.Equal(t, "home/artifacts/object-store", runtime.request.Volumes[1].Subpath)
	require.Equal(t, qualificationMultiNodeRootCertificate, runtime.request.Volumes[2].Target)
	require.Contains(t, runtime.request.Tmpfs, "/var/lib/leapview/home:rw,exec,nosuid,nodev,mode=0700,uid=999,gid=999,size=512m")
	require.Contains(t, runtime.request.Tmpfs, "/var/lib/leapview/home/artifacts:rw,exec,nosuid,nodev,mode=0700,uid=999,gid=999,size=128m")
	require.Contains(t, strings.Join(primary.commands[1], " "), "LEAPVIEW_API_TOKEN=qualification-token")
	require.Contains(t, strings.Join(secondary.commands[1], " "), "LEAPVIEW_API_TOKEN=qualification-token")
	require.Equal(t, 1, secondary.removed)
	require.Equal(t, 1, primary.kills)
	require.GreaterOrEqual(t, primary.restarts, 1)
	require.GreaterOrEqual(t, secondary.restarts, 1)
}

type multiNodeRuntimeFixture struct {
	secondary *multiNodeContainerFixture
	request   qualificationContainerRequest
}

func (runtime *multiNodeRuntimeFixture) Start(_ context.Context, request qualificationContainerRequest) (qualificationContainer, error) {
	runtime.request = request
	return runtime.secondary, nil
}

func (runtime *multiNodeRuntimeFixture) Existing(name string) qualificationContainer {
	if runtime.secondary != nil && runtime.secondary.name == name {
		return runtime.secondary
	}
	return nil
}

type multiNodeContainerFixture struct {
	name       string
	identity   string
	execOutput []byte
	kills      int
	restarts   int
	removed    int
	status     string
	commands   [][]string
}

func (container *multiNodeContainerFixture) Name() string { return container.name }

func (container *multiNodeContainerFixture) Exec(_ context.Context, _ io.Reader, command ...string) ([]byte, error) {
	container.commands = append(container.commands, append([]string(nil), command...))
	if strings.Contains(strings.Join(command, " "), "getInstance") {
		return []byte(container.identity), nil
	}
	if strings.Contains(strings.Join(command, " "), "healthcheck") {
		return nil, nil
	}
	if container.execOutput != nil {
		return append([]byte(nil), container.execOutput...), nil
	}
	return nil, nil
}

func (container *multiNodeContainerFixture) CopyTo(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (container *multiNodeContainerFixture) Restart(context.Context) ([]byte, error) {
	container.restarts++
	container.status = "running"
	return nil, nil
}

func (container *multiNodeContainerFixture) Kill(context.Context, string) ([]byte, error) {
	container.kills++
	container.status = "exited"
	return nil, nil
}

func (container *multiNodeContainerFixture) Start(context.Context) ([]byte, error) {
	container.status = "running"
	return nil, nil
}

func (container *multiNodeContainerFixture) Inspect(_ context.Context, format string) ([]byte, error) {
	if format == "{{.State.Status}}" {
		if container.status == "" {
			return []byte("running"), nil
		}
		return []byte(container.status), nil
	}
	return nil, nil
}

func (*multiNodeContainerFixture) Logs(context.Context, int) ([]byte, error) { return nil, nil }

func (container *multiNodeContainerFixture) Remove(context.Context) ([]byte, error) {
	container.removed++
	return nil, nil
}
