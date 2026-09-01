package composectl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/platform/ociref"
)

const (
	deploymentEnvName    = "deployment.env"
	appEnvName           = "leapview.env"
	credentialsName      = "initial-credentials.json"
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
	Now                     func() time.Time
	Sleep                   func(context.Context, time.Duration) error
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
	now                     func() time.Time
	sleep                   func(context.Context, time.Duration) error
	dockerPlatform          string
	qualificationExecutor   qualificationCommandExecutor
	qualificationContainers qualificationContainerRuntime
	startOverride           func(context.Context) error
	setImageOverride        func(string) error
	isRunningOverride       func(context.Context) (bool, error)
	stopOverride            func(context.Context, int) error
	composeOverride         func(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
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
	return &Controller{
		root: root, dockerBin: dockerBin, stdin: stdin, stdout: stdout,
		stderr: stderr, now: now, sleep: sleep,
		dockerPlatform:          strings.TrimSpace(options.DockerPlatform),
		qualificationExecutor:   executor,
		qualificationContainers: containers,
	}, nil
}

// scoped creates a controller for a qualification root while retaining the
// process dependencies and test seams of the parent controller. Qualification
// runtimes that shell out to Docker need to be rooted at the child directory
// as well; injected runtimes are deliberately retained by identity.
func (c *Controller) scoped(root string, stdout io.Writer) (*Controller, error) {
	if c == nil {
		return nil, fmt.Errorf("controller is required")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("scoped controller root is required")
	}
	if stdout == nil {
		return nil, fmt.Errorf("scoped controller output is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	containers := c.qualificationContainers
	if dockerRuntime, ok := containers.(*dockerCLIQualificationRuntime); ok {
		if dockerRuntime == nil {
			return nil, fmt.Errorf("qualification container runtime is nil")
		}
		containers = newDockerCLIQualificationRuntime(
			absoluteRoot,
			c.dockerBin,
			c.qualificationExecutor,
		)
	}
	return &Controller{
		root:                    absoluteRoot,
		dockerBin:               c.dockerBin,
		stdin:                   c.stdin,
		stdout:                  stdout,
		stderr:                  c.stderr,
		now:                     c.now,
		sleep:                   c.sleep,
		dockerPlatform:          c.dockerPlatform,
		qualificationExecutor:   c.qualificationExecutor,
		qualificationContainers: containers,
		startOverride:           c.startOverride,
		setImageOverride:        c.setImageOverride,
		isRunningOverride:       c.isRunningOverride,
		stopOverride:            c.stopOverride,
		composeOverride:         c.composeOverride,
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
	var originalAppEnvironment []byte
	if appExists {
		originalAppEnvironment, err = os.ReadFile(c.path(appEnvName))
		if err != nil {
			return err
		}
	}
	appEnvironment, err := initializationEnvironment(originalAppEnvironment, options, csrfKey, metricsToken)
	if err != nil {
		return err
	}
	if err := securefs.WritePrivateFileAtomic(c.path(appEnvName), []byte(appEnvironment)); err != nil {
		return err
	}
	cleanupInitialization := func() {
		if appExists {
			_ = securefs.WritePrivateFileAtomic(c.path(appEnvName), originalAppEnvironment)
		} else {
			_ = os.Remove(c.path(appEnvName))
		}
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
		return fmt.Errorf("instance initialization did not deliver credentials; initialization can be retried: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("instance initialization returned empty credentials; initialization can be retried")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	credentials, err := io.ReadAll(tmp)
	if err != nil {
		return err
	}
	if _, err := adminoffline.DecodeInitialCredentials(credentials); err != nil {
		return fmt.Errorf("instance initialization returned invalid credentials; initialization can be retried: %w", err)
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

// initializationEnvironment merges controller-owned defaults into an
// operator-provided application environment. In particular, PostgreSQL URLs,
// role names, and delivery-pool admission identities are never synthesized or
// replaced by Compose initialization. The controller only creates the local
// secrets it owns when they are absent (or still carry the example marker),
// while canonical public-address fields continue to follow init arguments.
func initializationEnvironment(existing []byte, options InitOptions, csrfKey, metricsToken string) (string, error) {
	contents := string(existing)
	values := environmentValues(contents)
	controllerOwned := map[string]string{
		"LEAPVIEW_PRODUCTION":           "1",
		"LEAPVIEW_ENVIRONMENT":          options.Environment,
		"LEAPVIEW_ADDR":                 ":8080",
		"LEAPVIEW_HOME":                 "/var/lib/leapview/home",
		"LEAPVIEW_MANAGED_DATA_BACKEND": "local",
		"LEAPVIEW_MANAGED_DATA_DIR":     "/var/lib/leapview/home/managed-data",
		"LEAPVIEW_LOCAL_AUTH":           "1",
		"LEAPVIEW_COOKIE_SECURE":        "true",
		"LEAPVIEW_TRUST_PROXY_HEADERS":  "true",
	}
	for key, value := range controllerOwned {
		values[key] = value
	}
	// These values are derived from the validated init arguments. They are
	// intentionally refreshed when an operator pre-populates leapview.env.
	values["LEAPVIEW_PUBLIC_URL"] = "https://" + options.Domain
	values["LEAPVIEW_ALLOWED_HOSTS"] = options.Domain
	values["LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL"] = options.AdminEmail
	for key, generated := range map[string]string{
		"LEAPVIEW_CSRF_KEY":             csrfKey,
		"LEAPVIEW_METRICS_BEARER_TOKEN": metricsToken,
	} {
		current := strings.TrimSpace(values[key])
		if current != "" && !strings.Contains(current, "<generated") {
			continue
		}
		values[key] = generated
	}

	// Preserve the operator's original ordering and comments. Missing
	// controller-owned keys are appended in stable order; every pre-existing
	// operator-owned key, including PostgreSQL and delivery settings, remains
	// untouched. Controller-owned runtime and security invariants are replaced
	// with their canonical values above.
	lines := strings.Split(contents, "\n")
	found := make(map[string]bool)
	for index, line := range lines {
		name, _, present := strings.Cut(line, "=")
		if !present {
			continue
		}
		if value, ok := values[name]; ok {
			lines[index] = name + "=" + value
			found[name] = true
		}
	}
	missing := make([]string, 0)
	for name := range values {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	// Split leaves a synthetic trailing line for a final newline. Remove it
	// while appending, then restore the newline below for deterministic files.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, name := range missing {
		if err := validateEnvLineValue(name, values[name]); err != nil {
			return "", err
		}
		lines = append(lines, name+"="+values[name])
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func environmentValues(contents string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(contents, "\n") {
		name, value, present := strings.Cut(line, "=")
		if present && name != "" {
			values[name] = value
		}
	}
	return values
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
