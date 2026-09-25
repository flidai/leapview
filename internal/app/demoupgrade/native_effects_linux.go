//go:build linux

package demoupgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// every CFO page against a loopback-only restored instance. EOF/cancellation is
// failure, not approval; a durable intent already exists before this wait.
func (e *NativeEffects) awaitBrowser(ctx context.Context, phase string) error {
	if _, err := fmt.Fprintln(e.stdout, phase); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		line, err := e.reader.ReadString('\n')
		if err == nil && line != "commit\n" {
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
func (e *NativeEffects) clone(ctx context.Context, name, alias string, original dockerInspection, volumes map[string]string, port string) error {
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
	if port != "" {
		args = append(args, "-p", port)
	}
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
	if err = e.clone(ctx, prefix+"-pg", nativePG, e.original.Postgres, volumes, ""); err != nil {
		return "", err
	}
	if err = e.waitPG(ctx, prefix+"-pg", 28); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-app", "leapview", e.original.App, volumes, ""); err != nil {
		return "", err
	}
	if err = e.waitApp(ctx, prefix+"-app", id.Predecessor, e.request.PredecessorRevision); err != nil {
		return "", err
	}
	if err = e.clone(ctx, prefix+"-caddy", "caddy", e.original.Caddy, volumes, "127.0.0.1:8444:443"); err != nil {
		return "", err
	}
	if err = e.awaitBrowser(ctx, "AWAITING_RECOVERY_BROWSER_VALIDATION"); err != nil {
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
	if _, err := e.docker(ctx, "start", nativePG); err != nil {
		return err
	}
	if err := e.waitPG(ctx, nativePG, 28); err != nil {
		return err
	}
	dsn := containerEnv(e.original.App)["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"]
	if dsn == "" {
		return errors.New("control migrator credential is missing")
	}
	secret := filepath.Join(e.operation, "migrator.url")
	if err := securefs.WritePrivateFileAtomic(secret, []byte(dsn)); err != nil {
		return err
	}
	defer os.Remove(secret)
	// No application environment or runtime credentials enter this process.
	_, err := e.docker(ctx, "run", "--rm", "--name", e.clonePrefix()+"-migrator", "--user", "0:0", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--network", nativeNetwork,
		"--mount", "type=bind,src="+filepath.Join(e.operation, "request.json")+",dst=/upgrade/request.json,readonly",
		"--mount", "type=bind,src="+secret+",dst=/upgrade/migrator.url,readonly",
		"--mount", "type=bind,src="+filepath.Join(e.provider, JournalName)+",dst=/upgrade-journal.json,readonly",
		"--entrypoint", "/usr/local/libexec/leapviewctl", id.Candidate, "demo-upgrade", "migrate", "--request", "/upgrade/request.json", "--journal", "/upgrade-journal.json", "--credential", "/upgrade/migrator.url", "--recovery-digest", digest)
	return err
}
func (e *NativeEffects) link(target string) error {
	temp := filepath.Join(e.root, ".upgrade-current")
	if err := os.Remove(temp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, filepath.Join(e.root, "current")); err != nil {
		return err
	}
	return syncDirectory(e.root)
}
func (e *NativeEffects) ensureAgentKey() error {
	path := filepath.Join(e.root, "leapview.env")
	data, err := securefs.ReadPrivateFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=") && strings.TrimSpace(strings.TrimPrefix(line, "LEAPVIEW_AGENT_CREDENTIAL_KEY=")) != "" {
			return nil
		}
	}
	keyPath := filepath.Join(e.operation, "agent-credential-key")
	key, err := securefs.ReadPrivateFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			return err
		}
		key = []byte(hex.EncodeToString(raw))
		err = securefs.WritePrivateFileAtomic(keyPath, key)
	}
	if err != nil {
		return err
	}
	data, err = setDeploymentValue(data, "LEAPVIEW_AGENT_CREDENTIAL_KEY", string(key))
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, data)
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
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", "leapview"); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "caddy"); err != nil {
		return err
	}
	// Recreated containers inherit Compose restart policy; keep writers fenced
	// across reboot until the durable commit boundary.
	for _, name := range []string{nativeApp, nativePG} {
		if _, err := e.docker(ctx, "update", "--restart=no", name); err != nil {
			return err
		}
	}
	return e.waitApp(ctx, nativeApp, id.Candidate, e.request.CandidateRevision)
}
func (e *NativeEffects) ValidateCandidate(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.waitPG(ctx, nativePG, 30); err != nil {
		return err
	}
	if err := e.waitApp(ctx, nativeApp, id.Candidate, e.request.CandidateRevision); err != nil {
		return err
	}
	return e.awaitBrowser(ctx, "AWAITING_CANDIDATE_BROWSER_VALIDATION")
}
func (e *NativeEffects) expose(ctx context.Context, image string) error {
	if err := e.writeDeployment(image, false); err != nil {
		return err
	}
	for _, name := range []string{nativeApp, nativePG} {
		if _, err := e.docker(ctx, "update", "--restart=unless-stopped", name); err != nil {
			return err
		}
	}
	return e.compose(ctx, "up", "-d", "--no-deps", "caddy")
}

// Commit/reopening may have been fsynced immediately before a host reboot.
// Restart the already-selected runtime without restoring or replaying SQL.
func (e *NativeEffects) resumeRuntime(ctx context.Context, image, revision string, schema int) error {
	if _, err := e.docker(ctx, "start", nativePG); err != nil {
		return err
	}
	if err := e.waitPG(ctx, nativePG, schema); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", "leapview"); err != nil {
		return err
	}
	return e.waitApp(ctx, nativeApp, image, revision)
}
func (e *NativeEffects) ExposeCandidate(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.resumeRuntime(ctx, id.Candidate, e.request.CandidateRevision, 30); err != nil {
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
	for _, name := range []string{nativeCaddy, nativeApp, nativePG} {
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
	if _, err := e.docker(ctx, "start", nativePG); err != nil {
		return err
	}
	if err := e.waitPG(ctx, nativePG, 28); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", "leapview"); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "caddy"); err != nil {
		return err
	}
	return e.waitApp(ctx, nativeApp, id.Predecessor, e.request.PredecessorRevision)
}
func (e *NativeEffects) ExposePredecessor(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	if err := e.resumeRuntime(ctx, id.Predecessor, e.request.PredecessorRevision, 28); err != nil {
		return err
	}
	return e.expose(ctx, id.Predecessor)
}
