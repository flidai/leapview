package composectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

type testcontainersQualificationRuntime struct {
	mu         sync.RWMutex
	containers map[string]testcontainers.Container
}

func newTestcontainersQualificationRuntime() *testcontainersQualificationRuntime {
	return &testcontainersQualificationRuntime{
		containers: map[string]testcontainers.Container{},
	}
}

func (runtime *testcontainersQualificationRuntime) Start(
	ctx context.Context,
	request qualificationContainerRequest,
) (qualificationContainer, error) {
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Image) == "" {
		return nil, fmt.Errorf("qualification container name and image are required")
	}
	binds := make([]string, 0, len(request.Volumes))
	for _, volume := range request.Volumes {
		value := volume.Source + ":" + volume.Target
		if volume.ReadOnly {
			value += ":ro"
		}
		binds = append(binds, value)
	}
	containerRequest := testcontainers.ContainerRequest{
		Name: request.Name, Image: request.Image,
		Env: request.Environment, Entrypoint: request.Entrypoint, Cmd: request.Command,
		ConfigModifier: func(config *dockercontainer.Config) {
			if request.NoHealth {
				config.Healthcheck = &dockercontainer.HealthConfig{Test: []string{"NONE"}}
			}
		},
		HostConfigModifier: func(config *dockercontainer.HostConfig) {
			config.Binds = append(config.Binds, binds...)
			if request.NetworkMode != "" {
				config.NetworkMode = dockercontainer.NetworkMode(request.NetworkMode)
			}
		},
	}
	if readyLog := strings.TrimSpace(request.ReadyLog); readyLog != "" {
		containerRequest.WaitingFor = wait.ForLog(readyLog)
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		Started: true, ContainerRequest: containerRequest,
	})
	if err != nil {
		return nil, errors.Join(err, testcontainers.TerminateContainer(container))
	}
	runtime.mu.Lock()
	runtime.containers[request.Name] = container
	runtime.mu.Unlock()
	return &testcontainersQualificationContainer{
		name: request.Name, container: container, runtime: runtime,
	}, nil
}

func (runtime *testcontainersQualificationRuntime) Existing(name string) qualificationContainer {
	runtime.mu.RLock()
	container := runtime.containers[name]
	runtime.mu.RUnlock()
	return &testcontainersQualificationContainer{
		name: name, container: container, runtime: runtime,
	}
}

type testcontainersQualificationContainer struct {
	name      string
	container testcontainers.Container
	runtime   *testcontainersQualificationRuntime
}

func (container *testcontainersQualificationContainer) Name() string {
	return container.name
}

func (container *testcontainersQualificationContainer) requireContainer() error {
	if container.container == nil {
		return fmt.Errorf("qualification container %q is not managed by Testcontainers", container.name)
	}
	return nil
}

func (container *testcontainersQualificationContainer) Exec(
	ctx context.Context,
	stdin io.Reader,
	command ...string,
) ([]byte, error) {
	return container.ExecEnvironment(ctx, stdin, nil, command...)
}

func (container *testcontainersQualificationContainer) ExecEnvironment(
	ctx context.Context,
	stdin io.Reader,
	environment map[string]string,
	command ...string,
) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	if stdin != nil {
		return nil, fmt.Errorf("Testcontainers qualification exec does not support stdin")
	}
	values := make([]string, 0, len(environment))
	for name, value := range environment {
		values = append(values, name+"="+value)
	}
	sort.Strings(values)
	options := []tcexec.ProcessOption{tcexec.Multiplexed()}
	if len(values) > 0 {
		options = append(options, tcexec.WithEnv(values))
	}
	exitCode, output, err := container.container.Exec(ctx, command, options...)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(output)
	if readErr != nil {
		return nil, readErr
	}
	if exitCode != 0 {
		return content, fmt.Errorf("qualification container exec exited with code %d", exitCode)
	}
	return content, nil
}

func (container *testcontainersQualificationContainer) CopyTo(
	ctx context.Context,
	source string,
	target string,
) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if err := container.container.CopyFileToContainer(ctx, source, target, int64(info.Mode().Perm())); err != nil {
		return nil, err
	}
	return nil, nil
}

func (container *testcontainersQualificationContainer) Restart(ctx context.Context) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	timeout := time.Duration(0)
	if err := container.container.Stop(ctx, &timeout); err != nil {
		return nil, err
	}
	return nil, container.container.Start(ctx)
}

func (container *testcontainersQualificationContainer) Kill(ctx context.Context, signal string) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	if signal != "" && signal != "KILL" {
		return nil, fmt.Errorf("Testcontainers qualification runtime supports only KILL")
	}
	timeout := time.Duration(0)
	return nil, container.container.Stop(ctx, &timeout)
}

func (container *testcontainersQualificationContainer) Start(ctx context.Context) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	return nil, container.container.Start(ctx)
}

func (container *testcontainersQualificationContainer) Inspect(ctx context.Context, format string) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	inspection, err := container.container.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	switch format {
	case "{{.State.Status}}":
		return []byte(inspection.State.Status), nil
	case "{{.State.Health.Status}}":
		if inspection.State.Health == nil {
			return nil, nil
		}
		return []byte(inspection.State.Health.Status), nil
	case "{{json .Config.Cmd}}":
		return json.Marshal(inspection.Config.Cmd)
	default:
		return nil, fmt.Errorf("unsupported Testcontainers inspect format %q", format)
	}
}

func (container *testcontainersQualificationContainer) Logs(ctx context.Context, tail int) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	logs, err := container.container.Logs(ctx)
	if err != nil {
		return nil, err
	}
	defer logs.Close()
	content, err := io.ReadAll(logs)
	if err != nil || tail <= 0 {
		return content, err
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return bytes.Join(lines, []byte("\n")), nil
}

func (container *testcontainersQualificationContainer) Remove(context.Context) ([]byte, error) {
	if err := container.requireContainer(); err != nil {
		return nil, err
	}
	err := testcontainers.TerminateContainer(container.container)
	container.runtime.mu.Lock()
	delete(container.runtime.containers, container.name)
	container.runtime.mu.Unlock()
	container.container = nil
	return nil, err
}

func TestQualificationContainerRuntimeLifecycle(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_CONTAINERS") != "1" {
		t.Skip("set LEAPVIEW_TEST_CONTAINERS=1 to run container-backed qualification runtime tests")
	}
	runtimes := map[string]qualificationContainerRuntime{
		"docker-cli":     newDockerCLIQualificationRuntime(t.TempDir(), "docker", nil),
		"testcontainers": newTestcontainersQualificationRuntime(),
	}
	for name, runtime := range runtimes {
		t.Run(name, func(t *testing.T) {
			exerciseQualificationContainerRuntime(t, runtime)
		})
	}
}

func exerciseQualificationContainerRuntime(
	t *testing.T,
	runtime qualificationContainerRuntime,
) {
	t.Helper()
	name := "leapview-qualification-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	container, err := runtime.Start(t.Context(), qualificationContainerRequest{
		Name: name, Image: "alpine:3.22",
		Command: []string{"sh", "-c", "echo ready; sleep 60"}, ReadyLog: "ready",
	})
	require.NoError(t, err)
	removed := false
	t.Cleanup(func() {
		if removed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = container.Remove(cleanupCtx)
	})
	hostFile := t.TempDir() + "/runtime.txt"
	if err := os.WriteFile(hostFile, []byte("copied-ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := container.CopyTo(t.Context(), hostFile, "/tmp/runtime.txt"); err != nil {
		t.Fatal(err)
	}
	output, err := container.Exec(t.Context(), nil, "sh", "-c", "printf lifecycle-ok")
	require.NoError(t, err)
	if string(output) != "lifecycle-ok" {
		t.Fatalf("exec output=%q", output)
	}
	state, err := container.Inspect(t.Context(), "{{.State.Status}}")
	require.NoError(t, err)
	if strings.TrimSpace(string(state)) != "running" {
		t.Fatalf("state=%q", state)
	}
	logs, err := container.Logs(t.Context(), 20)
	require.NoError(t, err)
	if !bytes.Contains(logs, []byte("ready")) {
		t.Fatalf("logs=%q", logs)
	}
	copied, err := container.Exec(t.Context(), nil, "cat", "/tmp/runtime.txt")
	require.NoError(t, err)
	if string(copied) != "copied-ok" {
		t.Fatalf("copied content=%q", copied)
	}
	if _, err := container.Kill(t.Context(), "KILL"); err != nil {
		t.Fatal(err)
	}
	if _, err := container.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := container.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := container.Remove(t.Context()); err != nil {
		t.Fatal(err)
	}
	removed = true
}

var _ qualificationContainerRuntime = (*testcontainersQualificationRuntime)(nil)
