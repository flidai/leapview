package composectl

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerCLIRuntimeStartsContainerWithDeterministicArguments(t *testing.T) {
	executor := &recordingQualificationExecutor{output: []byte("container-id\n")}
	runtime := newDockerCLIQualificationRuntime(t.TempDir(), "docker-probe", executor)

	container, err := runtime.Start(t.Context(), qualificationContainerRequest{
		Name:        "qualification-browser",
		Image:       "browser:stable",
		NetworkMode: "host",
		Volumes: []qualificationContainerVolume{
			{Source: "/host/read-only", Target: "/qualification", ReadOnly: true},
			{Source: "/host/evidence", Target: "/evidence"},
		},
		Tmpfs: []string{"/var/lib/postgresql:rw,exec,nosuid,nodev,size=512m"},
		Environment: map[string]string{
			"QUALIFICATION_URL":        "https://localhost",
			"QUALIFICATION_PROJECT_ID": "evaluation",
		},
		Command: []string{"sleep", "infinity"},
	})
	require.NoError(t, err)
	if container.Name() != "qualification-browser" {
		t.Fatalf("container name=%q", container.Name())
	}
	want := []string{
		"run", "--detach", "--name", "qualification-browser",
		"--network", "host",
		"--volume", "/host/read-only:/qualification:ro",
		"--volume", "/host/evidence:/evidence",
		"--tmpfs", "/var/lib/postgresql:rw,exec,nosuid,nodev,size=512m",
		"--env", "QUALIFICATION_PROJECT_ID=evaluation",
		"--env", "QUALIFICATION_URL=https://localhost",
		"browser:stable", "sleep", "infinity",
	}
	if len(executor.requests) != 1 || !slices.Equal(executor.requests[0].Arguments, want) {
		t.Fatalf("arguments=%v", executor.requests)
	}
}

func TestQualificationNamedContainerHandlePreservesNameAfterRemovalFailure(t *testing.T) {
	removeErr := errors.New("docker removal failed")
	runtime := &nativePostgresRuntimeFixture{
		container: &nativePostgresContainerFixture{removeErrs: []error{removeErr, nil}},
	}
	name := "qualification-browser"

	err := removeQualificationNamedContainerHandle(t.Context(), runtime, &name)
	require.ErrorIs(t, err, removeErr)
	require.Equal(t, "qualification-browser", name)

	require.NoError(t, removeQualificationNamedContainerHandle(t.Context(), runtime, &name))
	require.Empty(t, name)
	require.Equal(t, 2, runtime.container.removed)
}

func TestDockerCLIContainerMapsLifecycleOperations(t *testing.T) {
	executor := &recordingQualificationExecutor{output: []byte("ok")}
	runtime := newDockerCLIQualificationRuntime(t.TempDir(), "docker-probe", executor)
	container := runtime.Existing("qualification-browser")
	stdin := strings.NewReader("input")

	operations := []struct {
		run  func() ([]byte, error)
		want []string
	}{
		{func() ([]byte, error) { return container.Exec(t.Context(), stdin, "sh", "-c", "echo ready") },
			[]string{"exec", "-i", "qualification-browser", "sh", "-c", "echo ready"}},
		{func() ([]byte, error) { return container.CopyTo(t.Context(), "/host/file", "/work/file") },
			[]string{"cp", "/host/file", "qualification-browser:/work/file"}},
		{func() ([]byte, error) { return container.Restart(t.Context()) },
			[]string{"restart", "qualification-browser"}},
		{func() ([]byte, error) { return container.Kill(t.Context(), "KILL") },
			[]string{"kill", "--signal", "KILL", "qualification-browser"}},
		{func() ([]byte, error) { return container.Start(t.Context()) },
			[]string{"start", "qualification-browser"}},
		{func() ([]byte, error) { return container.Inspect(t.Context(), "{{.State.Status}}") },
			[]string{"inspect", "--format", "{{.State.Status}}", "qualification-browser"}},
		{func() ([]byte, error) { return container.Logs(t.Context(), 100) },
			[]string{"logs", "--tail", "100", "qualification-browser"}},
		{func() ([]byte, error) { return container.Remove(t.Context()) },
			[]string{"rm", "--force", "qualification-browser"}},
	}
	for index, operation := range operations {
		if _, err := operation.run(); err != nil {
			t.Fatalf("operation %d: %v", index, err)
		}
		request := executor.requests[index]
		if !slices.Equal(request.Arguments, operation.want) {
			t.Fatalf("operation %d arguments=%v want=%v", index, request.Arguments, operation.want)
		}
	}
	if executor.requests[0].Stdin != io.Reader(stdin) {
		t.Fatal("exec did not preserve stdin")
	}
}

func TestDockerCLIRuntimeRejectsIncompleteContainerRequest(t *testing.T) {
	runtime := newDockerCLIQualificationRuntime(t.TempDir(), "docker", &recordingQualificationExecutor{})
	for name, request := range map[string]qualificationContainerRequest{
		"missing name":  {Image: "browser:stable"},
		"missing image": {Name: "browser"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtime.Start(context.Background(), request); err == nil {
				t.Fatal("Start() accepted incomplete request")
			}
		})
	}
}

func TestQualificationContainerOperationErrorCapturesRedactedDiagnostics(t *testing.T) {
	executor := &recordingQualificationExecutor{
		output: []byte("status=unhealthy access_token=secret"),
	}
	runtime := newDockerCLIQualificationRuntime(t.TempDir(), "docker-probe", executor)
	container := runtime.Existing("qualification-browser")

	err := qualificationContainerOperationError(
		t.Context(), container, "install browser dependencies", errors.New("exit 1"),
	)
	if err == nil {
		t.Fatal("operation error is nil")
	}
	message := err.Error()
	for _, expected := range []string{
		"install browser dependencies",
		"exit 1",
		"status=unhealthy",
		"[REDACTED]",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("error %q does not contain %q", message, expected)
		}
	}
	if strings.Contains(message, "secret") {
		t.Fatalf("error retained secret: %q", message)
	}
	want := [][]string{
		{"inspect", "--format", "{{.State.Status}}", "qualification-browser"},
		{"logs", "--tail", "100", "qualification-browser"},
	}
	if len(executor.requests) != len(want) {
		t.Fatalf("requests=%v", executor.requests)
	}
	for index := range want {
		if !slices.Equal(executor.requests[index].Arguments, want[index]) {
			t.Fatalf("request %d=%v want=%v", index, executor.requests[index].Arguments, want[index])
		}
	}
}
