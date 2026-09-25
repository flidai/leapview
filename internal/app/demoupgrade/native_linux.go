//go:build linux

package demoupgrade

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const nativeRoot = "/opt/leapview"
const nativeProvider = "/etc/leapview-provider-cfo"
const nativeApp = "leapview-cfo-leapview-1"
const nativePG = "demo02-postgres-cfo"
const nativeCaddy = "leapview-cfo-caddy-1"
const nativeNetwork = "leapview-cfo_default"
const nativePGImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

type dockerInspection struct {
	Config struct {
		Image  string
		Env    []string
		Cmd    []string
		Labels map[string]string
	}
	State struct {
		Running  bool
		ExitCode int
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
	HostConfig struct {
		RestartPolicy struct {
			Name              string
			MaximumRetryCount int
		}
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
	}
	NetworkSettings struct{ Networks map[string]json.RawMessage }
}
type nativeOriginal struct {
	App      dockerInspection  `json:"app"`
	Postgres dockerInspection  `json:"postgres"`
	Caddy    dockerInspection  `json:"caddy"`
	Volumes  map[string]string `json:"volumes"`
	Current  string            `json:"current"`
}

// NativeEffects is deliberately restricted to the existing single-host demo
// profile. It must never be used for external tablespaces, S3 roots, a different
// PostgreSQL image, a clustered target, or a different schema transition.
type NativeEffects struct {
	root, provider string
	execute        func(context.Context, ...string) (string, error)
	reader         *bufio.Reader
	request        NativeRequest
	id             Identity
	operation      string
	original       nativeOriginal
	stdin          io.Reader
	stdout         io.Writer
	log            *os.File
}

func NewNativeEffects(request NativeRequest, stdin io.Reader, stdout io.Writer) (*NativeEffects, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("demo upgrade requires root")
	}
	host, err := os.Hostname()
	if err != nil || host != "app-leapview-demo-02" {
		return nil, errors.New("demo upgrade requires the verified demo-02 host")
	}
	id, err := request.Identity()
	if err != nil {
		return nil, err
	}
	binary := buildinfo.Current()
	if binary.Dirty || binary.Revision != request.CandidateRevision {
		return nil, errors.New("host controller must come from the exact qualified candidate")
	}
	operation := filepath.Join(nativeProvider, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"))
	if err = securefs.EnsurePrivateDir(operation); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(operation, "operator.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	e := &NativeEffects{root: nativeRoot, provider: nativeProvider, reader: bufio.NewReader(stdin), request: request, id: id, operation: operation, stdin: stdin, stdout: stdout, log: log}
	if raw, err := securefs.ReadPrivateFile(filepath.Join(operation, "original.json")); err == nil {
		if err = json.Unmarshal(raw, &e.original); err != nil {
			log.Close()
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Close()
		return nil, err
	}
	return e, nil
}
func (e *NativeEffects) Close() error { return e.log.Close() }
func (e *NativeEffects) docker(ctx context.Context, args ...string) (string, error) {
	if e.execute != nil {
		return e.execute(ctx, args...)
	}
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stderr = e.log
	raw, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s failed; inspect protected upgrade log: %w", args[0], err)
	}
	return strings.TrimSpace(string(raw)), nil
}
func (e *NativeEffects) inspect(ctx context.Context, name string) (dockerInspection, error) {
	raw, err := e.docker(ctx, "inspect", name)
	if err != nil {
		return dockerInspection{}, err
	}
	var records []dockerInspection
	if err = json.Unmarshal([]byte(raw), &records); err != nil || len(records) != 1 {
		return dockerInspection{}, errors.New("invalid container inspection")
	}
	return records[0], nil
}
func containerEnv(c dockerInspection) map[string]string {
	m := map[string]string{}
	for _, entry := range c.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			m[key] = value
		}
	}
	return m
}
func (e *NativeEffects) compose(ctx context.Context, args ...string) error {
	all := []string{"compose", "--project-name", "leapview-cfo", "--project-directory", e.root, "--env-file", filepath.Join(e.root, "deployment.env"), "-f", filepath.Join(e.root, "compose.yaml"), "-f", filepath.Join(e.root, "compose.https.yaml")}
	_, err := e.docker(ctx, append(all, args...)...)
	return err
}
func (e *NativeEffects) Admit(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	app, err := e.inspect(ctx, nativeApp)
	if err != nil {
		return err
	}
	pg, err := e.inspect(ctx, nativePG)
	if err != nil {
		return err
	}
	caddy, err := e.inspect(ctx, nativeCaddy)
	if err != nil {
		return err
	}
	if !app.State.Running || !pg.State.Running || !caddy.State.Running || app.Config.Image != id.Predecessor || pg.Config.Image != nativePGImage || app.Config.Labels["com.docker.compose.project"] != "leapview-cfo" {
		return errors.New("unexpected predecessor deployment")
	}
	for _, c := range []dockerInspection{app, pg, caddy} {
		if len(c.NetworkSettings.Networks) != 1 || c.NetworkSettings.Networks[nativeNetwork] == nil || c.HostConfig.RestartPolicy.Name != "unless-stopped" {
			return errors.New("unsupported network or restart topology")
		}
	}
	members, err := e.docker(ctx, "network", "inspect", nativeNetwork, "--format", "{{len .Containers}}")
	if err != nil || members != "3" {
		return errors.New("unexpected network participants; cannot fence all writers")
	}
	if !strings.Contains(caddy.Config.Image, "@sha256:") {
		return errors.New("Caddy must be digest pinned")
	}
	if len(pg.HostConfig.PortBindings) != 0 {
		return errors.New("externally published PostgreSQL is unsupported")
	}
	expected := map[string]struct {
		container           dockerInspection
		volume, destination string
	}{
		"postgres":     {pg, "leapview-provider-cfo_postgres-data", "/var/lib/postgresql"},
		"home":         {app, "leapview-cfo_leapview-state", "/var/lib/leapview"},
		"caddy-data":   {caddy, "leapview-cfo_caddy-data", "/data"},
		"caddy-config": {caddy, "leapview-cfo_caddy-config", "/config"},
	}
	volumes := map[string]string{}
	for name, want := range expected {
		for _, mount := range want.container.Mounts {
			if mount.Type == "volume" && mount.Name == want.volume && mount.Destination == want.destination && mount.RW {
				volumes[name] = mount.Source
			}
		}
		if volumes[name] == "" {
			return fmt.Errorf("unsupported %s volume binding", name)
		}
	}
	for _, c := range []dockerInspection{app, pg, caddy} {
		for _, mount := range c.Mounts {
			if mount.RW && mount.Type != "tmpfs" {
				known := false
				for _, path := range volumes {
					known = known || path == mount.Source
				}
				if !known {
					return errors.New("unaccounted writable mount")
				}
			}
		}
	}
	env := containerEnv(app)
	if env["LEAPVIEW_MANAGED_DATA_BACKEND"] != "local" || !strings.HasPrefix(env["LEAPVIEW_MANAGED_DATA_DIR"], "/var/lib/leapview/") {
		return errors.New("only contained local managed data is supported")
	}
	for key, database := range map[string]string{"LEAPVIEW_POSTGRES_CONTROL_URL": "leapview_control", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL": "leapview_control", "LEAPVIEW_POSTGRES_DUCKLAKE_URL": "leapview_ducklake"} {
		parsed, err := url.Parse(env[key])
		if err != nil || parsed.Hostname() != nativePG || parsed.Path != "/"+database {
			return errors.New("database binding mismatch")
		}
	}
	raw, err := e.docker(ctx, "exec", nativeApp, "leapview", "version", "--json")
	if err != nil {
		return err
	}
	var version buildinfo.Identity
	if err = json.Unmarshal([]byte(raw), &version); err != nil || version.Dirty || version.Revision != e.request.PredecessorRevision {
		return errors.New("predecessor source identity mismatch")
	}
	// Native cold copies cannot include unobserved external storage roots.
	roots, err := e.pgQuery(ctx, nativePG, "leapview_control", `SELECT COALESCE(json_agg(storage_location),'[]')::text FROM physical_pool.physical_pools`)
	if err != nil {
		return err
	}
	var locations []string
	if err = json.Unmarshal([]byte(roots), &locations); err != nil {
		return errors.New("cannot inspect storage roots")
	}
	for _, location := range locations {
		location = strings.TrimPrefix(location, "file://")
		if !strings.HasPrefix(filepath.Clean(location), "/var/lib/leapview/") {
			return errors.New("external physical storage requires a separately qualified recovery provider")
		}
	}
	current, err := os.Readlink(filepath.Join(e.root, "current"))
	if err != nil {
		return err
	}
	if current != "releases/sha256-"+strings.TrimPrefix(id.Predecessor, "ghcr.io/flidai/leapview@sha256:") {
		return errors.New("installed generation differs from predecessor image")
	}
	e.original = nativeOriginal{App: app, Postgres: pg, Caddy: caddy, Volumes: volumes, Current: current}
	if err = e.checkSpace(); err != nil {
		return err
	}
	if _, err = e.docker(ctx, "pull", id.Candidate); err != nil {
		return err
	}
	raw, err = e.docker(ctx, "run", "--rm", id.Candidate, "version", "--json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(raw), &version); err != nil || version.Dirty || version.Revision != e.request.CandidateRevision {
		return errors.New("candidate source identity mismatch")
	}
	if err = e.stage(ctx); err != nil {
		return err
	}
	configDir := filepath.Join(e.operation, "original-config")
	if err = securefs.EnsurePrivateDir(configDir); err != nil {
		return err
	}
	for _, name := range []string{"deployment.env", "leapview.env", ".host-install.json"} {
		data, err := securefs.ReadPrivateFile(filepath.Join(e.root, name))
		if err != nil {
			return err
		}
		if err = securefs.WritePrivateFileAtomic(filepath.Join(configDir, name), data); err != nil {
			return err
		}
	}
	document, err := json.Marshal(e.original)
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(e.operation, "original.json"), document)
}
func (e *NativeEffects) pgQuery(ctx context.Context, name, database, query string) (string, error) {
	return e.docker(ctx, "exec", name, "sh", "-c", `exec psql -U "$POSTGRES_USER" -d "$1" -At -v ON_ERROR_STOP=1 -c "$2"`, "upgrade", database, query)
}
func (e *NativeEffects) checkSpace() error {
	var size int64
	for _, root := range e.original.Volumes {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				size += info.Size()
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	var space syscall.Statfs_t
	if err := syscall.Statfs(e.provider, &space); err != nil {
		return err
	}
	if uint64(space.Bavail)*uint64(space.Bsize) < uint64(3*size+2*(1<<30)) {
		return errors.New("insufficient free space for backup, isolated restore and retained failed state")
	}
	return nil
}
func (e *NativeEffects) stage(ctx context.Context) error {
	cid, err := e.docker(ctx, "create", e.id.Candidate)
	if err != nil {
		return err
	}
	defer func() { _, _ = e.docker(context.Background(), "rm", "-v", cid) }()
	temporary, err := os.MkdirTemp(filepath.Join(e.root, "releases"), ".upgrade-stage-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if _, err = e.docker(ctx, "cp", cid+":/usr/local/share/leapview/deployment/.", temporary); err != nil {
		return err
	}
	for _, name := range []string{"compose.yaml", "compose.https.yaml", "Caddyfile", "deployment.env.example"} {
		candidate, err := os.ReadFile(filepath.Join(temporary, name))
		if err != nil {
			return err
		}
		installed, err := os.ReadFile(filepath.Join(e.root, name))
		if err != nil || !bytes.Equal(candidate, installed) {
			return fmt.Errorf("deployment payload changed: %s", name)
		}
	}
	release := filepath.Join(e.root, "releases", "sha256-"+strings.TrimPrefix(e.id.Candidate, "ghcr.io/flidai/leapview@sha256:"))
	if _, err = os.Lstat(release); err == nil {
		before, readErr := snapshotTree(ctx, release)
		if readErr != nil {
			return readErr
		}
		candidate, readErr := snapshotTree(ctx, temporary)
		if readErr != nil {
			return readErr
		}
		if !equalEntries(before, candidate) {
			return errors.New("existing candidate release differs from admitted payload")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Rename(temporary, release); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(release))
}
func setDeploymentValue(data []byte, key, value string) ([]byte, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("invalid deployment value")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			if found {
				return nil, errors.New("duplicate deployment key")
			}
			lines[i] = key + "=" + value
			found = true
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}
func (e *NativeEffects) writeDeployment(image string, private bool) error {
	original, err := securefs.ReadPrivateFile(filepath.Join(e.operation, "original-config", "deployment.env"))
	if err != nil {
		return err
	}
	original, err = setDeploymentValue(original, "LEAPVIEW_IMAGE", image)
	if err != nil {
		return err
	}
	if private {
		for key, value := range map[string]string{"CADDY_HTTP_BIND": "127.0.0.1:8082", "CADDY_HTTPS_BIND": "127.0.0.1:8443", "CADDY_HTTPS_UDP_BIND": "127.0.0.1:8443"} {
			original, err = setDeploymentValue(original, key, value)
			if err != nil {
				return err
			}
		}
	}
	return securefs.WritePrivateFileAtomic(filepath.Join(e.root, "deployment.env"), original)
}
func (e *NativeEffects) Quiesce(ctx context.Context) error {
	if err := e.writeDeployment(e.id.Predecessor, true); err != nil {
		return err
	}
	if err := e.compose(ctx, "up", "-d", "--no-deps", "caddy"); err != nil {
		return err
	}
	gate, err := e.inspect(ctx, nativeCaddy)
	if err != nil {
		return err
	}
	for _, bindings := range gate.HostConfig.PortBindings {
		for _, binding := range bindings {
			if binding.HostIP != "127.0.0.1" {
				return errors.New("public traffic did not close")
			}
		}
	}
	for _, name := range []string{nativeApp, nativePG} {
		if _, err = e.docker(ctx, "update", "--restart=no", name); err != nil {
			return err
		}
	}
	if _, err = e.docker(ctx, "stop", "--time", "120", nativeApp); err != nil {
		return err
	}
	jobs, err := e.pgQuery(ctx, nativePG, "leapview_control", `SELECT count(*) FROM public.river_job WHERE state NOT IN ('completed','cancelled','discarded')`)
	if err != nil || jobs != "0" {
		return errors.New("pending jobs require an explicitly drained maintenance window")
	}
	for _, name := range []string{nativeCaddy, nativePG} {
		if _, err = e.docker(ctx, "stop", "--time", "120", name); err != nil {
			return err
		}
	}
	return e.stopped(ctx)
}
func (e *NativeEffects) stopped(ctx context.Context) error {
	for _, name := range []string{nativeApp, nativePG, nativeCaddy} {
		info, err := e.inspect(ctx, name)
		if err != nil {
			return err
		}
		if info.State.Running {
			return errors.New("all snapshot writers must be stopped")
		}
	}
	return nil
}
