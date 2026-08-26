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
	"github.com/flidai/leapview/internal/platform/compatibility"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/ociref"
)

const (
	imageDeploymentPayloadPath = "/usr/local/share/leapview/deployment/."
	payloadCleanupTimeout      = 30 * time.Second
	controllerLockName         = ".leapviewctl.lock"
)

type PayloadLoadFunc func(context.Context, string) (map[string][]byte, error)
type UnitEnabledFunc func(context.Context, string) (bool, error)

type DeploymentPayloadManagerOptions struct {
	Paths       Paths
	DockerBin   string
	Stdout      io.Writer
	Stderr      io.Writer
	Load        PayloadLoadFunc
	Run         RunFunc
	UnitEnabled UnitEnabledFunc
}

type DeploymentPayloadManager struct {
	paths       Paths
	load        PayloadLoadFunc
	run         RunFunc
	unitEnabled UnitEnabledFunc
	dockerBin   string
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
	unitEnabled := options.UnitEnabled
	if unitEnabled == nil {
		unitEnabled = func(ctx context.Context, unit string) (bool, error) {
			command := exec.CommandContext(ctx, paths.Systemctl, "is-enabled", "--quiet", unit)
			if err := command.Run(); err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) && exit.ExitCode() == 1 {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}
	}
	return &DeploymentPayloadManager{paths: paths, load: load, run: run, unitEnabled: unitEnabled, dockerBin: dockerBin}, nil
}

func (m *DeploymentPayloadManager) Prepare(
	ctx context.Context,
	current string,
	next string,
	platform string,
	policyDocument []byte,
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
	policyDocument, err = m.policyForTargetGeneration(next, policyDocument)
	if err != nil {
		return nil, err
	}
	nextPayload["release-transition-policy.json"] = append([]byte(nil), policyDocument...)
	if err := validatePolicyArtifact(policyDocument, next, platform); err != nil {
		return nil, err
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
	if !generationMatchesImage(currentGeneration, currentReference) {
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
	currentApplicationEnvironment, err := securefs.ReadPrivateFile(filepath.Join(m.paths.Root, "leapview.env"))
	if err != nil {
		return nil, err
	}
	currentHasQualification, err := generationHasQualification(m.paths, currentGeneration)
	if err != nil {
		return nil, err
	}
	nextHasQualification := payloadHasQualification(nextPayload)
	nextApplicationEnvironment, err := configureQualificationEnvironment(currentApplicationEnvironment, nextHasQualification)
	if err != nil {
		return nil, err
	}
	currentUnitEnabled := false
	if currentHasQualification {
		currentUnitEnabled, err = m.unitEnabled(ctx, "leapview-recovery-qualification.timer")
		if err != nil {
			return nil, fmt.Errorf("read recovery qualification timer state: %w", err)
		}
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
		currentApplicationEnvironment: currentApplicationEnvironment, nextApplicationEnvironment: nextApplicationEnvironment,
		currentHasQualification: currentHasQualification, nextHasQualification: nextHasQualification,
		currentUnitEnabled: currentUnitEnabled, nextUnitEnabled: nextHasQualification,
	}, nil
}

// ReconcileCurrent completes the FAI-515 -> FAI-516 payload migration after a
// historical controller has activated the candidate's legacy payload subset.
// The historical backup-maintenance unit invokes this method through the new
// controller binary, providing a forward-compatible post-activation hook
// without granting the application container host or Docker authority.
func (m *DeploymentPayloadManager) ReconcileCurrent(ctx context.Context) error {
	lock, err := acquireControllerMigrationLock(ctx, m.paths.Root)
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			_ = lock.Release()
		}
	}()
	markerPath := filepath.Join(m.paths.Root, installMarkerName)
	installed, err := readMarker(markerPath)
	if err != nil || installed == nil {
		return err
	}
	currentGeneration, err := activeGeneration(m.paths)
	if err != nil {
		return err
	}
	reference, err := ociref.ParseImmutable(installed.Image)
	if err != nil {
		return err
	}
	if !generationMatchesImage(currentGeneration, reference) {
		return fmt.Errorf("active host generation %q does not match installed image %q", currentGeneration, installed.Image)
	}
	deploymentEnvironment, err := securefs.ReadPrivateFile(filepath.Join(m.paths.Root, "deployment.env"))
	if err != nil {
		return err
	}
	activeImage, err := deploymentEnvironmentValue(deploymentEnvironment, "LEAPVIEW_IMAGE")
	if err != nil || activeImage != installed.Image {
		return fmt.Errorf("managed deployment environment does not match installed image %q", installed.Image)
	}
	applicationEnvironment, err := securefs.ReadPrivateFile(filepath.Join(m.paths.Root, "leapview.env"))
	if err != nil {
		return err
	}
	configuredEnvironment, err := configureQualificationEnvironment(applicationEnvironment, true)
	if err != nil {
		return err
	}
	currentHasQualification, err := generationHasQualification(m.paths, currentGeneration)
	if err != nil {
		return err
	}
	currentUnitEnabled := false
	if currentHasQualification {
		currentUnitEnabled, err = m.unitEnabled(ctx, "leapview-recovery-qualification.timer")
		if err != nil {
			return fmt.Errorf("read recovery qualification timer state: %w", err)
		}
	}
	if currentHasQualification && bytes.Equal(applicationEnvironment, configuredEnvironment) && currentUnitEnabled {
		return nil
	}
	payload, err := m.load(ctx, installed.Image)
	if err != nil {
		return fmt.Errorf("load admitted deployment payload for recovery qualification migration: %w", err)
	}
	if !payloadHasQualification(payload) {
		return fmt.Errorf("admitted deployment payload does not contain the complete recovery qualification runner")
	}
	policyDocument, err := m.policyForTargetGeneration(installed.Image, payload["release-transition-policy.json"])
	if err != nil {
		return err
	}
	payload["release-transition-policy.json"] = policyDocument
	platformName, err := m.dockerPlatform(ctx)
	if err != nil {
		return err
	}
	if err := validatePolicyArtifact(policyDocument, installed.Image, platformName); err != nil {
		return err
	}
	qualifiedGeneration, err := stageGeneration(m.paths, installed.Image, payload)
	if err != nil {
		return fmt.Errorf("stage complete recovery qualification payload: %w", err)
	}
	rollback := func() error {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), payloadCleanupTimeout)
		defer cancel()
		return m.applyQualificationHostState(
			rollbackCtx, currentGeneration, applicationEnvironment, currentHasQualification, currentUnitEnabled, false,
		)
	}
	if err := m.applyQualificationHostState(ctx, qualifiedGeneration, configuredEnvironment, true, true, false); err != nil {
		return errors.Join(err, rollback())
	}
	if err := lock.Release(); err != nil {
		return err
	}
	locked = false
	if err := m.run(ctx, m.paths.Systemctl, "start", "leapview-recovery-qualification.service"); err != nil {
		rollbackLock, lockErr := acquireControllerMigrationLock(ctx, m.paths.Root)
		if lockErr != nil {
			return errors.Join(fmt.Errorf("validate recovery qualification runner prerequisites: %w", err), lockErr)
		}
		rollbackErr := rollback()
		releaseErr := rollbackLock.Release()
		return errors.Join(fmt.Errorf("validate recovery qualification runner prerequisites: %w", err), rollbackErr, releaseErr)
	}
	return nil
}

func acquireControllerMigrationLock(ctx context.Context, root string) (*instancelock.Lock, error) {
	for {
		lock, err := instancelock.AcquireNamed(root, controllerLockName)
		if err == nil {
			return lock, nil
		}
		if !strings.Contains(err.Error(), "another process") {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (m *DeploymentPayloadManager) dockerPlatform(ctx context.Context) (string, error) {
	var output bytes.Buffer
	if err := runPayloadDocker(ctx, m.dockerBin, nil, &output, io.Discard, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}"); err != nil {
		return "", fmt.Errorf("resolve recovery qualification Docker platform: %w", err)
	}
	platformName := strings.TrimSpace(output.String())
	if platformName == "" || strings.ContainsAny(platformName, " \t\r\n") {
		return "", fmt.Errorf("Docker returned invalid target platform %q", platformName)
	}
	return platformName, nil
}

func (m *DeploymentPayloadManager) applyQualificationHostState(
	ctx context.Context,
	generation string,
	applicationEnvironment []byte,
	hasQualification bool,
	unitEnabled bool,
	verify bool,
) error {
	if !hasQualification {
		if err := m.run(ctx, m.paths.Systemctl, "disable", "--now", "leapview-recovery-qualification.timer"); err != nil {
			return fmt.Errorf("disable recovery qualification timer: %w", err)
		}
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(m.paths.Root, "leapview.env"), applicationEnvironment); err != nil {
		return err
	}
	if err := activateGeneration(m.paths, generation); err != nil {
		return err
	}
	files := legacyPayloadFiles
	if hasQualification {
		files = requiredPayloadFiles
	}
	if err := m.ensureMigrationPayloadLinks(ctx, files); err != nil {
		return err
	}
	if !hasQualification {
		if err := removeQualificationPayloadLinks(m.paths); err != nil {
			return err
		}
	}
	if err := m.run(ctx, m.paths.Systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd after recovery qualification migration: %w", err)
	}
	if hasQualification {
		command := "disable"
		arguments := []string{"--now", "leapview-recovery-qualification.timer"}
		if unitEnabled {
			command = "enable"
			arguments = []string{"--now", "leapview-recovery-qualification.timer"}
		}
		if err := m.run(ctx, m.paths.Systemctl, append([]string{command}, arguments...)...); err != nil {
			return fmt.Errorf("%s recovery qualification timer: %w", command, err)
		}
		if verify && unitEnabled {
			if err := m.run(ctx, m.paths.Systemctl, "start", "leapview-recovery-qualification.service"); err != nil {
				return fmt.Errorf("validate recovery qualification runner prerequisites: %w", err)
			}
		}
	}
	return nil
}

func (m *DeploymentPayloadManager) ensureMigrationPayloadLinks(ctx context.Context, files []payloadFile) error {
	for _, file := range files {
		target := file.Target(m.paths)
		if filepath.Clean(filepath.Dir(target)) != filepath.Clean(m.paths.Systemd) {
			if err := ensurePayloadLinksFor(m.paths, []payloadFile{file}); err != nil {
				return err
			}
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			if err := ensurePayloadLinksFor(m.paths, []payloadFile{file}); err != nil {
				return err
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		source := filepath.Join(m.paths.Root, "current", file.Source)
		if err := os.Symlink(source, target); err == nil {
			if err := syncPath(filepath.Dir(target)); err != nil {
				return err
			}
			continue
		}
		// systemctl link asks PID 1 to create the unit link and remains usable
		// from the read-only filesystem namespace of the legacy maintenance unit.
		if err := m.run(ctx, m.paths.Systemctl, "link", source); err != nil {
			return fmt.Errorf("link recovery qualification unit %s: %w", file.Source, err)
		}
	}
	return nil
}

func (m *DeploymentPayloadManager) policyForTargetGeneration(image string, supplied []byte) ([]byte, error) {
	reference, err := ociref.ParseImmutable(image)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(m.paths.Root, "releases", reference.Generation, "release-transition-policy.json")
	contents, err := os.ReadFile(path)
	if err == nil {
		return contents, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	if len(supplied) == 0 {
		return nil, fmt.Errorf("target release-transition policy is required")
	}
	return supplied, nil
}

func validatePolicyArtifact(document []byte, image, platform string) error {
	policy, err := compatibility.ParsePolicy(document)
	if err != nil {
		return fmt.Errorf("validate target release-transition policy: %w", err)
	}
	decision := policy.EvaluateImages(compatibility.OperationFreshInstall, "", image, platform)
	if err := decision.Err(); err != nil {
		return fmt.Errorf("target release-transition policy does not bind image %s for %s: %w", image, platform, err)
	}
	return nil
}

type deploymentPayloadUpdate struct {
	paths                         Paths
	run                           RunFunc
	ctx                           context.Context
	currentGeneration             string
	nextGeneration                string
	currentMarker                 []byte
	nextMarker                    []byte
	currentEnvironment            []byte
	nextEnvironment               []byte
	currentApplicationEnvironment []byte
	nextApplicationEnvironment    []byte
	currentHasQualification       bool
	nextHasQualification          bool
	currentUnitEnabled            bool
	nextUnitEnabled               bool
	rolledBack                    bool
}

func (u *deploymentPayloadUpdate) Apply() error {
	if u.rolledBack {
		return fmt.Errorf("deployment payload update was already rolled back")
	}
	if err := u.write(
		u.ctx, u.nextGeneration, u.nextEnvironment, u.nextApplicationEnvironment, u.nextMarker,
		u.nextHasQualification, u.nextUnitEnabled,
	); err != nil {
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
	err := u.write(
		ctx, u.currentGeneration, u.currentEnvironment, u.currentApplicationEnvironment, u.currentMarker,
		u.currentHasQualification, u.currentUnitEnabled,
	)
	if err == nil {
		u.rolledBack = true
	}
	return err
}

func (u *deploymentPayloadUpdate) write(
	ctx context.Context,
	generation string,
	environment []byte,
	applicationEnvironment []byte,
	marker []byte,
	hasQualification bool,
	unitEnabled bool,
) error {
	if !hasQualification {
		if err := u.disableQualification(ctx); err != nil {
			return err
		}
	}
	if err := writeAtomic(filepath.Join(u.paths.Root, "deployment.env"), environment, 0o600); err != nil {
		return fmt.Errorf("write deployment environment: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(u.paths.Root, installMarkerName), marker); err != nil {
		return fmt.Errorf("write host installation marker: %w", err)
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(u.paths.Root, "leapview.env"), applicationEnvironment); err != nil {
		return fmt.Errorf("write managed application environment: %w", err)
	}
	if err := activateGeneration(u.paths, generation); err != nil {
		return fmt.Errorf("activate deployment generation: %w", err)
	}
	files := legacyPayloadFiles
	if hasQualification {
		files = requiredPayloadFiles
	}
	if err := ensurePayloadLinksFor(u.paths, files); err != nil {
		return fmt.Errorf("install deployment links: %w", err)
	}
	if !hasQualification {
		if err := removeQualificationPayloadLinks(u.paths); err != nil {
			return err
		}
	}
	if err := u.run(ctx, u.paths.Systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if hasQualification {
		if err := u.setQualificationTimer(ctx, unitEnabled); err != nil {
			return err
		}
	}
	return nil
}

func (u *deploymentPayloadUpdate) disableQualification(ctx context.Context) error {
	if err := u.run(ctx, u.paths.Systemctl, "disable", "--now", "leapview-recovery-qualification.timer"); err != nil {
		return fmt.Errorf("disable recovery qualification timer: %w", err)
	}
	return nil
}

func (u *deploymentPayloadUpdate) setQualificationTimer(ctx context.Context, enabled bool) error {
	if enabled {
		if err := u.run(ctx, u.paths.Systemctl, "enable", "--now", "leapview-recovery-qualification.timer"); err != nil {
			return fmt.Errorf("enable recovery qualification timer: %w", err)
		}
		return nil
	}
	return u.disableQualification(ctx)
}

func (u *deploymentPayloadUpdate) Close() error { return nil }

func validatePayloadContents(payload map[string][]byte) error {
	_, err := payloadFiles(payload)
	return err
}

func generationHasQualification(paths Paths, generation string) (bool, error) {
	present := 0
	for _, file := range qualificationPayloadFiles {
		info, err := os.Lstat(filepath.Join(paths.Root, "releases", generation, file.Source))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("recovery qualification payload %s must be regular", file.Source)
		}
		present++
	}
	if present == 0 {
		return false, nil
	}
	if present != len(qualificationPayloadFiles) {
		return false, fmt.Errorf("deployment generation contains an incomplete recovery qualification payload")
	}
	return true, nil
}

func configureQualificationEnvironment(contents []byte, enabled bool) ([]byte, error) {
	const enabledKey = "LEAPVIEW_RECOVERY_QUALIFICATION_ENABLED"
	const ownerKey = "LEAPVIEW_RECOVERY_QUALIFICATION_EXECUTION_ENVIRONMENT"
	lines := strings.Split(string(contents), "\n")
	result := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		key, _, found := strings.Cut(line, "=")
		if found && (key == enabledKey || key == ownerKey) {
			continue
		}
		if strings.ContainsAny(line, "\r\x00") {
			return nil, fmt.Errorf("managed application environment contains an invalid line")
		}
		result = append(result, line)
	}
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	if enabled {
		result = append(result, enabledKey+"=true", ownerKey+"=host")
	}
	return []byte(strings.Join(result, "\n") + "\n"), nil
}

func removeQualificationPayloadLinks(paths Paths) error {
	for _, file := range qualificationPayloadFiles {
		target := file.Target(paths)
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("recovery qualification target %s is not a symbolic link", target)
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := syncPath(filepath.Dir(target)); err != nil {
			return err
		}
	}
	qualificationDirectory := filepath.Join(paths.Root, "qualification")
	if err := os.Remove(qualificationDirectory); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove empty recovery qualification directory: %w", err)
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
