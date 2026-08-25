package composectl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/ociref"
	platformsecret "github.com/flidai/leapview/internal/platform/security/secret"
)

const (
	deploymentEnvName    = "deployment.env"
	appEnvName           = "leapview.env"
	credentialsName      = "initial-credentials.json"
	rollbackEnvName      = "rollback.env"
	controllerLockName   = ".leapviewctl.lock"
	defaultEnvironment   = "prod"
	defaultHealthChecks  = 120
	publicDomainHelpText = "--domain must be a hostname without a scheme, path, port, wildcard, or credentials"
)

var (
	domainLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type Options struct {
	Root                    string
	DockerBin               string
	Stdin                   io.Reader
	Stdout                  io.Writer
	Stderr                  io.Writer
	DeploymentPayloads      DeploymentPayloadManager
	Now                     func() time.Time
	Sleep                   func(context.Context, time.Duration) error
	TransitionPolicy        *compatibility.Policy
	DockerPlatform          string
	qualificationExecutor   qualificationCommandExecutor
	qualificationContainers qualificationContainerRuntime
}

type Controller struct {
	root                    string
	dockerBin               string
	stdin                   io.Reader
	stdout                  io.Writer
	stderr                  io.Writer
	deploymentPayloads      DeploymentPayloadManager
	now                     func() time.Time
	sleep                   func(context.Context, time.Duration) error
	transitionPolicy        *compatibility.Policy
	dockerPlatform          string
	qualificationExecutor   qualificationCommandExecutor
	qualificationContainers qualificationContainerRuntime
	startOverride           func(context.Context) error
	writePrivateOverride    func(string, []byte) error
	setImageOverride        func(string) error
	isRunningOverride       func(context.Context) (bool, error)
	stopOverride            func(context.Context, int) error
	backupArchiveOverride   func(context.Context, string) error
	restoreArchiveOverride  func(context.Context, string) error
	composeOverride         func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
}

// DeploymentPayloadManager stages host-level controller and deployment assets
// for the same immutable image transaction as application state and runtime.
// Generic Compose installations leave this unset.
type DeploymentPayloadManager interface {
	Prepare(context.Context, string, string) (DeploymentPayloadUpdate, error)
}

type DeploymentPayloadUpdate interface {
	Apply() error
	Rollback() error
	Close() error
}

type InitOptions struct {
	AdminEmail  string
	Domain      string
	Environment string
	Image       string
	NoHTTPS     bool
}

func New(options Options) (*Controller, error) {
	root := strings.TrimSpace(options.Root)
	if root == "" {
		return nil, fmt.Errorf("controller root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dockerBin := strings.TrimSpace(options.DockerBin)
	if dockerBin == "" {
		dockerBin = "docker"
	}
	stdin := options.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := options.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := options.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	executor := options.qualificationExecutor
	if executor == nil {
		executor = osQualificationCommandExecutor{}
	}
	containers := options.qualificationContainers
	if containers == nil {
		containers = newDockerCLIQualificationRuntime(root, dockerBin, executor)
	}
	transitionPolicy := options.TransitionPolicy
	if transitionPolicy == nil {
		transitionPolicy, err = compatibility.EmbeddedPolicy()
		if err != nil {
			return nil, fmt.Errorf("load release-transition policy: %w", err)
		}
	}
	if err := transitionPolicy.Validate(); err != nil {
		return nil, fmt.Errorf("validate release-transition policy: %w", err)
	}
	return &Controller{
		root: root, dockerBin: dockerBin, stdin: stdin, stdout: stdout,
		stderr: stderr, deploymentPayloads: options.DeploymentPayloads, now: now, sleep: sleep,
		transitionPolicy:        transitionPolicy,
		dockerPlatform:          strings.TrimSpace(options.DockerPlatform),
		qualificationExecutor:   executor,
		qualificationContainers: containers,
	}, nil
}

func (c *Controller) Initialize(ctx context.Context, options InitOptions) error {
	var err error
	options, err = NormalizeInitOptions(options)
	if err != nil {
		return err
	}
	if err := c.ensureDeploymentEnvironment(); err != nil {
		return err
	}
	lock, err := instancelock.AcquireNamed(c.root, controllerLockName)
	if err != nil {
		return err
	}
	defer lock.Release()

	appExists, err := nonEmptyRegularFile(c.path(appEnvName))
	if err != nil {
		return err
	}
	credentialsExist, err := nonEmptyRegularFile(c.path(credentialsName))
	if err != nil {
		return err
	}
	if appExists && credentialsExist {
		if err := c.acknowledgeCredentials(ctx); err != nil {
			return err
		}
		_, err := fmt.Fprintln(c.stdout, "initialization acknowledgement completed; run ./leapviewctl start")
		return err
	}
	if appExists {
		if err := c.captureInitialCredentials(ctx); err != nil {
			return err
		}
		_, err := fmt.Fprintln(c.stdout, "initialization completed; run ./leapviewctl start")
		return err
	}
	if credentialsExist {
		return fmt.Errorf("credential file exists without instance configuration; move it aside before retrying")
	}

	if options.Image == "" {
		options.Image, err = envFileValue(c.path(deploymentEnvName), "LEAPVIEW_IMAGE")
		if err != nil {
			return err
		}
	}
	if err := requireDigest(options.Image); err != nil {
		return err
	}
	httpsValue := "1"
	if options.NoHTTPS {
		httpsValue = "0"
	}
	if err := updateEnvFile(c.path(deploymentEnvName), map[string]string{
		"LEAPVIEW_IMAGE": options.Image,
		"CADDY_DOMAIN":   options.Domain,
		"COMPOSE_HTTPS":  httpsValue,
	}); err != nil {
		return err
	}
	caddyImage, err := envFileValue(c.path(deploymentEnvName), "CADDY_IMAGE")
	if err != nil {
		return err
	}
	if err := requireDigest(caddyImage); err != nil {
		return err
	}
	csrfKey, err := randomHex(32)
	if err != nil {
		return err
	}
	metricsToken, err := randomHex(32)
	if err != nil {
		return err
	}
	appEnvironment := fmt.Sprintf("LEAPVIEW_PRODUCTION=1\nLEAPVIEW_ENVIRONMENT=%s\nLEAPVIEW_ADDR=:8080\n", options.Environment) +
		"LEAPVIEW_HOME=/var/lib/leapview/home\nLEAPVIEW_MANAGED_DATA_BACKEND=local\nLEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data\n" +
		"LEAPVIEW_LOCAL_AUTH=1\nLEAPVIEW_COOKIE_SECURE=true\nLEAPVIEW_TRUST_PROXY_HEADERS=true\n" +
		fmt.Sprintf("LEAPVIEW_PUBLIC_URL=https://%s\nLEAPVIEW_ALLOWED_HOSTS=%s\nLEAPVIEW_BOOTSTRAP_ADMIN_EMAIL=%s\n", options.Domain, options.Domain, options.AdminEmail) +
		fmt.Sprintf("LEAPVIEW_CSRF_KEY=%s\nLEAPVIEW_METRICS_BEARER_TOKEN=%s\n", csrfKey, metricsToken)
	if err := securefs.WritePrivateFileAtomic(c.path(appEnvName), []byte(appEnvironment)); err != nil {
		return err
	}
	cleanupInitialization := func() {
		_ = os.Remove(c.path(appEnvName))
		_ = os.Remove(c.path(credentialsName))
	}
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "pull", "leapview"); err != nil {
		cleanupInitialization()
		return fmt.Errorf("initial image pull failed; initialization can be retried: %w", err)
	}
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "run", "--rm", "--no-deps", "leapview", "config", "validate", "--production"); err != nil {
		cleanupInitialization()
		return fmt.Errorf("configuration validation failed; initialization can be retried: %w", err)
	}
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "config", "--quiet"); err != nil {
		cleanupInitialization()
		return fmt.Errorf("Compose configuration is invalid; initialization can be retried: %w", err)
	}
	if err := c.captureInitialCredentials(ctx); err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.stdout, "initialized environment %s; run ./leapviewctl start\n", options.Environment)
	return err
}

// NormalizeInitOptions validates and canonicalizes initialization input without
// touching instance state. Host installers use it before creating any files so
// malformed bootstrap configuration cannot leave a partial installation.
func NormalizeInitOptions(options InitOptions) (InitOptions, error) {
	options.AdminEmail = strings.TrimSpace(options.AdminEmail)
	options.Domain = strings.TrimSpace(options.Domain)
	options.Environment = strings.TrimSpace(options.Environment)
	options.Image = strings.TrimSpace(options.Image)
	if options.AdminEmail == "" {
		return InitOptions{}, fmt.Errorf("init requires --admin-email")
	}
	if options.Domain == "" {
		return InitOptions{}, fmt.Errorf("init requires --domain (the public host, including with an external proxy)")
	}
	if options.Environment == "" {
		options.Environment = defaultEnvironment
	}
	for label, value := range map[string]string{
		"admin email": options.AdminEmail,
		"domain":      options.Domain,
		"environment": options.Environment,
	} {
		if err := validateEnvLineValue(label, value); err != nil {
			return InitOptions{}, err
		}
	}
	domain, err := canonicalPublicDomain(options.Domain)
	if err != nil {
		return InitOptions{}, err
	}
	options.Domain = domain
	if options.Image != "" {
		if err := requireDigest(options.Image); err != nil {
			return InitOptions{}, err
		}
	}
	return options, nil
}

func (c *Controller) Start(ctx context.Context) error {
	return c.withLock(func() error { return c.startUnlocked(ctx) })
}

func (c *Controller) Status(ctx context.Context) error {
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "ps"); err != nil {
		return err
	}
	id, err := c.containerID(ctx)
	if err != nil || id == "" {
		return err
	}
	return c.docker(ctx, nil, c.stdout, c.stderr, "exec", id, "leapview", "healthcheck")
}

func (c *Controller) Logs(ctx context.Context, args []string) error {
	if len(args) == 0 {
		args = []string{"leapview"}
	}
	return c.compose(ctx, nil, c.stdout, c.stderr, append([]string{"logs"}, args...)...)
}

func (c *Controller) FirstLogin() error {
	return c.withLock(func() error {
		path := c.path(credentialsName)
		if err := requireNonEmptyFile(path); err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := c.stdout.Write(contents); err != nil {
			return err
		}
		if len(contents) == 0 || contents[len(contents)-1] != '\n' {
			if _, err := fmt.Fprintln(c.stdout); err != nil {
				return err
			}
		}
		return os.Remove(path)
	})
}

func (c *Controller) Backup(ctx context.Context, requestedName string) error {
	return c.withLock(func() error {
		wasRunning, err := c.isRunning(ctx)
		if err != nil {
			return err
		}
		if wasRunning {
			if err := c.stop(ctx, 120); err != nil {
				return err
			}
		}
		name := strings.TrimSpace(requestedName)
		if name == "" {
			name = "leapview-" + c.timestamp() + ".tar.gz"
		}
		name = filepath.Base(name)
		if name == "." || name == string(filepath.Separator) || name == "" {
			return fmt.Errorf("invalid backup name")
		}
		path := filepath.Join(c.path("backups"), name)
		if err := c.backupArchive(ctx, path); err != nil {
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return fmt.Errorf("backup failed; the previous service state was restored: %w", err)
		}
		if err := writeBackupChecksum(path); err != nil {
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return fmt.Errorf("backup failed; the previous service state was restored: %w", err)
		}
		if wasRunning {
			if err := c.startUnlocked(ctx); err != nil {
				return err
			}
		}
		if hook := strings.TrimSpace(os.Getenv("LEAPVIEWCTL_BACKUP_HOOK")); hook != "" {
			command := exec.CommandContext(ctx, hook, path)
			command.Dir = c.root
			command.Stdin = c.stdin
			command.Stdout = c.stdout
			command.Stderr = c.stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("backup hook: %w", err)
			}
		}
		_, err = fmt.Fprintln(c.stdout, path)
		return err
	})
}

func (c *Controller) Restore(ctx context.Context, requestedArchive string) error {
	archive, err := c.resolveArchive(requestedArchive)
	if err != nil {
		return err
	}
	if err := verifyBackupChecksum(archive); err != nil {
		return err
	}
	return c.withLock(func() error {
		wasRunning, err := c.isRunning(ctx)
		if err != nil {
			return err
		} else if wasRunning {
			if err := c.stop(ctx, 120); err != nil {
				return err
			}
		}
		before := filepath.Join(c.path("backups"), "pre-restore-"+c.timestamp()+".tar.gz")
		if err := c.backupArchive(ctx, before); err != nil {
			return c.restorePreflightFailure(ctx, wasRunning, fmt.Errorf("pre-restore backup failed: %w", err))
		}
		if err := c.restoreArchive(ctx, archive); err != nil {
			reinstateErr := c.restoreArchive(ctx, before)
			stateErr := c.restoreServiceState(ctx, wasRunning)
			return errors.Join(fmt.Errorf("restore failed before health checking: %w", err), reinstateErr, stateErr)
		}
		// A stopped service remains stopped after a successful restore. Running
		// the health check would implicitly start it and violate the pre-op
		// state contract.
		if !wasRunning {
			return nil
		}
		if err := c.startUnlocked(ctx); err != nil {
			stopErr := c.stop(ctx, 30)
			restoreErr := c.restoreArchive(ctx, before)
			stateErr := c.restoreServiceState(ctx, true)
			errs := []error{fmt.Errorf("restored state failed health checks: %w", err)}
			if stopErr != nil {
				errs = append(errs, fmt.Errorf("stop failed service: %w", stopErr))
			}
			if restoreErr != nil {
				errs = append(errs, fmt.Errorf("reinstate previous state: %w", restoreErr))
			}
			if stateErr != nil {
				errs = append(errs, stateErr)
			}
			return errors.Join(errs...)
		}
		return nil
	})
}

func (c *Controller) Upgrade(ctx context.Context, next string) error {
	return c.upgrade(ctx, next, c.transitionPolicy)
}

func (c *Controller) UpgradeWithPolicy(ctx context.Context, next, policyPath string) error {
	policy, _, err := compatibility.LoadPolicy(policyPath)
	if err != nil {
		return err
	}
	return c.upgrade(ctx, next, policy)
}

func (c *Controller) upgrade(ctx context.Context, next string, policy *compatibility.Policy) error {
	next = strings.TrimSpace(next)
	if err := requireDigest(next); err != nil {
		return err
	}
	return c.withLock(func() error {
		current, err := envFileValue(c.path(deploymentEnvName), "LEAPVIEW_IMAGE")
		if err != nil {
			return err
		}
		if err := requireDigest(current); err != nil {
			return err
		}
		if next == current {
			_, err := fmt.Fprintf(c.stdout, "already running %s\n", next)
			return err
		}
		platform, err := c.targetDockerPlatform(ctx)
		if err != nil {
			return err
		}
		decision := policy.EvaluateImages(compatibility.OperationUpgrade, current, next, platform)
		if err := enforceTransitionRequirements(decision); err != nil {
			return err
		}
		payloadUpdate, err := c.prepareDeploymentPayload(ctx, current, next)
		if err != nil {
			return fmt.Errorf("prepare deployment payload: %w", err)
		}
		if payloadUpdate != nil {
			defer payloadUpdate.Close()
		}
		wasRunning, err := c.isRunning(ctx)
		if err != nil {
			return err
		}
		if wasRunning {
			if err := c.stop(ctx, 120); err != nil {
				return err
			}
		}
		markerPath := c.path(rollbackEnvName)
		previousMarker, markerReadErr := os.ReadFile(markerPath)
		markerExisted := markerReadErr == nil
		if markerReadErr != nil && !os.IsNotExist(markerReadErr) {
			return c.restorePreflightFailure(ctx, wasRunning, fmt.Errorf("read rollback marker: %w", markerReadErr))
		}
		restoreMarker := func() error {
			if markerExisted {
				return securefs.WritePrivateFileAtomic(markerPath, previousMarker)
			}
			if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		preflightFailure := func(operationErr error) error {
			return errors.Join(operationErr, restoreMarker(), c.restoreServiceState(ctx, wasRunning))
		}
		checkpoint := filepath.Join(c.path("backups"), "pre-upgrade-"+c.timestamp()+".tar.gz")
		if err := c.backupArchive(ctx, checkpoint); err != nil {
			return preflightFailure(fmt.Errorf("pre-upgrade backup failed: %w", err))
		}
		writePrivate := securefs.WritePrivateFileAtomic
		if c.writePrivateOverride != nil {
			writePrivate = c.writePrivateOverride
		}
		if err := writePrivate(c.path(rollbackEnvName), []byte(fmt.Sprintf("PREVIOUS_IMAGE=%s\nCHECKPOINT=%s\n", current, checkpoint))); err != nil {
			return preflightFailure(fmt.Errorf("write rollback marker: %w", err))
		}
		if err := c.setImage(next); err != nil {
			return preflightFailure(fmt.Errorf("update deployment image: %w", err))
		}
		if err := c.compose(ctx, nil, c.stdout, c.stderr, "pull", "leapview"); err != nil {
			imageErr := c.setImage(current)
			markerErr := restoreMarker()
			stateErr := c.restoreServiceState(ctx, wasRunning)
			if imageErr == nil && markerErr == nil && stateErr == nil {
				return fmt.Errorf("upgrade image pull failed; previous image and service state were restored: %w", err)
			}
			return errors.Join(fmt.Errorf("upgrade image pull failed; previous state restoration was incomplete: %w", err), imageErr, markerErr, stateErr)
		}
		if payloadUpdate != nil {
			if err := payloadUpdate.Apply(); err != nil {
				imageErr := c.setImage(current)
				payloadErr := payloadUpdate.Rollback()
				markerErr := restoreMarker()
				stateErr := c.restoreServiceState(ctx, wasRunning)
				return errors.Join(fmt.Errorf("apply deployment payload: %w", err), imageErr, payloadErr, markerErr, stateErr)
			}
		}
		// A stopped service remains stopped after an upgrade. Running
		// it here would turn a maintenance operation into an implicit start.
		if !wasRunning {
			return nil
		}
		if err := c.startUnlocked(ctx); err != nil {
			// A failed cutover must converge back to the exact pre-operation
			// image, data, and running state. Preserve every failure encountered
			// while attempting that convergence so operators can tell whether the
			// resulting state is known.
			stopErr := c.stop(ctx, 30)
			imageErr := c.setImage(current)
			payloadErr := rollbackDeploymentPayload(payloadUpdate)
			restoreErr := c.restoreArchive(ctx, checkpoint)
			markerErr := restoreMarker()
			stateErr := c.restoreServiceState(ctx, wasRunning)
			errs := []error{fmt.Errorf("upgrade failed (previous image=%s, data checkpoint=%s): %w", current, checkpoint, err)}
			if stopErr != nil {
				errs = append(errs, fmt.Errorf("stop failed service: %w", stopErr))
			}
			if imageErr != nil {
				errs = append(errs, fmt.Errorf("restore previous image %s: %w", current, imageErr))
			}
			if payloadErr != nil {
				errs = append(errs, fmt.Errorf("restore previous deployment payload: %w", payloadErr))
			}
			if restoreErr != nil {
				errs = append(errs, fmt.Errorf("restore previous data from %s: %w", checkpoint, restoreErr))
			}
			if markerErr != nil {
				errs = append(errs, fmt.Errorf("restore rollback marker: %w", markerErr))
			}
			if stateErr != nil {
				errs = append(errs, stateErr)
			}
			if stopErr == nil && imageErr == nil && payloadErr == nil && restoreErr == nil && markerErr == nil && stateErr == nil {
				return fmt.Errorf("upgrade failed; previous image and state were restored: %w", err)
			}
			return errors.Join(errs...)
		}
		return nil
	})
}

// restoreServiceState is deliberately conditional: a stopped service must
// remain stopped, while a service stopped by an operation must be restarted.
func (c *Controller) restoreServiceState(ctx context.Context, wasRunning bool) error {
	if !wasRunning {
		return nil
	}
	if err := c.startUnlocked(ctx); err != nil {
		return fmt.Errorf("restart previous service: %w", err)
	}
	return nil
}

func (c *Controller) restorePreflightFailure(ctx context.Context, wasRunning bool, operationErr error) error {
	return errors.Join(operationErr, c.restoreServiceState(ctx, wasRunning))
}

func (c *Controller) Rollback(ctx context.Context, confirmed bool) error {
	return c.rollback(ctx, confirmed, c.transitionPolicy)
}

func (c *Controller) RollbackWithPolicy(ctx context.Context, confirmed bool, policyPath string) error {
	policy, _, err := compatibility.LoadPolicy(policyPath)
	if err != nil {
		return err
	}
	return c.rollback(ctx, confirmed, policy)
}

func (c *Controller) rollback(ctx context.Context, confirmed bool, policy *compatibility.Policy) error {
	if !confirmed {
		return fmt.Errorf("rollback discards post-upgrade state; pass --confirm")
	}
	if err := requireNonEmptyFile(c.path(rollbackEnvName)); err != nil {
		return err
	}
	return c.withLock(func() error {
		current, err := envFileValue(c.path(deploymentEnvName), "LEAPVIEW_IMAGE")
		if err != nil {
			return err
		}
		if err := requireDigest(current); err != nil {
			return err
		}
		previous, err := envFileValue(c.path(rollbackEnvName), "PREVIOUS_IMAGE")
		if err != nil {
			return err
		}
		if err := requireDigest(previous); err != nil {
			return err
		}
		platform, err := c.targetDockerPlatform(ctx)
		if err != nil {
			return err
		}
		decision := policy.EvaluateImages(compatibility.OperationRollback, current, previous, platform)
		if err := enforceTransitionRequirements(decision); err != nil {
			return err
		}
		checkpoint, err := envFileValue(c.path(rollbackEnvName), "CHECKPOINT")
		if err != nil {
			return err
		}
		if err := requireNonEmptyFile(checkpoint); err != nil {
			return fmt.Errorf("rollback checkpoint is missing: %w", err)
		}
		payloadUpdate, err := c.prepareDeploymentPayload(ctx, current, previous)
		if err != nil {
			return fmt.Errorf("prepare previous deployment payload: %w", err)
		}
		if payloadUpdate != nil {
			defer payloadUpdate.Close()
		}
		wasRunning, err := c.isRunning(ctx)
		if err != nil {
			return err
		}
		if wasRunning {
			if err := c.stop(ctx, 120); err != nil {
				return err
			}
		}
		before := filepath.Join(c.path("backups"), "pre-rollback-"+c.timestamp()+".tar.gz")
		if err := c.backupArchive(ctx, before); err != nil {
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return fmt.Errorf("pre-rollback backup failed; rollback was not started: %w", err)
		}
		if err := c.setImage(previous); err != nil {
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return err
		}
		if err := c.restoreArchive(ctx, checkpoint); err != nil {
			_ = c.setImage(current)
			if restoreErr := c.restoreArchive(ctx, before); restoreErr != nil {
				return errors.Join(fmt.Errorf("rollback failed"), err, fmt.Errorf("reinstate pre-rollback state: %w", restoreErr))
			}
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return fmt.Errorf("rollback failed; pre-rollback image and state were reinstated: %w", err)
		}
		if payloadUpdate != nil {
			if err := payloadUpdate.Apply(); err != nil {
				_ = c.setImage(current)
				payloadErr := payloadUpdate.Rollback()
				restoreErr := c.restoreArchive(ctx, before)
				stateErr := c.restoreServiceState(ctx, wasRunning)
				return errors.Join(fmt.Errorf("rollback deployment payload failed: %w", err), payloadErr, restoreErr, stateErr)
			}
		}
		if err := c.startUnlocked(ctx); err != nil {
			_ = c.stop(ctx, 30)
			_ = c.setImage(current)
			payloadErr := rollbackDeploymentPayload(payloadUpdate)
			if restoreErr := c.restoreArchive(ctx, before); restoreErr != nil {
				return errors.Join(fmt.Errorf("rollback health check failed"), err, payloadErr, fmt.Errorf("reinstate pre-rollback state: %w", restoreErr))
			}
			if wasRunning {
				_ = c.startUnlocked(ctx)
			}
			return errors.Join(fmt.Errorf("rollback failed health checks; pre-rollback image and state were reinstated: %w", err), payloadErr)
		}
		return nil
	})
}

func enforceTransitionRequirements(decision compatibility.Decision) error {
	if err := decision.Err(); err != nil {
		return err
	}
	for _, required := range []string{
		compatibility.RequirementBackupBeforeMutation,
		compatibility.RequirementStoppedInstance,
	} {
		found := false
		for _, requirement := range decision.Requirements {
			if requirement == required {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("allowed %s transition omits enforced requirement %q", decision.Operation, required)
		}
	}
	return nil
}

func (c *Controller) targetDockerPlatform(ctx context.Context) (string, error) {
	if platform := strings.TrimSpace(c.dockerPlatform); platform != "" {
		return platform, nil
	}
	if platform := strings.TrimSpace(os.Getenv("DOCKER_DEFAULT_PLATFORM")); platform != "" {
		return platform, nil
	}
	var output bytes.Buffer
	command := exec.CommandContext(ctx, c.dockerBin, "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	command.Stdout = &output
	command.Stderr = c.stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("resolve Docker target platform: %w", err)
	}
	platform := strings.TrimSpace(output.String())
	if platform == "" || strings.ContainsAny(platform, " \t\r\n") {
		return "", fmt.Errorf("Docker returned invalid target platform %q", platform)
	}
	return platform, nil
}

func (c *Controller) prepareDeploymentPayload(ctx context.Context, current, next string) (DeploymentPayloadUpdate, error) {
	if c.deploymentPayloads == nil {
		return nil, nil
	}
	return c.deploymentPayloads.Prepare(ctx, current, next)
}

func rollbackDeploymentPayload(update DeploymentPayloadUpdate) error {
	if update == nil {
		return nil
	}
	return update.Rollback()
}

func (c *Controller) captureInitialCredentials(ctx context.Context) error {
	path := c.path(credentialsName)
	tmp, err := os.CreateTemp(c.root, ".initial-credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := c.compose(ctx, nil, tmp, c.stderr, "run", "--rm", "--no-deps", "leapview", "admin", "initialize", "--format", "json"); err != nil {
		return fmt.Errorf("offline initialization did not deliver credentials; initialization can be retried: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("offline initialization returned empty credentials; initialization can be retried")
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	if err := syncDirectory(c.root); err != nil {
		return err
	}
	if err := c.acknowledgeCredentials(ctx); err != nil {
		return fmt.Errorf("credentials were saved but acknowledgement failed; rerun init to complete initialization: %w", err)
	}
	return nil
}

func (c *Controller) acknowledgeCredentials(ctx context.Context) error {
	return c.compose(ctx, nil, c.stdout, c.stderr, "run", "--rm", "--no-deps", "leapview", "admin", "initialize", "--acknowledge-credentials")
}

func (c *Controller) backupArchive(ctx context.Context, path string) error {
	if c.backupArchiveOverride != nil {
		return c.backupArchiveOverride(ctx, path)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("backup path already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	if err := removeInterruptedBackupArchives(directory); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(directory, ".leapview-backup-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := c.compose(ctx, nil, tmp, c.stderr, "run", "--rm", "-T", "--no-deps", "leapview", "admin", "backup", "--out", "-"); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("backup command returned an empty archive")
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(directory)
}

func removeInterruptedBackupArchives(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".leapview-backup-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return fmt.Errorf("remove interrupted backup archive %q: %w", name, err)
		}
	}
	return nil
}

func (c *Controller) restoreArchive(ctx context.Context, archive string) error {
	if c.restoreArchiveOverride != nil {
		return c.restoreArchiveOverride(ctx, archive)
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	return c.compose(ctx, file, c.stdout, c.stderr, "run", "--rm", "-T", "--no-deps", "leapview", "admin", "restore", "--from", "-", "--current-out", "-", "--confirm")
}

func (c *Controller) startUnlocked(ctx context.Context) error {
	if c.startOverride != nil {
		return c.startOverride(ctx)
	}
	if err := c.compose(ctx, nil, c.stdout, c.stderr, "up", "-d"); err != nil {
		return err
	}
	if err := c.waitHealthy(ctx); err != nil {
		return fmt.Errorf("LeapView did not become healthy: %w", err)
	}
	return nil
}

func (c *Controller) waitHealthy(ctx context.Context) error {
	id, err := c.containerID(ctx)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("application container is missing")
	}
	for attempt := 0; attempt < defaultHealthChecks; attempt++ {
		var output bytes.Buffer
		err := c.docker(ctx, nil, &output, c.stderr, "inspect", "-f", "{{.State.Health.Status}}", id)
		status := strings.TrimSpace(output.String())
		if err == nil && status == "healthy" {
			return nil
		}
		if status == "unhealthy" {
			break
		}
		if err := c.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	_ = c.compose(ctx, nil, c.stderr, c.stderr, "logs", "--tail=100", "leapview")
	return fmt.Errorf("application container is unhealthy")
}

func (c *Controller) stop(ctx context.Context, seconds int) error {
	if c.stopOverride != nil {
		return c.stopOverride(ctx, seconds)
	}
	return c.compose(ctx, nil, c.stdout, c.stderr, "stop", "-t", fmt.Sprintf("%d", seconds), "leapview")
}

func (c *Controller) isRunning(ctx context.Context) (bool, error) {
	if c.isRunningOverride != nil {
		return c.isRunningOverride(ctx)
	}
	id, err := c.containerID(ctx)
	if err != nil || id == "" {
		return false, err
	}
	var output bytes.Buffer
	if err := c.docker(ctx, nil, &output, c.stderr, "inspect", "-f", "{{.State.Running}}", id); err != nil {
		return false, err
	}
	return strings.TrimSpace(output.String()) == "true", nil
}

func (c *Controller) containerID(ctx context.Context) (string, error) {
	var output bytes.Buffer
	if err := c.compose(ctx, nil, &output, c.stderr, "ps", "-q", "leapview"); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func (c *Controller) compose(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if c.composeOverride != nil {
		return c.composeOverride(ctx, stdin, stdout, stderr, args...)
	}
	if err := requireNonEmptyFile(c.path(deploymentEnvName)); err != nil {
		return err
	}
	https, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_HTTPS")
	if err != nil {
		return err
	}
	commandArgs := []string{"compose", "--project-directory", c.root, "--env-file", c.path(deploymentEnvName), "-f", c.path("compose.yaml")}
	if https == "1" {
		commandArgs = append(commandArgs, "-f", c.path("compose.https.yaml"))
	}
	commandArgs = append(commandArgs, args...)
	return c.docker(ctx, stdin, stdout, stderr, commandArgs...)
}

func (c *Controller) docker(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, c.dockerBin, args...)
	command.Dir = c.root
	command.Env = os.Environ()
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", c.dockerBin, strings.Join(args, " "), err)
	}
	return nil
}

func (c *Controller) setImage(image string) error {
	if c.setImageOverride != nil {
		return c.setImageOverride(image)
	}
	return updateEnvFile(c.path(deploymentEnvName), map[string]string{"LEAPVIEW_IMAGE": image})
}

func (c *Controller) resolveArchive(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", fmt.Errorf("restore requires an archive")
	}
	if info, err := os.Stat(requested); err == nil && info.Mode().IsRegular() {
		return filepath.Abs(requested)
	}
	candidate := filepath.Join(c.path("backups"), filepath.Base(requested))
	if err := requireNonEmptyFile(candidate); err != nil {
		return "", fmt.Errorf("archive not found: %s", requested)
	}
	return candidate, nil
}

func (c *Controller) withLock(operation func() error) error {
	lock, err := instancelock.AcquireNamed(c.root, controllerLockName)
	if err != nil {
		return err
	}
	defer lock.Release()
	return operation()
}

func (c *Controller) ensureDeploymentEnvironment() error {
	path := c.path(deploymentEnvName)
	exists, err := nonEmptyRegularFile(path)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	contents, err := os.ReadFile(c.path("deployment.env.example"))
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, contents)
}

func (c *Controller) timestamp() string {
	return fmt.Sprintf("%s-%d", c.now().UTC().Format("20060102T150405Z"), os.Getpid())
}

func (c *Controller) path(name string) string {
	return filepath.Join(c.root, name)
}

func requireDigest(value string) error {
	if err := ociref.ValidateImmutable(value); err != nil {
		return fmt.Errorf("image must be pinned by digest: %w", err)
	}
	return nil
}

func validateEnvLineValue(label, value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be a single environment-file value", label)
	}
	return nil
}

func canonicalPublicDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if domain == "" || len(domain) > 253 {
		return "", fmt.Errorf("%s: %q", publicDomainHelpText, raw)
	}
	for _, label := range strings.Split(domain, ".") {
		if !domainLabelPattern.MatchString(label) {
			return "", fmt.Errorf("%s: %q", publicDomainHelpText, raw)
		}
	}
	return domain, nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func envFileValue(path, key string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found && name == key {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s is missing %s", path, key)
}

func updateEnvFile(path string, replacements map[string]string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(replacements))
	lines := strings.Split(string(contents), "\n")
	for index, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if !present {
			continue
		}
		if value, replace := replacements[name]; replace {
			lines[index] = name + "=" + value
			found[name] = true
		}
	}
	for name := range replacements {
		if !found[name] {
			return fmt.Errorf("%s is missing %s", path, name)
		}
	}
	return securefs.WritePrivateFileAtomic(path, []byte(strings.Join(lines, "\n")))
}

func writeBackupChecksum(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return securefs.WritePrivateFileAtomic(path+".sha256", []byte(hex.EncodeToString(hash.Sum(nil))+"\n"))
}

func verifyBackupChecksum(path string) error {
	checksumPath := path + ".sha256"
	exists, err := nonEmptyRegularFile(checksumPath)
	if err != nil {
		return fmt.Errorf("validate backup checksum %s: %w", checksumPath, err)
	}
	if !exists {
		return fmt.Errorf("backup checksum is missing or empty: %s", checksumPath)
	}
	contents, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read backup checksum %s: %w", checksumPath, err)
	}
	expectedText := strings.TrimSpace(string(contents))
	if len(expectedText) != sha256.Size*2 || strings.ContainsAny(expectedText, " \t\r\n") {
		return fmt.Errorf("invalid backup checksum in %s", checksumPath)
	}
	if _, err := hex.DecodeString(expectedText); err != nil {
		return fmt.Errorf("invalid backup checksum in %s: %w", checksumPath, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !platformsecret.Equal(expectedText, hex.EncodeToString(hash.Sum(nil))) {
		return fmt.Errorf("backup checksum mismatch for %s", path)
	}
	return nil
}

func requireNonEmptyFile(path string) error {
	exists, err := nonEmptyRegularFile(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("required file is missing or empty: %s", path)
	}
	return nil
}

func nonEmptyRegularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("path is not a regular file: %s", path)
	}
	return info.Size() > 0, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
