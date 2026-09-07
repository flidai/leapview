package composectl

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type healthQualificationRuntime struct {
	container *healthQualificationContainer
	existing  []string
}

func (runtime *healthQualificationRuntime) Start(context.Context, qualificationContainerRequest) (qualificationContainer, error) {
	return runtime.container, nil
}

func (runtime *healthQualificationRuntime) Existing(name string) qualificationContainer {
	runtime.existing = append(runtime.existing, name)
	if runtime.container == nil {
		return nil
	}
	return runtime.container
}

type healthQualificationContainer struct {
	name          string
	execArguments [][]string
	execErrors    []error
	inspect       []string
	inspectArgs   []string
	logs          []byte
}

func (container *healthQualificationContainer) Name() string { return container.name }

func (container *healthQualificationContainer) Exec(
	_ context.Context,
	_ io.Reader,
	command ...string,
) ([]byte, error) {
	container.execArguments = append(container.execArguments, append([]string(nil), command...))
	if len(container.execErrors) == 0 {
		return nil, nil
	}
	err := container.execErrors[0]
	container.execErrors = container.execErrors[1:]
	return nil, err
}

func (*healthQualificationContainer) CopyTo(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (*healthQualificationContainer) Restart(context.Context) ([]byte, error) { return nil, nil }

func (*healthQualificationContainer) Kill(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (*healthQualificationContainer) Start(context.Context) ([]byte, error) { return nil, nil }

func (container *healthQualificationContainer) Inspect(_ context.Context, format string) ([]byte, error) {
	container.inspectArgs = append(container.inspectArgs, format)
	if len(container.inspect) == 0 {
		return nil, nil
	}
	value := container.inspect[0]
	if len(container.inspect) > 1 {
		container.inspect = container.inspect[1:]
	}
	return []byte(value), nil
}

func (container *healthQualificationContainer) Logs(context.Context, int) ([]byte, error) {
	return container.logs, nil
}

func (*healthQualificationContainer) Remove(context.Context) ([]byte, error) { return nil, nil }

func newHealthQualificationController(
	t *testing.T,
	composeOutput string,
	runtime qualificationContainerRuntime,
) *Controller {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(
		root+"/"+deploymentEnvName,
		[]byte("COMPOSE_PROJECT_NAME=qualification-project\nCOMPOSE_HTTPS=0\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	executor := &recordingQualificationExecutor{output: []byte(composeOutput)}
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe",
		qualificationExecutor:   executor,
		qualificationContainers: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestQualificationApplicationContainerReportsMissingService(t *testing.T) {
	runtime := &healthQualificationRuntime{container: &healthQualificationContainer{name: "unused"}}
	controller := newHealthQualificationController(t, " \n", runtime)

	_, err := controller.qualificationApplicationContainer(t.Context())
	if err == nil || !strings.Contains(err.Error(), "qualification application container is missing") {
		t.Fatalf("missing application error = %v", err)
	}
	if len(runtime.existing) != 0 {
		t.Fatalf("runtime Existing calls = %v, want none", runtime.existing)
	}
}

func TestQualificationApplicationContainerRejectsMissingRuntimeHandle(t *testing.T) {
	controller := newHealthQualificationController(
		t,
		"compose-app\n",
		&healthQualificationRuntime{},
	)

	_, err := controller.qualificationApplicationContainer(t.Context())
	if err == nil || !strings.Contains(err.Error(), "qualification application container is missing") {
		t.Fatalf("missing runtime handle error = %v", err)
	}
}

func TestWaitQualificationBootstrapLivenessUsesContainerHealthcheckArgv(t *testing.T) {
	container := &healthQualificationContainer{name: "compose-app"}
	runtime := &healthQualificationRuntime{container: container}
	controller := newHealthQualificationController(t, "compose-app\n", runtime)

	if err := controller.waitQualificationBootstrapLiveness(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"leapview", "healthcheck",
		"--url", "http://127.0.0.1:8080/healthz",
		"--timeout", "5s",
	}
	if len(container.execArguments) != 1 || !slices.Equal(container.execArguments[0], want) {
		t.Fatalf("healthcheck argv = %v, want %v", container.execArguments, want)
	}
}

func TestWaitQualificationReadinessWaitsForDockerHealthyTransition(t *testing.T) {
	container := &healthQualificationContainer{
		name:        "compose-app",
		inspect:     []string{"starting", "healthy"},
		inspectArgs: nil,
	}
	runtime := &healthQualificationRuntime{container: container}
	controller := newHealthQualificationController(t, "compose-app\n", runtime)

	if err := controller.waitQualificationReadiness(t.Context()); err != nil {
		t.Fatal(err)
	}
	wantExec := []string{
		"leapview", "healthcheck",
		"--url", "http://127.0.0.1:8080/readyz",
		"--timeout", "5s",
	}
	if len(container.execArguments) != 1 || !slices.Equal(container.execArguments[0], wantExec) {
		t.Fatalf("readiness argv = %v, want %v", container.execArguments, wantExec)
	}
	if !slices.Equal(container.inspectArgs, []string{
		"{{.State.Health.Status}}", "{{.State.Health.Status}}",
	}) {
		t.Fatalf("health inspect formats = %v", container.inspectArgs)
	}
}

func TestWaitQualificationHealthyReusesQualificationContainer(t *testing.T) {
	container := &healthQualificationContainer{name: "candidate", inspect: []string{"healthy"}}
	runtime := &healthQualificationRuntime{container: container}
	controller := newHealthQualificationController(t, "unused\n", runtime)

	if err := controller.waitQualificationHealthy(t.Context(), "candidate", "restart persistence"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runtime.existing, []string{"candidate"}) {
		t.Fatalf("runtime Existing calls = %v", runtime.existing)
	}
	if !slices.Equal(container.inspectArgs, []string{"{{.State.Health.Status}}"}) {
		t.Fatalf("recovery inspect formats = %v", container.inspectArgs)
	}
}

func TestWaitQualificationContainerValuePreservesOperationErrorWrapping(t *testing.T) {
	container := &healthQualificationContainer{
		name:    "image-app",
		inspect: []string{"starting"},
		logs:    []byte("healthcheck failed"),
	}
	runtime := &healthQualificationRuntime{container: container}
	controller := newHealthQualificationController(t, "unused\n", runtime)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := controller.waitQualificationContainerValue(
		ctx,
		"image-app",
		"{{.State.Health.Status}}",
		"healthy",
		time.Minute,
	)
	if err == nil {
		t.Fatal("container value wait unexpectedly succeeded")
	}
	for _, expected := range []string{
		"wait for container state healthy",
		"context canceled",
		"healthcheck failed",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error %q does not contain %q", err, expected)
		}
	}
	if errors.Is(err, context.Canceled) == false {
		t.Fatalf("error %q does not preserve context cancellation", err)
	}
}

func TestUpgradeQualificationApplicationForceRecreatesAndWaitsForReady(t *testing.T) {
	container := &healthQualificationContainer{name: "new-container", inspect: []string{"healthy"}}
	runtime := &healthQualificationRuntime{container: container}
	root := t.TempDir()
	if err := os.WriteFile(root+"/"+deploymentEnvName, []byte("COMPOSE_PROJECT_NAME=qualification-project\nCOMPOSE_HTTPS=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests [][]string
	executor := qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
		requests = append(requests, append([]string(nil), request.Arguments...))
		joined := strings.Join(request.Arguments, " ")
		switch {
		case strings.HasSuffix(joined, "up -d --no-deps --force-recreate leapview"):
			return nil, nil
		case strings.HasSuffix(joined, "ps --quiet leapview"):
			return []byte("new-container\n"), nil
		default:
			return nil, errors.New("unexpected qualification compose command: " + joined)
		}
	})
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", qualificationExecutor: executor,
		qualificationContainers: runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := controller.upgradeQualificationApplication(t.Context(), "old-container")
	if err != nil {
		t.Fatal(err)
	}
	if upgraded != "new-container" {
		t.Fatalf("upgraded container = %q, want new-container", upgraded)
	}
	if len(requests) != 2 || !strings.Contains(strings.Join(requests[0], " "), "--force-recreate") {
		t.Fatalf("qualification upgrade commands = %v", requests)
	}
	if len(container.execArguments) != 1 || !strings.Contains(strings.Join(container.execArguments[0], " "), "/readyz") {
		t.Fatalf("upgrade readiness command = %v", container.execArguments)
	}
}

func TestAssertQualificationNativePostgresOnlyChecksKnownSQLiteAuthorities(t *testing.T) {
	container := &healthQualificationContainer{name: "candidate"}
	if err := assertQualificationNativePostgresOnly(t.Context(), container); err != nil {
		t.Fatal(err)
	}
	if len(container.execArguments) != 1 {
		t.Fatalf("SQLite authority probe calls = %d, want 1", len(container.execArguments))
	}
	probe := strings.Join(container.execArguments[0], " ")
	for _, path := range []string{
		"/var/lib/leapview/home/leapview.db",
		"/var/lib/leapview/home/libredash.db",
		"/var/lib/leapview/home/ducklake/catalog.sqlite",
	} {
		if !strings.Contains(probe, path) {
			t.Errorf("SQLite authority probe omits %s", path)
		}
	}
}
