//go:build linux

package hostinstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

var _ Effects = (*NativeEffects)(nil)

func (e *NativeEffects) snapshot() string { return filepath.Join(e.operation, "snapshot") }
func (e *NativeEffects) clonePrefix() string {
	return "leapview-recovery-" + strings.TrimPrefix(e.id.ArtifactAdmissionDigest, "sha256:")[:16]
}

// Browser approval is delivered only by the authenticated runner after testing
// installation-specific checks against a loopback-only instance. EOF/cancellation is
// failure, not approval; a durable intent already exists before this wait.
func (e *NativeEffects) awaitBrowser(ctx context.Context, phase string) error {
	if _, err := fmt.Fprintf(e.stdout, "%s %s\n", phase, e.id.ArtifactAdmissionDigest); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		line, err := e.reader.ReadString('\n')
		if err == nil && line != "commit "+e.id.ArtifactAdmissionDigest+" "+phase+"\n" {
			err = errors.New("browser validation was not approved")
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(6 * time.Minute):
		return errors.New("browser validation timed out")
	}
}
func (e *NativeEffects) cleanupClone(ctx context.Context) error {
	prefix := e.clonePrefix()
	// These deterministic names belong only to this journal identity. No prune,
	// volume removal, or unrelated container cleanup is permitted.
	for _, suffix := range []string{"-migrator", "-caddy", "-app", "-pg"} {
		name := prefix + suffix
		found, err := e.docker(ctx, "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if err != nil {
			return err
		}
		if found != "" {
			if _, err = e.docker(ctx, "rm", "-f", "-v", name); err != nil {
				return err
			}
		}
	}
	found, err := e.docker(ctx, "network", "ls", "--filter", "name=^"+prefix+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if found != "" {
		_, err = e.docker(ctx, "network", "rm", prefix)
	}
	return err
}
func (e *NativeEffects) clone(ctx context.Context, name, alias string, original dockerInspection, volumes map[string]string) error {
	envFile := filepath.Join(e.operation, name+".env")
	for _, value := range original.Config.Env {
		if strings.ContainsAny(value, "\n\r") {
			return errors.New("multiline Docker environment is unsupported")
		}
	}
	if err := securefs.WritePrivateFileAtomic(envFile, []byte(strings.Join(original.Config.Env, "\n")+"\n")); err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", name, "--network", e.clonePrefix(), "--network-alias", alias, "--restart=no", "--env-file", envFile}
	for _, mount := range original.Mounts {
		switch mount.Type {
		case "tmpfs":
			args = append(args, "--tmpfs", mount.Destination+":rw,nosuid,nodev,size=536870912,mode=1777")
		case "volume":
			source := ""
			for domain, live := range e.original.Volumes {
				if live == mount.Source {
					source = volumes[domain]
				}
			}
			if source == "" {
				return errors.New("unmapped cloned volume")
			}
			args = append(args, "--mount", "type=bind,src="+source+",dst="+mount.Destination)
		case "bind":
			if mount.RW {
				return errors.New("writable clone bind is forbidden")
			}
			args = append(args, "--mount", "type=bind,src="+mount.Source+",dst="+mount.Destination+",readonly")
		default:
			return errors.New("unsupported clone mount")
		}
	}
	args = append(args, original.Config.Image)
	args = append(args, original.Config.Cmd...)
	_, err := e.docker(ctx, args...)
	return err
}
func (e *NativeEffects) waitPG(ctx context.Context, name string, schema int) error {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		ready, err := e.docker(ctx, "exec", name, "pg_isready", "-h", "127.0.0.1")
		if err == nil && strings.Contains(ready, "accepting connections") {
			actual, err := e.pgQuery(ctx, name, "leapview_control", "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied")
			if err == nil && actual == fmt.Sprint(schema) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("PostgreSQL did not reach required schema")
		case <-time.After(time.Second):
		}
	}
}
func (e *NativeEffects) waitApp(ctx context.Context, name, image, revision string) error {
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	for {
		info, err := e.inspect(ctx, name)
		if err == nil && info.State.Running && info.Config.Image == image {
			if _, err = e.docker(ctx, "exec", name, "leapview", "healthcheck"); err == nil {
				raw, err := e.docker(ctx, "exec", name, "leapview", "version", "--json")
				var version buildinfo.Identity
				if err == nil && json.Unmarshal([]byte(raw), &version) == nil && !version.Dirty && version.Revision == revision {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("runtime did not become ready at the required image and source")
		case <-time.After(time.Second):
		}
	}
}
func (e *NativeEffects) CaptureAndVerify(ctx context.Context, id Identity) (digest string, err error) {
	if id != e.id {
		return "", ErrIdentity
	}
	if err = e.stopped(ctx); err != nil {
		return "", err
	}
	digest, err = CaptureStoppedDirectories(ctx, e.snapshot(), id.Target, e.original.Volumes)
	if err != nil {
		return "", err
	}
	cloneRoot := filepath.Join(e.operation, "rehearsal")
	if err = securefs.EnsurePrivateDir(cloneRoot); err != nil {
		return "", err
	}
	volumes := map[string]string{}
	for name := range e.original.Volumes {
		volumes[name] = filepath.Join(cloneRoot, name)
	}
	if err = RestoreStoppedDirectories(ctx, e.snapshot(), id.Target, digest, volumes); err != nil {
		return "", err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if cleanupErr := e.cleanupClone(cleanupCtx); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	prefix := e.clonePrefix()
	if _, err = e.docker(ctx, "network", "create", "--internal", prefix); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-pg", e.request.Profile.Postgres, e.original.Postgres, volumes); err != nil {
		return "", err
	}
	if err = e.waitPG(ctx, prefix+"-pg", e.request.Plan.CurrentSchema); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-app", e.request.Profile.AppService, e.original.App, volumes); err != nil {
		return "", err
	}
	if err = e.waitApp(ctx, prefix+"-app", id.Predecessor, e.request.PredecessorRevision); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-caddy", e.request.Profile.ProxyService, e.original.Caddy, volumes); err != nil {
		return "", err
	}
	// An internal network has no published Docker ports. Reach its private
	// bridge IP from a loopback-only host relay; never attach the clone to the
	// live network merely to make the browser check reachable.
	cloneCaddy, err := e.inspect(ctx, prefix+"-caddy")
	if err != nil {
		return "", err
	}
	var endpoint struct{ IPAddress string }
	if err = json.Unmarshal(cloneCaddy.NetworkSettings.Networks[prefix], &endpoint); err != nil {
		return "", err
	}
	ip := net.ParseIP(endpoint.IPAddress)
	if ip == nil || !ip.IsPrivate() {
		return "", errors.New("invalid isolated Caddy bridge address")
	}
	_, closeRelay, err := e.relay(ctx, e.request.Profile.RehearsalBinding, net.JoinHostPort(endpoint.IPAddress, "443"))
	if err != nil {
		return "", err
	}
	defer closeRelay()
	if err = e.awaitBrowser(ctx, "AWAITING_RECOVERY_BROWSER_VALIDATION"); err != nil {
		return "", err
	}
	// Rehearse the exact candidate against the recovered predecessor data before
	// any SQL is applied to the live cluster. The private network has no route to
	// production integrations; the backup itself remains immutable.
	if _, err = e.docker(ctx, "rm", "-f", prefix+"-app"); err != nil {
		return "", err
	}
	if err = e.migrateOn(ctx, id, digest, e.clonePrefix(), true); err != nil {
		return "", err
	}
	candidate := e.original.App
	candidate.Config.Image = id.Candidate
	prepared, err := e.candidateEnvironment()
	if err != nil {
		return "", err
	}
	var key string
	for _, line := range strings.Split(string(prepared), "\n") {
		if strings.HasPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=") {
			key = line
		}
	}
	candidate.Config.Env = append([]string{}, candidate.Config.Env...)
	for i, line := range candidate.Config.Env {
		if strings.HasPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=") {
			candidate.Config.Env[i] = key
			key = ""
			break
		}
	}
	if key != "" {
		candidate.Config.Env = append(candidate.Config.Env, key)
	}
	if err = e.clone(ctx, prefix+"-app", e.request.Profile.AppService, candidate, volumes); err != nil {
		return "", err
	}
	if err = e.waitPG(ctx, prefix+"-pg", e.request.Plan.CandidateSchema); err != nil {
		return "", err
	}
	if err = e.waitApp(ctx, prefix+"-app", id.Candidate, e.request.CandidateRevision); err != nil {
		return "", err
	}
	// Recreate the proxy so its upstream resolves the candidate clone's address.
	if _, err = e.docker(ctx, "rm", "-f", prefix+"-caddy"); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-caddy", e.request.Profile.ProxyService, e.original.Caddy, volumes); err != nil {
		return "", err
	}
	closeRelay()
	proxy, err := e.inspect(ctx, prefix+"-caddy")
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(proxy.NetworkSettings.Networks[prefix], &endpoint); err != nil {
		return "", err
	}
	ip = net.ParseIP(endpoint.IPAddress)
	if ip == nil || !ip.IsPrivate() {
		return "", errors.New("invalid rehearsal proxy address")
	}
	_, closeCandidateRelay, err := e.relay(ctx, e.request.Profile.RehearsalBinding, net.JoinHostPort(endpoint.IPAddress, "443"))
	if err != nil {
		return "", err
	}
	defer closeCandidateRelay()
	if err = e.awaitBrowser(ctx, "AWAITING_REHEARSAL_BROWSER_VALIDATION"); err != nil {
		return "", err
	}
	proof, err := json.Marshal(State{Identity: id, Phase: Verified, RecoveryDigest: digest})
	if err != nil {
		return "", err
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(e.operation, "rehearsal-passed.json"), proof); err != nil {
		return "", err
	}
	// The immutable snapshot is checked again after exercising the writable clone.
	if _, err = readSnapshot(ctx, e.snapshot(), id.Target, digest); err != nil {
		return "", err
	}
	return digest, nil
}
func (e *NativeEffects) Migrate(ctx context.Context, id Identity, digest string) error {
	if id != e.id {
		return ErrIdentity
	}
	if _, err := readSnapshot(ctx, e.snapshot(), id.Target, digest); err != nil {
		return err
	}
	if err := e.stopped(ctx); err != nil {
		return err
	}
	if _, err := e.docker(ctx, "start", e.request.Profile.Postgres); err != nil {
		return err
	}
	if err := e.waitPG(ctx, e.request.Profile.Postgres, e.request.Plan.CurrentSchema); err != nil {
		return err
	}
	proof, err := securefs.ReadPrivateFile(filepath.Join(e.operation, "rehearsal-passed.json"))
	if err != nil {
		return err
	}
	var passed State
	if json.Unmarshal(proof, &passed) != nil || passed.Identity != id || passed.RecoveryDigest != digest || passed.Phase != Verified {
		return errors.New("missing exact candidate rehearsal")
	}
	return e.migrateOn(ctx, id, digest, e.request.Profile.Network, false)
}
func (e *NativeEffects) migrateOn(ctx context.Context, id Identity, digest, network string, rehearsal bool) error {
	dsn, err := e.migratorURL(e.original.App)
	if err != nil {
		return err
	}
	secret := filepath.Join(e.operation, "migrator.url")
	if err := securefs.WritePrivateFileAtomic(secret, []byte(dsn)); err != nil {
		return err
	}
	defer os.Remove(secret)
	action := "migrate"
	journalPath := filepath.Join(e.root, JournalName)
	if rehearsal {
		action = "rehearse"
	}
	// No application environment or runtime credentials enter this process.
	args := []string{"run", "--rm", "--name", e.clonePrefix() + "-migrator", "--user", "0:0", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--network", network,
		"--mount", "type=bind,src=" + filepath.Join(e.operation, "request.json") + ",dst=/upgrade/request.json,readonly",
		"--mount", "type=bind,src=" + secret + ",dst=/upgrade/migrator.url,readonly",
		"--mount", "type=bind,src=" + journalPath + ",dst=/upgrade-journal.json,readonly"}
	tlsMounts, err := migrationTLSMounts(dsn, e.original.App)
	if err != nil {
		return err
	}
	args = append(args, tlsMounts...)
	args = append(args, "--entrypoint", "/usr/local/libexec/leapviewctl", id.Candidate, "host", "upgrade", action, "--request", "/upgrade/request.json", "--journal", "/upgrade-journal.json", "--credential", "/upgrade/migrator.url", "--recovery-digest", digest)
	_, err = e.docker(ctx, args...)
	return err
}
func (e *NativeEffects) link(target string) error {
	if !strings.HasPrefix(target, "releases/") {
		return errors.New("invalid release generation")
	}
	return activateGeneration(InstalledPaths(e.root), strings.TrimPrefix(target, "releases/"))
}
func (e *NativeEffects) candidateEnvironment() ([]byte, error) {
	path := filepath.Join(e.root, "leapview.env")
	data, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=") && strings.TrimSpace(strings.TrimPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=")) != "" {
			key := strings.TrimSpace(strings.TrimPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY="))
			decoded, err := hex.DecodeString(key)
			if err != nil || len(decoded) != 32 {
				return nil, errors.New("existing agent credential key must be 32-byte hex; refusing to rotate it")
			}
			return data, nil
		}
	}
	keyPath := filepath.Join(e.operation, "agent-credential-key")
	key, err := securefs.ReadPrivateFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			return nil, err
		}
		key = []byte(hex.EncodeToString(raw))
		err = securefs.WritePrivateFileAtomic(keyPath, key)
	}
	if err != nil {
		return nil, err
	}
	data, err = setDeploymentValue(data, "LEAPVIEW_AGENT_CREDENTIAL_KEY", string(key))
	if err != nil {
		return nil, err
	}
	return data, nil
}
func (e *NativeEffects) ensureAgentKey() error {
	data, err := e.candidateEnvironment()
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(e.root, "leapview.env"), data)
}
func (e *NativeEffects) StartCandidateIsolated(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.ensureAgentKey(); err != nil {
		return err
	}
	if err := e.writeDeployment(id.Candidate, true); err != nil {
		return err
	}
	if err := e.link("releases/sha256-" + strings.TrimPrefix(id.Candidate, "ghcr.io/flidai/leapview@sha256:")); err != nil {
		return err
	}
	if err := e.composePrivate(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", e.request.Profile.AppService); err != nil {
		return err
	}
	if err := e.composePrivate(ctx, "up", "-d", "--no-deps", e.request.Profile.ProxyService); err != nil {
		return err
	}
	// The Compose override disables restart at container creation, without a
	// crash window between creation and a later Docker update.
	for _, name := range []string{e.app(), e.request.Profile.Postgres} {
		if _, err := e.docker(ctx, "update", "--restart=no", name); err != nil {
			return err
		}
	}
	return e.waitApp(ctx, e.app(), id.Candidate, e.request.CandidateRevision)
}
func (e *NativeEffects) ValidateCandidate(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.waitPG(ctx, e.request.Profile.Postgres, e.request.Plan.CandidateSchema); err != nil {
		return err
	}
	if err := e.waitApp(ctx, e.app(), id.Candidate, e.request.CandidateRevision); err != nil {
		return err
	}
	return e.awaitBrowser(ctx, "AWAITING_CANDIDATE_BROWSER_VALIDATION")
}
func (e *NativeEffects) expose(ctx context.Context, image string) error {
	if err := e.writeDeployment(image, false); err != nil {
		return err
	}
	for _, name := range []string{e.app(), e.request.Profile.Postgres} {
		if _, err := e.docker(ctx, "update", "--restart=unless-stopped", name); err != nil {
			return err
		}
	}
	return e.compose(ctx, "up", "-d", "--no-deps", e.request.Profile.ProxyService)
}

// Commit/reopening may have been fsynced immediately before a host reboot.
// Restart the already-selected runtime without restoring or replaying SQL.
func (e *NativeEffects) resumeRuntime(ctx context.Context, image, revision string, schema int) error {
	if _, err := e.docker(ctx, "start", e.request.Profile.Postgres); err != nil {
		return err
	}
	if err := e.waitPG(ctx, e.request.Profile.Postgres, schema); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", e.request.Profile.AppService); err != nil {
		return err
	}
	return e.waitApp(ctx, e.app(), image, revision)
}
func (e *NativeEffects) ExposeCandidate(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.resumeRuntime(ctx, id.Candidate, e.request.CandidateRevision, e.request.Plan.CandidateSchema); err != nil {
		return err
	}
	if err := e.expose(ctx, id.Candidate); err != nil {
		return err
	}
	raw, err := securefs.ReadPrivateFile(filepath.Join(e.operation, "original-config", ".host-install.json"))
	if err != nil {
		return err
	}
	var marker map[string]any
	if err = json.Unmarshal(raw, &marker); err != nil {
		return err
	}
	marker["image"] = id.Candidate
	raw, err = json.Marshal(marker)
	if err != nil {
		return err
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(e.root, ".host-install.json"), raw); err != nil {
		return err
	}
	receipt, err := json.Marshal(map[string]any{"image": id.Candidate, "revision": e.request.CandidateRevision, "previousImage": id.Predecessor, "upgradeOperation": e.operation})
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(e.provider, "compose-deployment.json"), receipt)
}
func (e *NativeEffects) StopCandidate(ctx context.Context) error {
	if err := e.cleanupClone(ctx); err != nil {
		return err
	}
	for _, name := range []string{e.proxy(), e.app(), e.request.Profile.Postgres} {
		if _, err := e.docker(ctx, "update", "--restart=no", name); err != nil {
			return err
		}
		if _, err := e.docker(ctx, "stop", "--time", "120", name); err != nil {
			return err
		}
	}
	return e.stopped(ctx)
}
func (e *NativeEffects) Restore(ctx context.Context, id Identity, digest string) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.stopped(ctx); err != nil {
		return err
	}
	return RestoreStoppedDirectories(ctx, e.snapshot(), id.Target, digest, e.original.Volumes)
}
func (e *NativeEffects) VerifyPredecessor(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	for _, name := range []string{"leapview.env", ".host-install.json"} {
		raw, err := securefs.ReadPrivateFile(filepath.Join(e.operation, "original-config", name))
		if err != nil {
			return err
		}
		if err = securefs.WritePrivateFileAtomic(filepath.Join(e.root, name), raw); err != nil {
			return err
		}
	}
	if err := e.writeDeployment(id.Predecessor, true); err != nil {
		return err
	}
	if err := e.link(e.original.Current); err != nil {
		return err
	}
	if _, err := e.docker(ctx, "start", e.request.Profile.Postgres); err != nil {
		return err
	}
	if err := e.waitPG(ctx, e.request.Profile.Postgres, e.request.Plan.CurrentSchema); err != nil {
		return err
	}
	if err := e.composePrivate(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", e.request.Profile.AppService); err != nil {
		return err
	}
	if err := e.composePrivate(ctx, "up", "-d", "--no-deps", e.request.Profile.ProxyService); err != nil {
		return err
	}
	return e.waitApp(ctx, e.app(), id.Predecessor, e.request.PredecessorRevision)
}
func (e *NativeEffects) ExposePredecessor(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.resumeRuntime(ctx, id.Predecessor, e.request.PredecessorRevision, e.request.Plan.CurrentSchema); err != nil {
		return err
	}
	return e.expose(ctx, id.Predecessor)
}

func (e *NativeEffects) migratorURL(app dockerInspection) (string, error) {
	value := containerEnv(app)["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"]
	if path := e.request.Profile.ControlMigratorURLFile; path != "" {
		raw, err := securefs.ReadPrivateFile(path)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(string(raw))
	}
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("private control migrator credential is missing or malformed")
	}
	return value, nil
}

// Mount only the TLS files explicitly named by the migrator URL, preserving
// their container paths. Never copy the application's environment or whole
// secret directories into the one-shot migrator.
func migrationTLSMounts(dsn string, app dockerInspection) ([]string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, errors.New("invalid migrator URL")
	}
	var args []string
	seen := map[string]bool{}
	for _, key := range []string{"sslrootcert", "sslcert", "sslkey"} {
		path := u.Query().Get(key)
		if path == "" || (key == "sslrootcert" && path == "system") || seen[path] {
			continue
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, ",\r\n") {
			return nil, errors.New("TLS material must use canonical container paths")
		}
		source := ""
		for _, mount := range app.Mounts {
			if mount.Type != "bind" || mount.RW {
				continue
			}
			if path == mount.Destination {
				source = mount.Source
				break
			}
			if strings.HasPrefix(path, mount.Destination+"/") {
				source = filepath.Join(mount.Source, strings.TrimPrefix(path, mount.Destination+"/"))
				break
			}
		}
		info, statErr := os.Lstat(source)
		if source == "" || strings.ContainsAny(source, ",\r\n") || statErr != nil || !info.Mode().IsRegular() {
			return nil, errors.New("migrator TLS material must be an existing read-only application bind file")
		}
		args = append(args, "--mount", "type=bind,src="+source+",dst="+path+",readonly")
		seen[path] = true
	}
	return args, nil
}

// Set restart policy as part of container creation. Updating it only after
// compose up leaves a reboot window before validation/recovery has completed.
func (e *NativeEffects) composePrivate(ctx context.Context, args ...string) error {
	raw, err := json.Marshal(map[string]any{"services": map[string]any{
		e.request.Profile.AppService:   map[string]string{"restart": "no"},
		e.request.Profile.ProxyService: map[string]string{"restart": "no"},
	}})
	if err != nil {
		return err
	}
	path := filepath.Join(e.operation, "compose.maintenance.json")
	if err := securefs.WritePrivateFileAtomic(path, raw); err != nil {
		return err
	}
	return e.compose(ctx, append([]string{"--file", path}, args...)...)
}
