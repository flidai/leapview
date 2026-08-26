package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const (
	installMarkerName = ".host-install.json"
	installLockName   = ".host-install.lock"
)

type Config struct {
	SchemaVersion int    `json:"schemaVersion"`
	Domain        string `json:"domain"`
	AdminEmail    string `json:"adminEmail"`
	Environment   string `json:"environment"`
	Image         string `json:"image"`
	HTTPS         *bool  `json:"https"`
}

type Paths struct {
	Payload   string
	Config    string
	Root      string
	ConfigDir string
	SystemBin string
	Systemd   string
	Systemctl string
}

type Lifecycle interface {
	Initialize(context.Context, composectl.InitOptions) error
	Start(context.Context) error
	RunScheduledRecoveryQualification(context.Context) error
}

type LifecycleFactory func(root string) (Lifecycle, error)
type RunFunc func(context.Context, string, ...string) error

type Options struct {
	Paths            Paths
	LifecycleFactory LifecycleFactory
	Run              RunFunc
	ExpectedImage    string
	DockerBin        string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
}

type Installer struct {
	paths            Paths
	lifecycleFactory LifecycleFactory
	run              RunFunc
	expectedImage    string
}

func DefaultPaths(payload, config string) Paths {
	paths := InstalledPaths("/opt/leapview")
	paths.Payload = payload
	paths.Config = config
	return paths
}

func InstalledPaths(root string) Paths {
	return Paths{
		Root:      root,
		ConfigDir: "/etc/leapview",
		SystemBin: "/usr/local/sbin",
		Systemd:   "/etc/systemd/system",
		Systemctl: "systemctl",
	}
}

func New(options Options) (*Installer, error) {
	paths := options.Paths
	for name, path := range map[string]string{
		"payload": paths.Payload, "configuration": paths.Config, "installation root": paths.Root,
		"configuration directory": paths.ConfigDir, "system binary directory": paths.SystemBin,
		"systemd directory": paths.Systemd, "systemctl": paths.Systemctl,
	} {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("%s path is required", name)
		}
	}
	factory := options.LifecycleFactory
	if factory == nil {
		factory = func(root string) (Lifecycle, error) {
			return composectl.New(composectl.Options{
				Root: root, DockerBin: options.DockerBin, Stdin: options.Stdin,
				Stdout: options.Stdout, Stderr: options.Stderr,
			})
		}
	}
	run := options.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) error {
			command := exec.CommandContext(ctx, name, args...)
			command.Stdin = options.Stdin
			command.Stdout = options.Stdout
			command.Stderr = options.Stderr
			return command.Run()
		}
	}
	return &Installer{
		paths: paths, lifecycleFactory: factory, run: run,
		expectedImage: strings.TrimSpace(options.ExpectedImage),
	}, nil
}

func (i *Installer) Install(ctx context.Context) error {
	config, normalized, err := readAndValidateConfig(i.paths.Config)
	if err != nil {
		return fmt.Errorf("validate host installation configuration: %w", err)
	}
	if i.expectedImage != "" && normalized.Image != i.expectedImage {
		return fmt.Errorf("bootstrap configuration image does not match the extracted deployment payload image")
	}
	payload, err := readPayload(i.paths.Payload)
	if err != nil {
		return fmt.Errorf("validate host installation payload: %w", err)
	}
	if !payloadHasQualification(payload) {
		return fmt.Errorf("validate host installation payload: the complete recovery qualification bundle is required")
	}
	if err := validatePolicyArtifact(payload["release-transition-policy.json"], normalized.Image, runtime.GOOS+"/"+runtime.GOARCH); err != nil {
		return err
	}
	if err := i.prepareDirectories(); err != nil {
		return err
	}
	lock, err := instancelock.AcquireNamed(i.paths.Root, installLockName)
	if err != nil {
		return err
	}
	defer lock.Release()
	installed, err := readMarker(filepath.Join(i.paths.Root, installMarkerName))
	if err != nil {
		return err
	}
	if installed != nil && !configsEqual(*installed, config) {
		return fmt.Errorf("bootstrap configuration does not match the installed instance; use leapviewctl lifecycle commands for changes")
	}
	generation, err := stageGeneration(i.paths, normalized.Image, payload)
	if err != nil {
		return fmt.Errorf("stage deployment generation: %w", err)
	}
	if err := ensurePayloadLinks(i.paths); err != nil {
		return fmt.Errorf("install deployment links: %w", err)
	}
	if err := activateGeneration(i.paths, generation); err != nil {
		return fmt.Errorf("activate deployment generation: %w", err)
	}
	deployment := filepath.Join(i.paths.Root, "deployment.env")
	if err := installInitialFile(deployment, payload["deployment.env.example"], 0o600); err != nil {
		return fmt.Errorf("install deployment environment: %w", err)
	}
	lifecycle, err := i.lifecycleFactory(i.paths.Root)
	if err != nil {
		return err
	}
	if installed == nil {
		if err := lifecycle.Initialize(ctx, composectl.InitOptions{
			AdminEmail: normalized.AdminEmail, Domain: normalized.Domain,
			Environment: normalized.Environment, Image: normalized.Image,
			NoHTTPS: !*config.HTTPS,
		}); err != nil {
			return fmt.Errorf("initialize LeapView: %w", err)
		}
	}
	if err := lifecycle.Start(ctx); err != nil {
		return fmt.Errorf("start LeapView: %w", err)
	}
	marker, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	marker = append(marker, '\n')
	if err := securefs.WritePrivateFileAtomic(filepath.Join(i.paths.Root, installMarkerName), marker); err != nil {
		return fmt.Errorf("write host installation marker: %w", err)
	}
	// Reconcile the four owner schedules through the same production entrypoint
	// used by the timer. This validates the released controller, bundle, Docker
	// capability, managed storage, and evidence permissions before installation
	// can claim scheduled qualification is active.
	if err := lifecycle.RunScheduledRecoveryQualification(ctx); err != nil {
		return fmt.Errorf("validate managed recovery qualification: %w", err)
	}
	if err := i.run(ctx, i.paths.Systemctl, "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd: %w", err)
	}
	if err := i.run(ctx, i.paths.Systemctl, "enable", "--now", "leapview-backup.timer"); err != nil {
		return fmt.Errorf("enable LeapView backup timer: %w", err)
	}
	if err := i.run(ctx, i.paths.Systemctl, "enable", "--now", "leapview-backup-maintenance.timer"); err != nil {
		return fmt.Errorf("enable LeapView backup maintenance timer: %w", err)
	}
	if err := i.run(ctx, i.paths.Systemctl, "enable", "--now", "leapview-recovery-qualification.timer"); err != nil {
		return fmt.Errorf("enable LeapView recovery qualification timer: %w", err)
	}
	return nil
}

func (i *Installer) prepareDirectories() error {
	for _, path := range []string{i.paths.Root, i.paths.ConfigDir, filepath.Join(i.paths.Root, "backups")} {
		if err := securefs.EnsurePrivateDir(path); err != nil {
			return err
		}
	}
	for _, path := range []string{i.paths.SystemBin, i.paths.Systemd} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}
