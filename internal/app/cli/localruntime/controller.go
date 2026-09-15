package localruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"golang.org/x/mod/semver"
)

const (
	statusApplying   = "applying"
	statusIncomplete = "incomplete"
	statusApplied    = "applied"
	phaseIntent      = "intent"
	phasePostgres    = "postgres"
	phaseInstance    = "instance"
	phasePool        = "pool"
	phaseApplication = "application"
	phaseProject     = "project"
	phaseSession     = "session"
	phaseReady       = "ready"
	phaseReset       = "reset"

	resetStagePlanned          = "planned"
	resetStageResourcesRemoved = "resources_removed"
	resetStageSessionRemoved   = "session_removed"
	controllerLock             = ".local-runtime.lock"
	maxPortAttempts            = 5
	defaultChecks              = 120
)

type Controller struct {
	checkoutRoot                string
	packageRoot                 string
	stateRoot                   string
	endpoint                    Endpoint
	resolveProjectAuthority     func() (ProjectAuthority, error)
	identity                    buildinfo.Identity
	runner                      Runner
	httpClient                  *http.Client
	establishSessions           func(context.Context, SessionRequest) (SessionResult, error)
	resetSessions               func(context.Context, SessionRequest) error
	stdout                      io.Writer
	sleep                       func(context.Context, time.Duration) error
	now                         func() time.Time
	attachmentHeartbeatInterval time.Duration
	attachmentStaleAfter        time.Duration
}

type osRunner struct{ dockerBin string }

func (runner osRunner) Run(ctx context.Context, environment []string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, runner.dockerBin, arguments...)
	command.Env = environment
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("Docker command failed: %w", err)
		}
		return nil, fmt.Errorf("Docker command failed: %s", detail)
	}
	return output, nil
}

func New(options Options) (*Controller, error) {
	if options.Endpoint == nil {
		return nil, errors.New("verified local Docker endpoint is required")
	}
	if options.EstablishSessions == nil {
		return nil, errors.New("local CLI/browser session authority is required")
	}
	if options.ResolveProjectAuthority == nil {
		return nil, errors.New("issuer-owned Project identity authority is required")
	}
	checkoutRoot := strings.TrimSpace(options.CheckoutRoot)
	if checkoutRoot == "" {
		var err error
		checkoutRoot, err = os.Getwd()
		if err != nil {
			return nil, err
		}
		checkoutRoot, err = discoverCheckoutRoot(checkoutRoot)
		if err != nil {
			return nil, err
		}
	}
	packageRoot := strings.TrimSpace(options.RuntimePackage)
	if packageRoot == "" {
		var err error
		packageRoot, err = defaultRuntimePackageRoot()
		if err != nil {
			return nil, err
		}
	}
	stateRoot := strings.TrimSpace(options.StateRoot)
	if stateRoot == "" {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("locate local runtime state: %w", err)
		}
		stateRoot = filepath.Join(configRoot, "leapview", "dev")
	}
	dockerBin := strings.TrimSpace(options.DockerBin)
	if dockerBin == "" {
		dockerBin = "docker"
	}
	identity := options.BuildIdentity
	if identity.Version == "" {
		identity = buildinfo.Current()
	}
	runner := options.Runner
	if runner == nil {
		runner = osRunner{dockerBin: dockerBin}
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	stdout := options.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	heartbeatInterval := options.AttachmentHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultAttachmentHeartbeatInterval
	}
	staleAfter := options.AttachmentStaleAfter
	if staleAfter <= heartbeatInterval {
		staleAfter = 3 * heartbeatInterval
		if staleAfter < defaultAttachmentStaleAfter {
			staleAfter = defaultAttachmentStaleAfter
		}
	}
	return &Controller{
		checkoutRoot: checkoutRoot, packageRoot: packageRoot, stateRoot: stateRoot,
		endpoint: options.Endpoint, resolveProjectAuthority: options.ResolveProjectAuthority, identity: identity,
		runner: runner, httpClient: httpClient, establishSessions: options.EstablishSessions,
		resetSessions: options.ResetSessions, stdout: stdout, sleep: sleep, now: now,
		attachmentHeartbeatInterval: heartbeatInterval, attachmentStaleAfter: staleAfter,
	}, nil
}

func defaultRuntimePackageRoot() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate released local runtime package: %w", err)
	}
	return installedRuntimePackageRoot(executable)
}

func installedRuntimePackageRoot(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve installed authoring executable: %w", err)
	}
	return filepath.Join(filepath.Dir(resolved), "local-runtime"), nil
}

// Start validates immutable intent before mutation, then reconciles every
// persisted bootstrap boundary. A failed phase remains explicitly incomplete.
func (controller *Controller) Start(ctx context.Context) (result State, err error) {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return State{}, fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	canonicalCheckout, checkoutID, err := checkoutIdentity(controller.checkoutRoot)
	if err != nil {
		return State{}, err
	}
	manifest, manifestDigest, err := loadManifest(controller.packageRoot, controller.identity)
	if err != nil {
		return State{}, err
	}
	packageRoot, err := canonicalDirectory(controller.packageRoot)
	if err != nil {
		return State{}, err
	}
	controller.packageRoot = packageRoot
	root := stateDirectory(controller.stateRoot, checkoutID)
	if err := securefs.EnsurePrivateDir(root); err != nil {
		return State{}, err
	}
	lock, err := acquireLifecycleLock(ctx, root)
	if err != nil {
		return State{}, err
	}
	defer lock.Release()

	statePath := filepath.Join(root, stateFileName)
	envPath := filepath.Join(root, runtimeEnvFileName)
	state, exists, err := loadState(statePath)
	if err != nil {
		return State{}, err
	}
	if exists {
		if err := validateRetainedIntent(state, canonicalCheckout, checkoutID, manifestDigest, controller.endpoint, controller.identity); err != nil {
			return State{}, err
		}
		if state.Reset != nil {
			return State{}, ErrResetInProgress
		}
		values, err := readEnvironment(envPath)
		if err != nil {
			return State{}, fmt.Errorf("retained local runtime secrets are unavailable; refusing to replace them: %w", err)
		}
		if state.Phase == phaseIntent {
			values["LEAPVIEW_LOCAL_APP_PORT"] = strconv.Itoa(state.Network.AppPort)
			if err := writeEnvironment(envPath, values); err != nil {
				return State{}, err
			}
		}
		if err := validateRetainedEnvironment(state, manifest, values); err != nil {
			return State{}, err
		}
	} else {
		state, err = controller.newIntent(canonicalCheckout, checkoutID, manifestDigest)
		if err != nil {
			return State{}, err
		}
		values, err := initialEnvironment(state, manifest)
		if err != nil {
			return State{}, err
		}
		if err := writeEnvironment(envPath, values); err != nil {
			return State{}, err
		}
		if err := saveState(statePath, state); err != nil {
			return State{}, err
		}
	}
	if state.AttachmentRegistryVersion == 0 {
		if err := controller.initializeAttachmentRegistry(ctx, root, statePath, &state); err != nil {
			return State{}, err
		}
	}
	state.Status = statusApplying
	state.LastError = nil
	if err := saveState(statePath, state); err != nil {
		return State{}, err
	}
	phase := state.Phase
	defer func() {
		if err == nil {
			return
		}
		state.Status = statusIncomplete
		state.LastError = &failure{Phase: phase, Code: "bootstrap_failed"}
		_ = saveState(statePath, state)
		result = state
	}()

	if err = controller.requireComposeVersion(ctx, manifest.ComposeMinimumVersion); err != nil {
		return state, err
	}
	if err = controller.verifyResourceOwnership(ctx, state, !exists); err != nil {
		return state, err
	}
	if err = controller.compose(ctx, envPath, nil, "config", "--quiet"); err != nil {
		return state, fmt.Errorf("validate local runtime Compose package: %w", err)
	}
	if phase = phasePostgres; phaseBefore(state.Phase, phasePostgres) {
		if err = controller.compose(ctx, envPath, nil, "pull", "postgres", "leapview"); err != nil {
			return state, fmt.Errorf("pull local runtime images (verify registry and network access, then rerun leapview dev): %w", err)
		}
		if err = controller.startPostgres(ctx, statePath, envPath, &state); err != nil {
			return state, err
		}
		state.Phase = phasePostgres
		if err = saveState(statePath, state); err != nil {
			return state, err
		}
	} else if err = controller.startPostgres(ctx, statePath, envPath, &state); err != nil {
		return state, fmt.Errorf("reconcile local PostgreSQL: %w", err)
	}

	if phase = phaseInstance; phaseBefore(state.Phase, phaseInstance) {
		if err = controller.initializeInstance(ctx, root, envPath); err != nil {
			return state, err
		}
		state.Phase = phaseInstance
		if err = saveState(statePath, state); err != nil {
			return state, err
		}
	}

	if phase = phaseProject; phaseBefore(state.Phase, phaseProject) {
		if err = controller.claimProject(ctx, root, &state); err != nil {
			return state, err
		}
		state.Phase = phaseProject
		if err = saveState(statePath, state); err != nil {
			return state, err
		}
	}

	if phase = phasePool; phaseBefore(state.Phase, phasePool) {
		if err = controller.qualifyAndAdmitPool(ctx, root, statePath, envPath, &state); err != nil {
			return state, err
		}
		state.Phase = phasePool
		if err = saveState(statePath, state); err != nil {
			return state, err
		}
	}

	if phase = phaseApplication; phaseBefore(state.Phase, phaseApplication) {
		if err = controller.compose(ctx, envPath, nil, "up", "-d", "leapview"); err != nil {
			return state, fmt.Errorf("start local LeapView: %w", err)
		}
		if err = controller.waitReady(ctx, state.Network.URL+"/readyz"); err != nil {
			return state, err
		}
		state.Phase = phaseApplication
		if err = saveState(statePath, state); err != nil {
			return state, err
		}
	} else if err = controller.compose(ctx, envPath, nil, "up", "-d", "leapview"); err != nil {
		return state, fmt.Errorf("reconcile local LeapView: %w", err)
	}

	phase = phaseSession
	credentialsPath := filepath.Join(root, credentialsFileName)
	fmt.Fprintf(controller.stdout, "Use the private initial credentials at %s if the browser requests local sign-in.\n", credentialsPath)
	targetName := sessionTargetName(state)
	session, sessionErr := controller.establishSessions(ctx, SessionRequest{
		TargetName: targetName, Origin: state.Network.URL, InstanceID: state.Authority.InstanceID,
		Environment: state.Authority.Environment, ProjectID: state.Authority.ProjectUID,
	})
	if sessionErr != nil {
		return state, fmt.Errorf("establish local CLI/browser sessions: %w", sessionErr)
	}
	if session.TargetName != targetName {
		return state, errors.New("local session authority returned a different target identity")
	}
	if strings.TrimSpace(session.SessionID) == "" || session.SessionID != strings.TrimSpace(session.SessionID) {
		return state, errors.New("local session authority returned an invalid session identity")
	}
	state.Session = sessionID{TargetName: session.TargetName, SessionID: session.SessionID}
	if phaseBefore(state.Phase, phaseSession) {
		state.Phase = phaseSession
	}
	if err = saveState(statePath, state); err != nil {
		return state, err
	}

	phase = phaseReady
	if err = controller.waitReady(ctx, state.Network.URL+"/readyz"); err != nil {
		return state, err
	}
	state.Phase, state.Status, state.LastError = phaseReady, statusApplied, nil
	if err = saveState(statePath, state); err != nil {
		return state, err
	}
	fmt.Fprintf(controller.stdout, "LeapView local development is ready at %s\n", state.Network.URL)
	fmt.Fprintf(controller.stdout, "Local authoring target: %s\n", state.Session.TargetName)
	return state, nil
}

func (controller *Controller) newIntent(canonicalCheckout, checkoutID, manifestDigest string) (State, error) {
	port, err := availablePort()
	if err != nil {
		return State{}, err
	}
	ownerID, err := randomToken("lvowner_", 18)
	if err != nil {
		return State{}, err
	}
	authority, err := controller.resolveProjectAuthority()
	if err != nil {
		return State{}, err
	}
	if _, err := projectgraph.NewResourceID(authority.IssuerID); err != nil {
		return State{}, fmt.Errorf("issuer-owned Project authority returned invalid issuer identity: %w", err)
	}
	if _, err := projectgraph.NewResourceID(authority.ProjectUID); err != nil {
		return State{}, fmt.Errorf("issuer-owned Project authority returned invalid ProjectUID: %w", err)
	}
	shortID := strings.TrimPrefix(checkoutID, "sha256:")
	return State{
		SchemaVersion: stateSchemaVersion, Status: statusApplying, Phase: phaseIntent,
		OperationID: uuid.NewString(),
		Checkout:    checkout{CanonicalRoot: canonicalCheckout, ID: checkoutID},
		Runtime:     runtimeID{OwnerID: ownerID, ComposeProject: "leapview-" + shortID[:20], ManifestDigest: manifestDigest, Version: controller.identity.Version, Revision: controller.identity.Revision},
		Endpoint:    endpointID{Host: controller.endpoint.Host(), ServerID: controller.endpoint.ServerID(), Fingerprint: controller.endpoint.Fingerprint()},
		Network:     networkID{AppPort: port, URL: "http://127.0.0.1:" + strconv.Itoa(port)},
		Authority:   authorityID{Environment: "dev", IssuerID: authority.IssuerID, ProjectUID: authority.ProjectUID},
	}, nil
}

func validateRetainedIntent(state State, root, checkoutID, manifestDigest string, endpoint Endpoint, identity buildinfo.Identity) error {
	validPhase := map[string]bool{phaseIntent: true, phasePostgres: true, phaseInstance: true, phasePool: true, phaseApplication: true, phaseProject: true, phaseSession: true, phaseReady: true, phaseReset: true}
	validStatus := map[string]bool{statusApplying: true, statusIncomplete: true, statusApplied: true}
	if !validPhase[state.Phase] || !validStatus[state.Status] ||
		(state.Status == statusApplied && state.Phase != phaseReady) ||
		(state.Phase == phaseReset && state.Status == statusApplied) {
		return errors.New("retained local runtime progress state is invalid; refusing guessed recovery")
	}
	if err := validateResetState(state); err != nil {
		return err
	}
	if state.AttachmentRegistryVersion != attachmentSchemaVersion &&
		!(state.AttachmentRegistryVersion == 0 && state.Phase == phaseIntent && state.Status != statusApplied) {
		return errors.New("retained local attachment registry intent is invalid; refusing guessed recovery")
	}
	if state.Checkout.CanonicalRoot != root || state.Checkout.ID != checkoutID {
		return errors.New("retained local runtime belongs to a different canonical checkout")
	}
	if state.Runtime.ManifestDigest != manifestDigest || state.Runtime.Version != identity.Version || state.Runtime.Revision != identity.Revision {
		return errors.New("retained local runtime package identity differs; explicit upgrade is required")
	}
	if state.Endpoint.Host != endpoint.Host() || state.Endpoint.ServerID != endpoint.ServerID() || state.Endpoint.Fingerprint != endpoint.Fingerprint() {
		return errors.New("retained local runtime belongs to a different Docker endpoint; refusing mutation")
	}
	if state.Runtime.ComposeProject == "" || state.Runtime.OwnerID == "" || state.Network.AppPort < 1 || state.Network.AppPort > 65535 || state.Authority.Environment != "dev" || state.Authority.IssuerID == "" || state.Authority.ProjectUID == "" {
		return errors.New("retained local runtime state is incomplete; refusing guessed recovery")
	}
	if state.Network.URL != "http://127.0.0.1:"+strconv.Itoa(state.Network.AppPort) {
		return errors.New("retained local runtime URL is not the expected loopback endpoint")
	}
	if _, err := uuid.Parse(state.OperationID); err != nil {
		return errors.New("retained local runtime operation identity is invalid")
	}
	if state.Status == statusApplied && !phaseBefore(state.Phase, phaseSession) {
		if state.Session.TargetName != sessionTargetName(state) {
			return errors.New("retained local CLI session target disagrees with checkout identity")
		}
	}
	return nil
}

func validateResetState(state State) error {
	if state.Phase != phaseReset {
		if state.Reset != nil {
			return errors.New("retained local reset progress exists outside the reset phase; refusing guessed recovery")
		}
		return nil
	}
	if state.Reset == nil {
		return errors.New("retained local reset phase has no progress descriptor; refusing guessed recovery")
	}
	validStage := map[string]bool{
		resetStagePlanned:          true,
		resetStageResourcesRemoved: true,
		resetStageSessionRemoved:   true,
	}
	if !validStage[state.Reset.Stage] {
		return errors.New("retained local reset stage is invalid; refusing guessed recovery")
	}
	previous := ""
	for _, resource := range state.Reset.Resources {
		if resource.Kind != "container" && resource.Kind != "volume" && resource.Kind != "network" {
			return errors.New("retained local reset resource kind is invalid; refusing guessed recovery")
		}
		if resource.ID == "" || strings.TrimSpace(resource.ID) != resource.ID || strings.ContainsAny(resource.ID, "\x00\r\n") {
			return errors.New("retained local reset resource identity is invalid; refusing guessed recovery")
		}
		key := resource.Kind + "\x00" + resource.ID
		if previous != "" && key <= previous {
			return errors.New("retained local reset resources are not a unique canonical set; refusing guessed recovery")
		}
		previous = key
	}
	return nil
}

func sessionTargetName(state State) string {
	return "local-" + strings.TrimPrefix(state.Checkout.ID, "sha256:")[:20]
}

func phaseBefore(current, wanted string) bool {
	order := map[string]int{phaseIntent: 0, phasePostgres: 1, phaseInstance: 2, phaseProject: 3, phasePool: 4, phaseApplication: 5, phaseSession: 6, phaseReady: 7}
	currentOrder, ok := order[current]
	if !ok {
		return true
	}
	return currentOrder < order[wanted]
}

func (controller *Controller) compose(ctx context.Context, envPath string, extraEnvironment []string, arguments ...string) error {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	state, _, err := loadState(filepath.Join(filepath.Dir(envPath), stateFileName))
	if err != nil {
		return err
	}
	base := []string{"compose", "--project-name", state.Runtime.ComposeProject, "--project-directory", controller.packageRoot, "--env-file", envPath, "--file", filepath.Join(controller.packageRoot, composeFileName)}
	base = append(base, arguments...)
	environment := controller.processEnvironment(extraEnvironment)
	_, err = controller.runner.Run(ctx, environment, controller.endpoint.DockerArguments(base...)...)
	return err
}

func (controller *Controller) composeOutput(ctx context.Context, envPath string, extraEnvironment []string, arguments ...string) ([]byte, error) {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return nil, fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	state, _, err := loadState(filepath.Join(filepath.Dir(envPath), stateFileName))
	if err != nil {
		return nil, err
	}
	base := []string{"compose", "--project-name", state.Runtime.ComposeProject, "--project-directory", controller.packageRoot, "--env-file", envPath, "--file", filepath.Join(controller.packageRoot, composeFileName)}
	base = append(base, arguments...)
	environment := controller.processEnvironment(extraEnvironment)
	return controller.runner.Run(ctx, environment, controller.endpoint.DockerArguments(base...)...)
}

func (controller *Controller) requireComposeVersion(ctx context.Context, minimum string) error {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	output, err := controller.runner.Run(ctx, controller.processEnvironment(nil), controller.endpoint.DockerArguments("compose", "version", "--short")...)
	if err != nil {
		return fmt.Errorf("read Docker Compose version (install the Docker Compose plugin and rerun leapview dev): %w", err)
	}
	actual := strings.TrimPrefix(strings.TrimSpace(string(output)), "v")
	if !semver.IsValid("v"+actual) || semver.Compare("v"+actual, "v"+minimum) < 0 {
		return fmt.Errorf("Docker Compose %s or newer is required (found %q); upgrade Compose and rerun leapview dev", minimum, strings.TrimSpace(string(output)))
	}
	return nil
}

func (controller *Controller) verifyResourceOwnership(ctx context.Context, state State, requireEmpty bool) error {
	_, err := controller.ownedResources(ctx, state, requireEmpty)
	return err
}

func (controller *Controller) ownedResources(ctx context.Context, state State, requireEmpty bool) ([]OwnedResource, error) {
	var owned []OwnedResource
	for _, resource := range []struct {
		kind        string
		list        []string
		inspectPath string
		format      string
	}{
		{kind: "container", list: []string{"container", "ls", "--all"}, inspectPath: ".Config.Labels", format: "{{.ID}}"},
		{kind: "volume", list: []string{"volume", "ls"}, inspectPath: ".Labels", format: "{{.Name}}"},
		{kind: "network", list: []string{"network", "ls"}, inspectPath: ".Labels", format: "{{.ID}}"},
	} {
		arguments := append(resource.list, "--filter", "label=com.docker.compose.project="+state.Runtime.ComposeProject, "--format", resource.format)
		output, err := controller.dockerOutput(ctx, arguments...)
		if err != nil {
			return nil, fmt.Errorf("list local runtime %ss: %w", resource.kind, err)
		}
		ids := strings.Fields(string(output))
		if requireEmpty && len(ids) > 0 {
			return nil, fmt.Errorf("local runtime state is missing but Docker %ss already use project %q; refusing adoption", resource.kind, state.Runtime.ComposeProject)
		}
		for _, id := range ids {
			encoded, err := controller.dockerOutput(ctx, resource.kind, "inspect", "--format", "{{json "+resource.inspectPath+"}}", id)
			if err != nil {
				return nil, fmt.Errorf("inspect local runtime %s ownership: %w", resource.kind, err)
			}
			var labels map[string]string
			if err := json.Unmarshal(bytes.TrimSpace(encoded), &labels); err != nil {
				return nil, fmt.Errorf("decode local runtime %s ownership: %w", resource.kind, err)
			}
			if labels["io.leapview.local-runtime"] != "true" || labels["io.leapview.local-runtime.schema"] != "1" || labels["io.leapview.local-runtime.checkout"] != state.Checkout.ID || labels["io.leapview.local-runtime.owner"] != state.Runtime.OwnerID {
				return nil, fmt.Errorf("Docker %s %q does not have exact checkout ownership; refusing mutation", resource.kind, id)
			}
			owned = append(owned, OwnedResource{Kind: resource.kind, ID: id})
		}
	}
	return owned, nil
}

func (controller *Controller) dockerOutput(ctx context.Context, arguments ...string) ([]byte, error) {
	if err := controller.endpoint.Verify(ctx); err != nil {
		return nil, fmt.Errorf("verify pinned Docker endpoint: %w", err)
	}
	return controller.runner.Run(ctx, controller.processEnvironment(nil), controller.endpoint.DockerArguments(arguments...)...)
}

func (controller *Controller) processEnvironment(extra []string) []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "DOCKER_CONFIG": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true,
		"LANG": true, "LC_ALL": true,
	}
	selected := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			selected = append(selected, entry)
		}
	}
	base := controller.endpoint.Environment(selected)
	filtered := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(name, "LEAPVIEW_") || strings.HasPrefix(name, "COMPOSE_") {
			continue
		}
		if strings.HasPrefix(name, "DOCKER_") && name != "DOCKER_HOST" && name != "DOCKER_CONFIG" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, extra...)
}
