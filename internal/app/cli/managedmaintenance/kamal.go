package managedmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// KamalEffects is deliberately host-local: the enrolled Kamal configuration
// targets this host's loopback SSH and the same local Docker daemon. It never
// downloads an image, restores state, or invokes a migration.
type KamalEffects struct {
	Profile       HostProfile
	Request       Request
	run           commandRunner
	lockFile      *os.File
	recovering    bool
	capacityProbe func(string) (filesystemCapacity, error)
}
type commandRunner func(context.Context, string, []string, []string, string) ([]byte, error)
type renderedProfile struct {
	Service     string            `json:"service"`
	Hosts       []string          `json:"hosts"`
	Roles       []string          `json:"roles"`
	Hostname    string            `json:"hostname"`
	Environment map[string]string `json:"environment"`
	Image       string            `json:"image"`
	ProxyImage  string            `json:"proxyImage"`
}
type imageInfo struct {
	ID          string
	RepoDigests []string
	Config      struct {
		Env    []string
		Labels map[string]string
	}
}
type controlStatus struct {
	Revision, Operation, State string
	Drained                    bool
}

func (k *KamalEffects) command(ctx context.Context, bin string, args []string, env []string) ([]byte, error) {
	if bin == "bundle" {
		deadline, ok := ctx.Deadline()
		if !ok {
			budget := k.Request.Budgets.Phase
			if budget <= 0 {
				budget = 2 * time.Minute
			}
			deadline = time.Now().Add(budget)
		}
		env = append(append([]string(nil), env...), "LEAPVIEW_MANAGED_DEADLINE_UNIX_MS="+strconv.FormatInt(deadline.UnixMilli(), 10))
	}
	runner := k.run
	if runner == nil {
		runner = func(ctx context.Context, bin string, args, env []string, dir string) ([]byte, error) {
			return runCommandWithLock(ctx, bin, args, env, dir, k.lockFile)
		}
	}
	return runner(ctx, bin, args, env, k.Profile.Root)
}
func (k *KamalEffects) docker(ctx context.Context, args ...string) ([]byte, error) {
	// Ignore caller DOCKER_HOST/CONTEXT, so inventory and loopback Kamal cannot
	// accidentally operate against different daemons.
	return k.command(ctx, "docker", append([]string{"--host", "unix:///var/run/docker.sock"}, args...), []string{"PATH=" + os.Getenv("PATH"), "HOME=/root"})
}
func (k *KamalEffects) environment(release Release) ([]string, error) {
	raw, err := readOperatorFile(filepath.Join(k.Profile.Root, "environment.json"))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(k.Profile.Root, "environment.json"))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("managed environment must be private")
	}
	var values map[string]string
	if err = strictJSON(raw, &values); err != nil {
		return nil, errors.New("invalid managed environment")
	}
	for name := range values {
		if !strings.HasPrefix(name, "LEAPVIEW_") && !strings.HasPrefix(name, "KAMAL_") {
			return nil, errors.New("unexpected managed environment key")
		}
		if name == "LEAPVIEW_MANAGED_IMAGE" || name == "LEAPVIEW_MANAGED_PROXY_IMAGE" || name == "LEAPVIEW_MANAGED_GATE" || name == "LEAPVIEW_MANAGED_REVISION" || name == "LEAPVIEW_MANAGED_CONFIG" || name == "LEAPVIEW_MANAGED_LOCK_FD" || name == "LEAPVIEW_MANAGED_LOCK_PATH" || name == "LEAPVIEW_MANAGED_DEADLINE_UNIX_MS" {
			return nil, errors.New("reserved managed adapter environment")
		}
	}
	values["PATH"] = os.Getenv("PATH")
	values["HOME"] = "/root"
	values["LEAPVIEW_MANAGED_IMAGE"] = release.Image
	values["LEAPVIEW_MANAGED_PROXY_IMAGE"] = k.Profile.ProxyImage
	values["LEAPVIEW_MANAGED_GATE"] = filepath.Join(k.Profile.StateRoot, "ingress.json")
	values["LEAPVIEW_MANAGED_REVISION"] = release.Revision
	values["LEAPVIEW_MANAGED_CONFIG"] = "deploy.yml"
	values["LEAPVIEW_MANAGED_LOCK_FD"] = "3"
	values["LEAPVIEW_MANAGED_LOCK_PATH"] = filepath.Join(k.Profile.StateRoot, ".leapviewctl.lock")
	return mergeEnvironment(nil, values), nil
}
func (k *KamalEffects) kamal(ctx context.Context, release Release, args ...string) error {
	env, err := k.environment(release)
	if err != nil {
		return err
	}
	// Thor resolves the command before its options. Placing --version before
	// the subcommand selects Kamal's root help/version parser instead.
	argv := append([]string{"exec", "ruby", "-r", "./maintenance_adapter.rb", "-S", "kamal"}, args...)
	argv = append(argv, "--config-file", "deploy.yml", "--version", release.Revision, "--skip-hooks")
	_, err = k.command(ctx, "bundle", argv, env)
	return err
}
func (k *KamalEffects) projection(ctx context.Context, release Release) (renderedProfile, error) {
	var p renderedProfile
	env, err := k.environment(release)
	if err != nil {
		return p, err
	}
	raw, err := k.command(ctx, "bundle", []string{"exec", "ruby", "maintenance_config.rb"}, env)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("invalid managed configuration projection")
	}
	if p.Service != k.Profile.Service || p.Hostname != k.Profile.Hostname || len(p.Hosts) != 1 || p.Hosts[0] != "127.0.0.1" || len(p.Roles) != 1 || p.Roles[0] != "web" || p.Image != release.Image || p.ProxyImage != k.Profile.ProxyImage {
		return p, errors.New("Kamal projection differs from enrolled local host")
	}
	if p.Environment["LEAPVIEW_HOME"] != k.Profile.Home || p.Environment["LEAPVIEW_MAINTENANCE_SOCKET"] != k.Profile.Socket {
		return p, errors.New("Kamal projection lacks private startup admission")
	}
	return p, nil
}
func (k *KamalEffects) image(ctx context.Context, reference string) (imageInfo, error) {
	var images []imageInfo
	raw, err := k.docker(ctx, "image", "inspect", reference)
	if err != nil {
		return imageInfo{}, err
	}
	if json.Unmarshal(raw, &images) != nil || len(images) != 1 {
		return imageInfo{}, errors.New("invalid retained image inventory")
	}
	for _, digest := range images[0].RepoDigests {
		if digest == reference {
			return images[0], nil
		}
	}
	return imageInfo{}, errors.New("retained image lacks exact admitted repository digest")
}
func (k *KamalEffects) inventory(ctx context.Context, private bool) ([]containerInfo, error) {
	raw, err := k.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 128 {
		return nil, errors.New("managed container inventory exceeds bound")
	}
	raw, err = k.docker(ctx, append([]string{"container", "inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var items []containerInfo
	if json.Unmarshal(raw, &items) != nil {
		return nil, errors.New("invalid managed process inventory")
	}
	return items, validateInventory(k.Profile, k.Request, items, private)
}
func (k *KamalEffects) Preflight(ctx context.Context) error {
	if err := k.Profile.Validate(); err != nil {
		return err
	}
	if err := k.Request.Validate(); err != nil {
		return err
	}
	if k.Profile.Target != k.Request.Target {
		return errors.New("managed target differs from request")
	}
	digest, err := ProfileDigest(k.Profile)
	if err != nil {
		return err
	}
	if digest != k.Request.Candidate.ConfigurationDigest {
		return errors.New("managed operator configuration changed")
	}
	if err := k.checkCapacity(ctx); err != nil {
		return err
	}
	for _, r := range []Release{k.Request.Predecessor, k.Request.Candidate} {
		raw, err := readOperatorFile(filepath.Join(k.Profile.AdmissionRoot, strings.TrimPrefix(r.ArtifactAdmissionDigest, "sha256:")+".json"))
		if err != nil {
			return err
		}
		record, err := artifactadmission.ParseCanonical(raw)
		if err != nil {
			return err
		}
		actual, err := record.Digest()
		if err != nil {
			return err
		}
		if actual != r.ArtifactAdmissionDigest || record.Release.Image != r.Image || record.Release.SourceRevision != r.Revision {
			return errors.New("release differs from authenticated artifact admission")
		}
		image, err := k.image(ctx, r.Image)
		if err != nil {
			return err
		}
		if image.Config.Labels["org.opencontainers.image.revision"] != r.Revision {
			return errors.New("retained image revision differs from admission")
		}
		projection, err := k.projection(ctx, r)
		if err != nil {
			return err
		}
		if environmentIdentity(mergeEnvironment(image.Config.Env, projection.Environment)) != r.CredentialDigest {
			return errors.New("resolved runtime configuration/credentials differ from request")
		}
	}
	proxy, err := k.image(ctx, k.Profile.ProxyImage)
	if err != nil {
		return err
	}
	// Kamal-proxy has no version command. The enrolled, locally retained v0.9.2
	// reference must identify the same content as the operator-pinned digest.
	raw, err := k.docker(ctx, "image", "inspect", "basecamp/kamal-proxy:v0.9.2")
	var baseline []imageInfo
	if err != nil || json.Unmarshal(raw, &baseline) != nil || len(baseline) != 1 || baseline[0].ID != proxy.ID {
		return errors.New("proxy digest differs from enrolled Kamal v0.9.2 baseline")
	}
	enrollment := k.Request.Operation == "enroll"
	if enrollment && !k.recovering {
		var gate struct {
			Publish *bool `json:"publish"`
		}
		if err := readPrivateJSON(filepath.Join(k.Profile.StateRoot, "ingress.json"), &gate); err != nil {
			return err
		}
		if gate.Publish == nil || *gate.Publish {
			return errors.New("initial managed enrollment requires an explicitly private ingress gate")
		}
	}
	items, err := k.inventory(ctx, enrollment && !k.recovering)
	if err != nil {
		return err
	}
	predecessorPresent := false
	for _, c := range items {
		if c.State.Running && c.Config.Labels["service"] == k.Profile.Service {
			if !k.recovering && c.Config.Image != k.Request.Predecessor.Image {
				return errors.New("initial handoff requires the admitted predecessor")
			}
			predecessorPresent = true
			if environmentIdentity(c.Config.Env) != k.Request.Predecessor.CredentialDigest {
				return errors.New("running process configuration/credentials changed")
			}
			if enrollment {
				image, err := k.image(ctx, k.Request.Candidate.Image)
				if err != nil {
					return err
				}
				if c.Image != image.ID {
					return errors.New("enrollment process content differs from admitted image")
				}
			}
		}
	}
	if enrollment {
		if !predecessorPresent {
			return nil
		}
		status, err := k.control(ctx, "status", k.Request.Candidate)
		if err != nil {
			return err
		}
		if status.State == "closed" && status.Operation == "" {
			return nil
		}
		operation, _ := k.Request.Digest()
		if k.recovering && status.Operation == operation {
			switch status.State {
			case "closed", "prepared", "opening", "provisional", "admitted", "draining", "failed":
				return nil
			}
		}
		return errors.New("managed enrollment requires a startup-closed process or its own recoverable operation")
	}
	if !k.recovering {
		if !predecessorPresent {
			return errors.New("initial handoff requires a running predecessor")
		}
		status, err := k.control(ctx, "status", k.Request.Predecessor)
		if err != nil {
			return err
		}
		if status.State != "admitted" {
			return errors.New("predecessor is not admitted; use the enrolled operation recovery path")
		}
	}
	return nil
}
func (k *KamalEffects) control(ctx context.Context, action string, release Release) (controlStatus, error) {
	var status controlStatus
	operation, err := k.Request.Digest()
	if err != nil {
		return status, err
	}
	raw, _ := json.Marshal(map[string]any{"revision": release.Revision, "operation": operation, "leaseMilliseconds": k.Request.Budgets.Phase.Milliseconds()})
	method := http.MethodPost
	if action == "status" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://maintenance/"+action, bytes.NewReader(raw))
	if err != nil {
		return status, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", k.Profile.Socket)
	}}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return status, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return status, fmt.Errorf("managed control %s rejected (%d)", action, response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&status); err != nil {
		return status, err
	}
	if status.Revision != release.Revision {
		return status, errors.New("private control process revision differs")
	}
	if action != "close" && action != "status" && status.Operation != operation {
		return status, errors.New("private control operation differs")
	}
	return status, nil
}
func (k *KamalEffects) CloseAdmission(ctx context.Context) error {
	items, err := k.inventory(ctx, false)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.State.Running && c.Config.Labels["service"] == k.Profile.Service {
			r := k.Request.Predecessor
			if c.Config.Image == k.Request.Candidate.Image {
				r = k.Request.Candidate
			}
			_, err = k.control(ctx, "close", r)
			return err
		}
	}
	return nil
}
func (k *KamalEffects) CloseIngress(ctx context.Context) error {
	if err := writeGate(k.Profile, false); err != nil {
		return err
	}
	if err := k.kamal(ctx, k.Request.Predecessor, "proxy", "reboot", "--confirmed"); err != nil {
		return err
	}
	items, err := k.inventory(ctx, true)
	if err != nil {
		return err
	}
	return requireProxy(items)
}
func (k *KamalEffects) DrainAndStop(ctx context.Context) error {
	items, err := k.inventory(ctx, true)
	if err != nil {
		return err
	}
	if err = requireProxy(items); err != nil {
		return err
	}
	for _, c := range items {
		if c.State.Running && c.Config.Labels["service"] == k.Profile.Service {
			r := k.Request.Predecessor
			if c.Config.Image == k.Request.Candidate.Image {
				r = k.Request.Candidate
			}
			status, err := k.control(ctx, "status", r)
			if err != nil {
				return err
			}
			if !status.Drained || (status.State != "closed" && status.State != "failed") {
				return errors.New("application has not completed drain")
			}
			// Infinite Docker grace period: cancelling the controller never converts a
			// timed-out graceful drain into a successful SIGKILL deployment.
			if _, err = k.docker(ctx, "container", "stop", "--time", "-1", c.ID); err != nil {
				return err
			}
			raw, err := k.docker(ctx, "container", "inspect", c.ID)
			if err != nil {
				return err
			}
			var stopped []containerInfo
			if json.Unmarshal(raw, &stopped) != nil || len(stopped) != 1 || stopped[0].State.Running || stopped[0].State.OOMKilled || stopped[0].State.ExitCode != 0 {
				return errors.New("application did not exit gracefully")
			}
		}
	}
	lock, err := instancelock.Acquire(k.Profile.Home)
	if err != nil {
		return err
	}
	return lock.Release()
}
func (k *KamalEffects) StartPrepared(ctx context.Context, r Release) error {
	if err := k.checkBootInputs(ctx); err != nil {
		return err
	}
	items, err := k.inventory(ctx, true)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.State.Running && c.Config.Labels["service"] == k.Profile.Service {
			return errors.New("application owner still running")
		}
	}
	if err = k.kamal(ctx, r, "app", "boot"); err != nil {
		return err
	}
	return k.verifyProcess(ctx, r)
}
func (k *KamalEffects) verifyProcess(ctx context.Context, r Release) error {
	items, err := k.inventory(ctx, true)
	if err != nil {
		return err
	}
	if err = requireProxy(items); err != nil {
		return err
	}
	found := false
	image, err := k.image(ctx, r.Image)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.State.Running && c.Config.Labels["service"] == k.Profile.Service {
			if c.Config.Image != r.Image || c.Image != image.ID || environmentIdentity(c.Config.Env) != r.CredentialDigest {
				return errors.New("prepared process differs from admitted release")
			}
			found = true
		}
	}
	if !found {
		return errors.New("prepared application process absent")
	}
	return nil
}
func (k *KamalEffects) VerifyPrepared(ctx context.Context, r Release) error {
	if err := k.verifyProcess(ctx, r); err != nil {
		return err
	}
	status, err := k.control(ctx, "prepare", r)
	if err != nil {
		return err
	}
	if status.State != "prepared" {
		return errors.New("application was not prepared closed")
	}
	return nil
}
func (k *KamalEffects) OpenWork(ctx context.Context, r Release) error {
	status, err := k.control(ctx, "open", r)
	if err != nil {
		return err
	}
	if status.State != "provisional" {
		return errors.New("worker admission was not provisional")
	}
	return nil
}
func (k *KamalEffects) OpenIngress(ctx context.Context, r Release) error {
	if err := k.verifyProcess(ctx, r); err != nil {
		return err
	}
	status, err := k.control(ctx, "status", r)
	if err != nil {
		return err
	}
	operation, _ := k.Request.Digest()
	if status.State != "provisional" || status.Operation != operation {
		return errors.New("workers are not provisionally admitted")
	}
	if err = writeGate(k.Profile, true); err != nil {
		return err
	}
	if err = k.kamal(ctx, r, "proxy", "reboot", "--confirmed"); err != nil {
		return err
	}
	items, err := k.inventory(ctx, false)
	if err != nil {
		return err
	}
	for _, c := range items {
		if c.State.Running && strings.TrimPrefix(c.Name, "/") == "kamal-proxy" {
			if len(c.HostConfig.PortBindings["443/tcp"]) == 0 {
				return errors.New("proxy did not publish HTTPS")
			}
		}
	}
	return k.publicReadiness(ctx)
}
func (k *KamalEffects) publicReadiness(ctx context.Context) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return waitPublishedReadiness(ctx, client, "https://"+k.Profile.Hostname+"/readyz")
}
func waitPublishedReadiness(ctx context.Context, client *http.Client, endpoint string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	// Kamal's proxy reboot returns after detached Docker start. Listening and
	// persisted route restoration can finish later, within this phase's deadline.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastFailure error
	deadlineError := func() error {
		if lastFailure != nil {
			return fmt.Errorf("published proxy readiness failed (%v): %w", lastFailure, ctx.Err())
		}
		return fmt.Errorf("published proxy readiness failed: %w", ctx.Err())
	}
	for {
		if err := ctx.Err(); err != nil {
			return deadlineError()
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return ctx.Err()
			}
			lastFailure = fmt.Errorf("HTTP status %d", response.StatusCode)
		} else {
			lastFailure = err
		}
		select {
		case <-ctx.Done():
			return deadlineError()
		case <-ticker.C:
		}
	}
}
func (k *KamalEffects) FinalizeWork(ctx context.Context, r Release) error {
	status, err := k.control(ctx, "finalize", r)
	if err != nil {
		return err
	}
	if status.State != "admitted" {
		return errors.New("worker admission was not finalized")
	}
	return nil
}

func strictJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing managed JSON")
	}
	return nil
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if n > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func runBoundedCommand(ctx context.Context, bin string, args, env []string, dir string) ([]byte, error) {
	return runCommandWithLock(ctx, bin, args, env, dir, nil)
}
func runCommandWithLock(ctx context.Context, bin string, args, env []string, dir string, lock *os.File) ([]byte, error) {
	command := exec.CommandContext(ctx, bin, args...)
	command.Dir = dir
	command.Env = env
	if lock != nil {
		command.ExtraFiles = []*os.File{lock}
	}
	configureCommandCancellation(command)
	var stdout boundedOutput
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("managed %s command failed: %w", bin, err)
	}
	if stdout.overflow {
		return nil, errors.New("managed command output exceeded limit")
	}
	return stdout.Bytes(), nil
}

var _ Effects = (*KamalEffects)(nil)

func requireProxy(items []containerInfo) error {
	for _, c := range items {
		if c.State.Running && strings.TrimPrefix(c.Name, "/") == "kamal-proxy" {
			return nil
		}
	}
	return errors.New("managed proxy is absent")
}
