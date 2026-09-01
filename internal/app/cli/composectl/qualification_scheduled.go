package composectl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

const managedContainerStateRoot = "/var/lib/leapview"

// RunScheduledRecoveryQualification is the released Compose execution path.
// It deliberately runs on the managed host: the web container remains
// read-only and never receives the Docker socket required by FAI-514.
func (c *Controller) RunScheduledRecoveryQualification(ctx context.Context) error {
	return c.runScheduledRecoveryQualification(ctx, buildinfo.Current())
}

func (c *Controller) runScheduledRecoveryQualification(ctx context.Context, build buildinfo.Identity) error {
	return c.withLock(func() error {
		environment, err := readQualificationEnvironment(c.path(appEnvName))
		if err != nil {
			return fmt.Errorf("load managed application environment: %w", err)
		}
		deployment, err := readQualificationEnvironment(c.path(deploymentEnvName))
		if err != nil {
			return fmt.Errorf("load managed deployment environment: %w", err)
		}
		environment["LEAPVIEW_IMAGE"] = deployment["LEAPVIEW_IMAGE"]
		cfg, err := config.LoadEnvironment(environment)
		if err != nil {
			return err
		}
		if !cfg.RecoveryQualificationEnabled {
			return fmt.Errorf("scheduled recovery qualification is disabled; run leapviewctl init or complete the managed-host qualification migration")
		}
		if owner := strings.TrimSpace(cfg.RecoveryQualificationExecutionEnvironment); owner != "" && owner != "host" {
			return fmt.Errorf("scheduled recovery qualification requires execution environment host, got %q", owner)
		}
		stateRoot, err := c.managedContainerStateSource(ctx)
		if err != nil {
			return fmt.Errorf("validate scheduled recovery container capability: %w", err)
		}
		if err := translateRecoveryQualificationPaths(&cfg, stateRoot); err != nil {
			return err
		}
		cfg.RecoveryQualificationController = c.path("leapviewctl")
		cfg.RecoveryQualificationBundle = c.root
		cfg.RecoveryQualificationWorkDir = c.path("recovery-qualification-work")
		store, err := platform.Open(ctx, cfg.DBPath())
		if err != nil {
			return fmt.Errorf("open managed recovery ledger: %w", err)
		}
		defer store.Close()
		instanceID, err := store.InstanceID(ctx)
		if err != nil {
			return err
		}
		instanceEnvironment, err := store.InstanceEnvironment(ctx)
		if err != nil {
			return err
		}
		lifecycle, err := app.BuildProductionRecoveryLifecycleWithContainerRuntime(cfg, build, instanceEnvironment, instanceID, c.dockerBin)
		if err != nil {
			return err
		}
		lifecycle = refreshmodule.NewSQLiteRecoveryLifecycle(store.SQLDB(), *lifecycle)
		lifecycle.Clock = scheduledRecoveryClock{now: c.now}
		return lifecycle.RunOnce(ctx)
	})
}

type scheduledRecoveryClock struct {
	now func() time.Time
}

func (clock scheduledRecoveryClock) Now() time.Time {
	return clock.now()
}

func (c *Controller) managedContainerStateSource(ctx context.Context) (string, error) {
	containerID, err := c.containerID(ctx)
	if err != nil {
		return "", err
	}
	if containerID == "" {
		return "", fmt.Errorf("managed LeapView container is not running")
	}
	var output bytes.Buffer
	format := `{{range .Mounts}}{{if eq .Destination "/var/lib/leapview"}}{{.Source}}{{end}}{{end}}`
	if err := c.docker(ctx, nil, &output, c.stderr, "inspect", "--format", format, containerID); err != nil {
		return "", err
	}
	root := strings.TrimSpace(output.String())
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("managed container has no absolute %s storage mount", managedContainerStateRoot)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("managed storage source %s is not an accessible directory", root)
	}
	return filepath.Clean(root), nil
}

func translateRecoveryQualificationPaths(cfg *config.Config, hostStateRoot string) error {
	translate := func(label, value string, optional bool) (string, error) {
		value = strings.TrimSpace(value)
		if optional && value == "" {
			return "", nil
		}
		relative, err := filepath.Rel(managedContainerStateRoot, filepath.Clean(value))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("scheduled recovery %s must be stored under %s", label, managedContainerStateRoot)
		}
		return filepath.Join(hostStateRoot, relative), nil
	}
	var err error
	if cfg.HomeDir, err = translate("home", cfg.HomeDir, false); err != nil {
		return err
	}
	if cfg.ManagedDataDir, err = translate("managed-data directory", cfg.ManagedDataDir, true); err != nil {
		return err
	}
	if cfg.RecoveryQualificationExternalRecoveryPoints, err = translate("external recovery-points evidence", cfg.RecoveryQualificationExternalRecoveryPoints, true); err != nil {
		return err
	}
	if cfg.RecoveryQualificationExternalEvidence, err = translate("external recovery evidence", cfg.RecoveryQualificationExternalEvidence, true); err != nil {
		return err
	}
	return nil
}

func readQualificationEnvironment(path string) (map[string]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for number, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\x00") || strings.ContainsAny(value, "\r\x00") {
			return nil, fmt.Errorf("invalid environment assignment on line %d", number+1)
		}
		values[name] = value
	}
	return values, nil
}
