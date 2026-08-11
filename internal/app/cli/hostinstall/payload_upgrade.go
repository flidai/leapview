package hostinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/ociref"
)

const (
	imageDeploymentPayloadPath = "/usr/local/share/leapview/deployment/."
	payloadCleanupTimeout      = 30 * time.Second
)

type PayloadLoadFunc func(context.Context, string) (map[string][]byte, error)

type DeploymentPayloadManagerOptions struct {
	Paths     Paths
	DockerBin string
	Stdout    io.Writer
	Stderr    io.Writer
	Load      PayloadLoadFunc
	Run       RunFunc
}

type DeploymentPayloadManager struct {
	paths Paths
	load  PayloadLoadFunc
	run   RunFunc
}

func NewDeploymentPayloadManager(options DeploymentPayloadManagerOptions) (*DeploymentPayloadManager, error) {
	paths := options.Paths
	for name, path := range map[string]string{
		"installation root":       paths.Root,
		"system binary directory": paths.SystemBin,
		"systemd directory":       paths.Systemd,
		"systemctl":               paths.Systemctl,
	} {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%s path is required", name)
		}
	}
	dockerBin := strings.TrimSpace(options.DockerBin)
	if dockerBin == "" {
		dockerBin = "docker"
	}
	stdout := options.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	load := options.Load
	if load == nil {
		load = func(ctx context.Context, image string) (map[string][]byte, error) {
			return loadImageDeploymentPayload(ctx, dockerBin, image, stdout, stderr)
		}
	}
	run := options.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			command := exec.CommandContext(ctx, name, args...)
			command.Stdout = stdout
			command.Stderr = stderr
			return command.Run()
		}
	}
	return &DeploymentPayloadManager{paths: paths, load: load, run: run}, nil
}

func (m *DeploymentPayloadManager) Prepare(
	ctx context.Context,
	current string,
	next string,
) (composectl.DeploymentPayloadUpdate, error) {
	markerPath := filepath.Join(m.paths.Root, installMarkerName)
	installed, err := readMarker(markerPath)
	if err != nil {
		return nil, err
	}
	if installed == nil {
		return nil, nil
	}
	if installed.Image != current {
		return nil, fmt.Errorf("installed host payload image %q does not match active image %q", installed.Image, current)
	}
	nextPayload, err := m.load(ctx, next)
	if err != nil {
		return nil, fmt.Errorf("load deployment payload from %s: %w", next, err)
	}
	if err := validatePayloadContents(nextPayload); err != nil {
		return nil, err
	}
	currentGeneration, err := activeGeneration(m.paths)
	if err != nil {
		return nil, fmt.Errorf("read active deployment generation: %w", err)
	}
	currentReference, err := ociref.ParseImmutable(current)
	if err != nil {
		return nil, err
	}
	if currentGeneration != currentReference.Generation {
		return nil, fmt.Errorf("active host generation %q does not match image %q", currentGeneration, current)
	}
	nextGeneration, err := stageGeneration(m.paths, next, nextPayload)
	if err != nil {
		return nil, fmt.Errorf("stage target deployment generation: %w", err)
	}
	currentMarker, err := securefs.ReadPrivateFile(markerPath)
	if err != nil {
		return nil, err
	}
	currentEnvironment, err := securefs.ReadPrivateFile(filepath.Join(m.paths.Root, "deployment.env"))
	if err != nil {
		return nil, err
	}
	nextCaddyImage, err := deploymentEnvironmentValue(nextPayload["deployment.env.example"], "CADDY_IMAGE")
	if err != nil {
		return nil, fmt.Errorf("read target proxy image: %w", err)
	}
	nextEnvironment, err := replaceDeploymentEnvironmentValues(currentEnvironment, map[string]string{
		"LEAPVIEW_IMAGE": next,
		"CADDY_IMAGE":    nextCaddyImage,
	})
	if err != nil {
		return nil, err
	}
	nextConfig := *installed
	nextConfig.Image = next
	nextMarker, err := json.MarshalIndent(nextConfig, "", "  ")
	if err != nil {
		return nil, err
	}
	nextMarker = append(nextMarker, '\n')
	return &deploymentPayloadUpdate{
		paths: m.paths, run: m.run, ctx: ctx,
		currentGeneration: currentGeneration, nextGeneration: nextGeneration,
		currentMarker: currentMarker, nextMarker: nextMarker,
		currentEnvironment: currentEnvironment, nextEnvironment: nextEnvironment,
	}, nil
}

type deploymentPayloadUpdate struct {
	paths              Paths
	run                RunFunc
	ctx                context.Context
	currentGeneration  string
	nextGeneration     string
	currentMarker      []byte
	nextMarker         []byte
	currentEnvironment []byte
	nextEnvironment    []byte
	rolledBack         bool
}

func (u *deploymentPayloadUpdate) Apply() error {
	if u.rolledBack {
		return fmt.Errorf("deployment payload update was already rolled back")
	}
	if err := u.write(u.ctx, u.nextGeneration, u.nextEnvironment, u.nextMarker); err != nil {
		rollbackErr := u.restoreCurrent()
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (u *deploymentPayloadUpdate) Rollback() error {
	if u.rolledBack {
		return nil
	}
	return u.restoreCurrent()
}

func (u *deploymentPayloadUpdate) restoreCurrent() error {
	ctx, cancel := context.WithTimeout(context.Background(), payloadCleanupTimeout)
	defer cancel()
	err := u.write(ctx, u.currentGeneration, u.currentEnvironment, u.currentMarker)
	if err == nil {
		u.rolledBack = true
	}
	return err
}

func (u *deploymentPayloadUpdate) write(
	ctx context.Context,
	generation string,
	environment []byte,
	marker []byte,
) error {
	if err := writeAtomic(filepath.Join(u.paths.Root, "deployment.env"), environment, 0o600); err != nil {
		return fmt.Errorf("write deployment environment: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(u.paths.Root, installMarkerName), marker); err != nil {
		return fmt.Errorf("write host installation marker: %w", err)
	}
	if err := activateGeneration(u.paths, generation); err != nil {
		return fmt.Errorf("activate deployment generation: %w", err)
	}
	if err := u.run(ctx, u.paths.Systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	return nil
}

func (u *deploymentPayloadUpdate) Close() error { return nil }

func validatePayloadContents(payload map[string][]byte) error {
	for _, file := range requiredPayloadFiles {
		if len(payload[file.Source]) == 0 {
			return fmt.Errorf("deployment payload %s is missing or empty", file.Source)
		}
	}
	return nil
}

func deploymentEnvironmentValue(contents []byte, key string) (string, error) {
	for _, line := range strings.Split(string(contents), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found && name == key {
			value = strings.TrimSpace(value)
			if value == "" {
				return "", fmt.Errorf("%s is empty", key)
			}
			return value, nil
		}
	}
	return "", fmt.Errorf("%s is missing", key)
}

func replaceDeploymentEnvironmentValues(contents []byte, updates map[string]string) ([]byte, error) {
	lines := strings.Split(string(contents), "\n")
	found := make(map[string]bool, len(updates))
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value, exists := updates[key]
		if !exists {
			continue
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("deployment environment value %s is not a single line", key)
		}
		lines[index] = key + "=" + value
		found[key] = true
	}
	for key := range updates {
		if !found[key] {
			return nil, fmt.Errorf("deployment environment key %s is missing", key)
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func loadImageDeploymentPayload(
	ctx context.Context,
	dockerBin string,
	image string,
	stdout io.Writer,
	stderr io.Writer,
) (_ map[string][]byte, resultErr error) {
	if err := runPayloadDocker(ctx, dockerBin, nil, io.Discard, io.Discard, "image", "inspect", image); err != nil {
		if err := runPayloadDocker(ctx, dockerBin, nil, stdout, stderr, "pull", image); err != nil {
			return nil, err
		}
	}
	var containerOutput bytes.Buffer
	if err := runPayloadDocker(ctx, dockerBin, nil, &containerOutput, stderr, "create", image); err != nil {
		return nil, err
	}
	container := strings.TrimSpace(containerOutput.String())
	if container == "" || strings.ContainsAny(container, " \t\r\n") {
		return nil, fmt.Errorf("docker create returned an invalid container identifier")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), payloadCleanupTimeout)
		defer cancel()
		cleanupErr := runPayloadDocker(cleanupCtx, dockerBin, nil, io.Discard, io.Discard, "rm", "--force", container)
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	directory, err := os.MkdirTemp("", "leapview-deployment-payload-*")
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(directory)) }()
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	if err := runPayloadDocker(
		ctx, dockerBin, nil, stdout, stderr,
		"cp", container+":"+imageDeploymentPayloadPath, directory,
	); err != nil {
		return nil, err
	}
	return readPayload(directory)
}

func runPayloadDocker(
	ctx context.Context,
	dockerBin string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	args ...string,
) error {
	command := exec.CommandContext(ctx, dockerBin, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", dockerBin, strings.Join(args, " "), err)
	}
	return nil
}
