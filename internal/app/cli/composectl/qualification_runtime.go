package composectl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type qualificationContainerVolume struct {
	Source   string
	Target   string
	ReadOnly bool
}

type qualificationContainerRequest struct {
	Name        string
	Image       string
	NetworkMode string
	Volumes     []qualificationContainerVolume
	Tmpfs       []string
	Environment map[string]string
	Entrypoint  []string
	Command     []string
	NoHealth    bool
	ReadyLog    string
}

type qualificationContainerRuntime interface {
	Start(context.Context, qualificationContainerRequest) (qualificationContainer, error)
	Existing(string) qualificationContainer
}

type qualificationContainer interface {
	Name() string
	Exec(context.Context, io.Reader, ...string) ([]byte, error)
	CopyTo(context.Context, string, string) ([]byte, error)
	Restart(context.Context) ([]byte, error)
	Kill(context.Context, string) ([]byte, error)
	Start(context.Context) ([]byte, error)
	Inspect(context.Context, string) ([]byte, error)
	Logs(context.Context, int) ([]byte, error)
	Remove(context.Context) ([]byte, error)
}

// removeQualificationNamedContainerHandle removes a disposable container and
// clears its name only after Docker confirms removal. Keeping the name on
// failure lets a later cleanup pass retry the same container instead of
// silently leaking it.
func removeQualificationNamedContainerHandle(
	ctx context.Context,
	runtime qualificationContainerRuntime,
	name *string,
) error {
	if name == nil || strings.TrimSpace(*name) == "" {
		return nil
	}
	if runtime == nil {
		return fmt.Errorf("qualification container runtime is required")
	}
	container := runtime.Existing(*name)
	if container == nil {
		return fmt.Errorf("qualification container %q is unavailable", *name)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := container.Remove(ctx)
	err = ignoreQualificationNotFound(err)
	if err == nil {
		*name = ""
	}
	return err
}

func qualificationContainerOperationError(
	ctx context.Context,
	container qualificationContainer,
	operation string,
	operationErr error,
) error {
	state, stateErr := container.Inspect(ctx, "{{.State.Status}}")
	logs, logsErr := container.Logs(ctx, 100)
	diagnostics := make([]string, 0, 2)
	if stateErr == nil {
		diagnostics = append(
			diagnostics,
			"state="+strings.TrimSpace(string(redactQualificationBytes(state))),
		)
	}
	if logsErr == nil {
		diagnostics = append(
			diagnostics,
			"logs="+strings.TrimSpace(string(redactQualificationLog(logs, 100))),
		)
	}
	if len(diagnostics) == 0 {
		return fmt.Errorf("%s in container %q: %w", operation, container.Name(), operationErr)
	}
	return fmt.Errorf(
		"%s in container %q: %w (%s)",
		operation,
		container.Name(),
		operationErr,
		strings.Join(diagnostics, "; "),
	)
}

type dockerCLIQualificationRuntime struct {
	process  qualificationProcess
	executor qualificationCommandExecutor
}

func newDockerCLIQualificationRuntime(
	root string,
	dockerBin string,
	executor qualificationCommandExecutor,
) *dockerCLIQualificationRuntime {
	return &dockerCLIQualificationRuntime{
		process: qualificationProcess{
			dir: root, executable: dockerBin, environment: os.Environ(),
		},
		executor: executor,
	}
}

func (runtime *dockerCLIQualificationRuntime) Start(
	ctx context.Context,
	request qualificationContainerRequest,
) (qualificationContainer, error) {
	request.Name = strings.TrimSpace(request.Name)
	request.Image = strings.TrimSpace(request.Image)
	if request.Name == "" || request.Image == "" {
		return nil, fmt.Errorf("qualification container name and image are required")
	}
	if len(request.Entrypoint) > 1 {
		return nil, fmt.Errorf("qualification container entrypoint must be a single executable")
	}
	arguments := []string{"run", "--detach", "--name", request.Name}
	if network := strings.TrimSpace(request.NetworkMode); network != "" {
		arguments = append(arguments, "--network", network)
	}
	for _, volume := range request.Volumes {
		source := strings.TrimSpace(volume.Source)
		target := strings.TrimSpace(volume.Target)
		if source == "" || target == "" {
			return nil, fmt.Errorf("qualification container volume source and target are required")
		}
		value := source + ":" + target
		if volume.ReadOnly {
			value += ":ro"
		}
		arguments = append(arguments, "--volume", value)
	}
	for _, mount := range request.Tmpfs {
		if strings.TrimSpace(mount) == "" {
			return nil, fmt.Errorf("qualification container tmpfs mount is required")
		}
		arguments = append(arguments, "--tmpfs", strings.TrimSpace(mount))
	}
	environment := make([]string, 0, len(request.Environment))
	for name, value := range request.Environment {
		environment = append(environment, name+"="+value)
	}
	sort.Strings(environment)
	for _, value := range environment {
		arguments = append(arguments, "--env", value)
	}
	if request.NoHealth {
		arguments = append(arguments, "--no-healthcheck")
	}
	if len(request.Entrypoint) > 0 {
		arguments = append(arguments, "--entrypoint", request.Entrypoint[0])
	}
	arguments = append(arguments, request.Image)
	arguments = append(arguments, request.Command...)
	if _, err := runtime.run(ctx, nil, arguments...); err != nil {
		return nil, err
	}
	container := runtime.Existing(request.Name)
	readyLog := strings.TrimSpace(request.ReadyLog)
	if readyLog == "" {
		return container, nil
	}
	if err := qualificationWait(ctx, 50*time.Millisecond, func(waitCtx context.Context) (bool, error) {
		logs, logErr := container.Logs(waitCtx, 100)
		return bytes.Contains(logs, []byte(readyLog)), logErr
	}); err != nil {
		operationErr := qualificationContainerOperationError(
			ctx, container, "wait for readiness log", err,
		)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = container.Remove(cleanupCtx)
		return nil, operationErr
	}
	return container, nil
}

func (runtime *dockerCLIQualificationRuntime) Existing(name string) qualificationContainer {
	return &dockerCLIQualificationContainer{
		name: strings.TrimSpace(name), runtime: runtime,
	}
}

func (runtime *dockerCLIQualificationRuntime) run(
	ctx context.Context,
	stdin io.Reader,
	arguments ...string,
) ([]byte, error) {
	return runtime.process.Run(ctx, stdin, runtime.executor, arguments...)
}

type dockerCLIQualificationContainer struct {
	name    string
	runtime *dockerCLIQualificationRuntime
}

func (container *dockerCLIQualificationContainer) Name() string {
	return container.name
}

func (container *dockerCLIQualificationContainer) Exec(
	ctx context.Context,
	stdin io.Reader,
	command ...string,
) ([]byte, error) {
	arguments := []string{"exec"}
	if stdin != nil {
		arguments = append(arguments, "-i")
	}
	arguments = append(arguments, container.name)
	arguments = append(arguments, command...)
	return container.runtime.run(ctx, stdin, arguments...)
}

func (container *dockerCLIQualificationContainer) CopyTo(
	ctx context.Context,
	source string,
	target string,
) ([]byte, error) {
	return container.runtime.run(ctx, nil, "cp", source, container.name+":"+target)
}

func (container *dockerCLIQualificationContainer) Restart(ctx context.Context) ([]byte, error) {
	return container.runtime.run(ctx, nil, "restart", container.name)
}

func (container *dockerCLIQualificationContainer) Kill(ctx context.Context, signal string) ([]byte, error) {
	return container.runtime.run(ctx, nil, "kill", "--signal", signal, container.name)
}

func (container *dockerCLIQualificationContainer) Start(ctx context.Context) ([]byte, error) {
	return container.runtime.run(ctx, nil, "start", container.name)
}

func (container *dockerCLIQualificationContainer) Inspect(ctx context.Context, format string) ([]byte, error) {
	return container.runtime.run(ctx, nil, "inspect", "--format", format, container.name)
}

func (container *dockerCLIQualificationContainer) Logs(ctx context.Context, tail int) ([]byte, error) {
	return container.runtime.run(ctx, nil, "logs", "--tail", strconv.Itoa(tail), container.name)
}

func (container *dockerCLIQualificationContainer) Remove(ctx context.Context) ([]byte, error) {
	return container.runtime.run(ctx, nil, "rm", "--force", container.name)
}
