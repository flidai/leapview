package hostinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/installationstate"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/hostmaintenance"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

const (
	installMarkerName       = ".host-install.json"
	installLockName         = ".host-install.lock"
	operatorBootstrapConfig = "/run/leapview/operator-bootstrap.json"
)

type Config = installationstate.Config

type Paths struct {
	Payload        string
	Config         string
	OperatorConfig string
	Root           string
	ConfigDir      string
	SystemBin      string
	Systemd        string
	Systemctl      string
}

type Lifecycle interface {
	UpdateImage(string) error
	PrepareFirstInstall(context.Context, composectl.FirstInstallOptions) error
	InitializeFirstInstall(context.Context, composectl.InitOptions, composectl.FirstInstallOptions) error
	ApplyFirstInstall(context.Context, composectl.FirstInstallOptions) error
	StartFirstInstallBootstrap(context.Context) error
	Start(context.Context) error
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
		OperatorConfig: operatorBootstrapConfig,
		Root:           root,
		ConfigDir:      "/etc/leapview",
		SystemBin:      "/usr/local/sbin",
		Systemd:        "/etc/systemd/system",
		Systemctl:      "systemctl",
	}
}

func New(options Options) (*Installer, error) {
	paths := options.Paths
	for name, path := range map[string]string{
		"payload": paths.Payload, "configuration": paths.Config, "operator bootstrap configuration": paths.OperatorConfig, "installation root": paths.Root,
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
	if err := i.prepareDirectories(); err != nil {
		return err
	}
	lock, err := instancelock.AcquireNamed(i.paths.Root, installLockName)
	if err != nil {
		return err
	}
	defer lock.Release()
	if err := hostmaintenance.Check(i.paths.Root); err != nil {
		return err
	}
	installed, err := readMarker(filepath.Join(i.paths.Root, installMarkerName))
	if err != nil {
		return err
	}
	if installed != nil && !configsEqual(installed.Config, config) {
		return fmt.Errorf("bootstrap configuration does not match the installed instance; use leapviewctl lifecycle commands for changes")
	}
	var operatorOptions composectl.FirstInstallOptions
	if installed == nil {
		_, operatorOptions, err = readAndValidateOperatorBootstrap(i.paths.OperatorConfig)
		if err != nil {
			return err
		}
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
		// The installed example is a link through the immutable generation.
		// Seed from the payload we validated above instead of asking the flat-
		// bundle fallback to follow that link. Preserve resumed operator input.
		if err := installInitialFile(filepath.Join(i.paths.Root, "leapview.env"), payload["leapview.env.example"], 0o600); err != nil {
			return fmt.Errorf("install application environment: %w", err)
		}
		// OCI payloads retain the image marker, unlike rendered Compose
		// archives. The pool dry-run must already select the validated image.
		if err := lifecycle.UpdateImage(normalized.Image); err != nil {
			return fmt.Errorf("select first-install application image: %w", err)
		}
		if err := lifecycle.PrepareFirstInstall(ctx, operatorOptions); err != nil {
			return fmt.Errorf("prepare production PostgreSQL and delivery-pool bootstrap: %w", err)
		}
		if err := lifecycle.InitializeFirstInstall(ctx, composectl.InitOptions{
			AdminEmail: normalized.AdminEmail, Domain: normalized.Domain,
			Environment: normalized.Environment, Image: normalized.Image,
			NoHTTPS: !*config.HTTPS,
		}, operatorOptions); err != nil {
			return fmt.Errorf("initialize LeapView: %w", err)
		}
		if err := lifecycle.ApplyFirstInstall(ctx, operatorOptions); err != nil {
			return fmt.Errorf("apply production delivery-pool bootstrap: %w", err)
		}
		marker, err := installationstate.NewMarker(config, installationstate.PhasePrivate)
		if err != nil {
			return err
		}
		if marker.Generation != generation {
			return errors.New("host installation marker generation differs from staged generation")
		}
		if err := installationstate.WriteMarker(i.paths.Root, marker); err != nil {
			return fmt.Errorf("write private-bootstrap installation marker: %w", err)
		}
		if err := lifecycle.StartFirstInstallBootstrap(ctx); err != nil {
			return fmt.Errorf("start LeapView in private first-install bootstrap: %w", err)
		}
		return nil
	}
	if err := installationstate.VerifyCurrent(i.paths.Root, *installed, normalized.Image); err != nil {
		return err
	}
	if err := lifecycle.Start(ctx); err != nil {
		return fmt.Errorf("start LeapView: %w", err)
	}
	return nil
}

func (i *Installer) prepareDirectories() error {
	for _, path := range []string{i.paths.Root, i.paths.ConfigDir} {
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
