package composectl

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type scopedQualificationRuntime struct{}

func (*scopedQualificationRuntime) Start(context.Context, qualificationContainerRequest) (qualificationContainer, error) {
	return nil, nil
}

func (*scopedQualificationRuntime) Existing(string) qualificationContainer {
	return nil
}

func TestControllerScopedPreservesInjectedQualificationRuntimeAndDependencies(t *testing.T) {
	root := t.TempDir()
	childRoot := filepath.Join(t.TempDir(), "bundle")
	stdin := strings.NewReader("input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	now := func() time.Time { return time.Unix(123, 0) }
	sleep := func(context.Context, time.Duration) error { return nil }
	executor := qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
		return nil, nil
	})
	runtime := &scopedQualificationRuntime{}
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", Stdin: stdin,
		Stdout: stdout, Stderr: stderr, Now: now, Sleep: sleep,
		qualificationExecutor: executor, qualificationContainers: runtime,
	})
	require.NoError(t, err)

	childOutput := &bytes.Buffer{}
	child, err := controller.scoped(childRoot, childOutput)
	require.NoError(t, err)
	require.Same(t, runtime, child.qualificationContainers)
	require.Same(t, stdin, child.stdin)
	require.Same(t, childOutput, child.stdout)
	require.Same(t, stderr, child.stderr)
	if reflect.ValueOf(child.qualificationExecutor).Pointer() != reflect.ValueOf(executor).Pointer() {
		t.Fatal("scoped controller did not preserve command executor")
	}
	if reflect.ValueOf(child.now).Pointer() != reflect.ValueOf(controller.now).Pointer() {
		t.Fatal("scoped controller did not preserve clock")
	}
	if reflect.ValueOf(child.sleep).Pointer() != reflect.ValueOf(controller.sleep).Pointer() {
		t.Fatal("scoped controller did not preserve sleep function")
	}
	if child.root != childRoot {
		t.Fatalf("scoped controller root = %q, want %q", child.root, childRoot)
	}
}

func TestControllerScopedRerootsDefaultDockerCLIQualificationRuntime(t *testing.T) {
	executor := &recordingQualificationExecutor{}
	root := t.TempDir()
	controller, err := New(Options{Root: root, DockerBin: "docker-probe", qualificationExecutor: executor})
	require.NoError(t, err)
	parentRuntime, ok := controller.qualificationContainers.(*dockerCLIQualificationRuntime)
	require.True(t, ok)

	childRoot := filepath.Join(t.TempDir(), "bundle")
	child, err := controller.scoped(childRoot, &bytes.Buffer{})
	require.NoError(t, err)
	childRuntime, ok := child.qualificationContainers.(*dockerCLIQualificationRuntime)
	require.True(t, ok)
	if childRuntime == parentRuntime {
		t.Fatal("scoped controller reused the default Docker CLI runtime")
	}
	if childRuntime.process.dir != childRoot {
		t.Fatalf("child Docker CLI runtime directory = %q, want %q", childRuntime.process.dir, childRoot)
	}
	if childRuntime.process.executable != parentRuntime.process.executable {
		t.Fatalf("child Docker executable = %q, want %q", childRuntime.process.executable, parentRuntime.process.executable)
	}
	require.Same(t, executor, childRuntime.executor)
}

func TestControllerScopedValidatesReceiverRootAndOutput(t *testing.T) {
	var nilController *Controller
	if _, err := nilController.scoped(t.TempDir(), io.Discard); err == nil {
		t.Fatal("nil controller accepted by scoped")
	}

	controller, err := New(Options{Root: t.TempDir()})
	require.NoError(t, err)
	for name, root := range map[string]string{
		"empty":      "",
		"whitespace": " \t ",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := controller.scoped(root, io.Discard); err == nil {
				t.Fatal("empty scoped root accepted")
			}
		})
	}
	if _, err := controller.scoped(t.TempDir(), nil); err == nil {
		t.Fatal("nil scoped output accepted")
	}
}
