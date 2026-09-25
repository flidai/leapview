//go:build linux

package hostinstall

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

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

type dockerInspection struct {
	Name string

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

// NativeEffects implements coordinated local PostgreSQL/filesystem recovery.
// Installation selectors come from the request profile; external storage and
// database/extension engine transitions are not supported.
type NativeEffects struct {
	relay          func(context.Context, string, string) (string, func(), error)
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
		return nil, errors.New("host maintenance requires root")
	}
	host, err := os.Hostname()
	if err != nil || host != request.Profile.Hostname {
		return nil, errors.New("host does not match installation profile")
	}
	if err := requireLocalDocker(context.Background()); err != nil {
		return nil, err
	}
	id, err := request.Identity()
	if err != nil {
		return nil, err
	}
	binary := buildinfo.Current()
	if binary.Dirty || binary.Revision != request.CandidateRevision {
		return nil, errors.New("host controller must come from the exact qualified candidate")
	}
	operation := filepath.Join(request.Profile.StateRoot, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"))
	if err = securefs.EnsurePrivateDir(operation); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(operation, "operator.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	e := &NativeEffects{relay: startTCPRelay, root: request.Profile.Root, provider: request.Profile.StateRoot, reader: bufio.NewReader(stdin), request: request, id: id, operation: operation, stdin: stdin, stdout: stdout, log: log}
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
	command, environment, err := composectl.MaintenanceInvocation(e.root, args...)
	if err != nil {
		return err
	}
	if e.execute != nil {
		_, err = e.docker(ctx, command...)
		return err
	}
	process := exec.CommandContext(ctx, "docker", command...)
	process.Dir = e.root
	process.Env = environment
	process.Stdout = e.log
	process.Stderr = e.log
	return process.Run()
}
func (e *NativeEffects) Admit(ctx context.Context, id Identity) error {
	if id != e.id {
		return ErrIdentity
	}
	appName, err := e.service(ctx, e.request.Profile.AppService)
	if err != nil {
		return err
	}
	proxyName, err := e.service(ctx, e.request.Profile.ProxyService)
	if err != nil {
		return err
	}
	app, err := e.inspect(ctx, appName)
	if err != nil {
		return err
	}
	pg, err := e.inspect(ctx, e.request.Profile.Postgres)
	if err != nil {
		return err
	}
	caddy, err := e.inspect(ctx, proxyName)
	if err != nil {
		return err
	}
	e.original.App.Name = appName
	e.original.Caddy.Name = proxyName
	app.Name = appName
	caddy.Name = proxyName
	if !app.State.Running || !pg.State.Running || !caddy.State.Running || app.Config.Image != id.Predecessor || pg.Config.Image != e.request.Profile.PostgresImage || app.Config.Labels["com.docker.compose.project"] != e.request.Profile.Project || app.Config.Labels["com.docker.compose.project.working_dir"] != e.root {
		return errors.New("unexpected predecessor deployment")
	}
	for _, c := range []dockerInspection{app, pg, caddy} {
		if len(c.NetworkSettings.Networks) != 1 || c.NetworkSettings.Networks[e.request.Profile.Network] == nil || c.HostConfig.RestartPolicy.Name != "unless-stopped" {
			return errors.New("unsupported network or restart topology")
		}
	}
	members, err := e.docker(ctx, "network", "inspect", e.request.Profile.Network, "--format", "{{len .Containers}}")
	if err != nil || members != "3" {
		return errors.New("unexpected network participants; cannot fence all writers")
	}
	if !strings.Contains(caddy.Config.Image, "@sha256:") {
		return errors.New("Caddy must be digest pinned")
	}
	if containerEnv(pg)["PG_MAJOR"] != "18" {
		return errors.New("physical recovery requires PostgreSQL 18")
	}
	for _, bindings := range app.HostConfig.PortBindings {
		for _, binding := range bindings {
			if binding.HostIP != "127.0.0.1" && binding.HostIP != "::1" {
				return errors.New("direct application ports must remain loopback-only")
			}
		}
	}
	if len(pg.HostConfig.PortBindings) != 0 {
		return errors.New("externally published PostgreSQL is unsupported")
	}
	expected := map[string]struct {
		container           dockerInspection
		volume, destination string
	}{
		"postgres":     {pg, e.request.Profile.Volumes["postgres"], "/var/lib/postgresql"},
		"home":         {app, e.request.Profile.Volumes["home"], "/var/lib/leapview"},
		"caddy-data":   {caddy, e.request.Profile.Volumes["caddy-data"], "/data"},
		"caddy-config": {caddy, e.request.Profile.Volumes["caddy-config"], "/config"},
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
	for _, volume := range volumes {
		resolvedVolume, err := filepath.EvalSymlinks(volume)
		if err != nil {
			return err
		}
		for _, path := range []string{e.root, e.provider} {
			resolvedPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			volume, path = resolvedVolume, resolvedPath
			if path == volume || strings.HasPrefix(path, volume+"/") || strings.HasPrefix(volume, path+"/") {
				return errors.New("recovery journal and installation must be outside restored volumes")
			}
		}
	}
	markerRaw, err := securefs.ReadPrivateFile(filepath.Join(e.root, ".host-install.json"))
	if err != nil {
		return err
	}
	var marker Config
	if json.Unmarshal(markerRaw, &marker) != nil || marker.Image != id.Predecessor {
		return errors.New("installation marker differs from predecessor")
	}
	origin, _ := url.Parse(e.request.Profile.Origin)
	if marker.Domain != origin.Hostname() || containerEnv(app)["LEAPVIEW_PUBLIC_URL"] != e.request.Profile.Origin {
		return errors.New("installation public origin mismatch")
	}
	deployment, err := securefs.ReadPrivateFile(filepath.Join(e.root, "deployment.env"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(deployment), "COMPOSE_PROJECT_NAME="+e.request.Profile.Project+"\n") {
		return errors.New("Compose project binding mismatch")
	}
	tablespaces, err := e.pgQuery(ctx, e.request.Profile.Postgres, "leapview_control", `SELECT count(*) FROM pg_tablespace WHERE pg_tablespace_location(oid) <> ''`)
	if err != nil || tablespaces != "0" {
		return errors.New("external PostgreSQL tablespaces are unsupported")
	}
	env := containerEnv(app)
	if env["LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH"] != "/usr/local/share/leapview/extensions/extension-supply.json" {
		return errors.New("custom runtime extension supply is unsupported")
	}
	for _, mount := range app.Mounts {
		if strings.HasPrefix(mount.Destination, "/usr/local/share/leapview/extensions") {
			return errors.New("mounted extension supply is unsupported")
		}
	}
	if env["LEAPVIEW_MANAGED_DATA_BACKEND"] != "local" || !strings.HasPrefix(env["LEAPVIEW_MANAGED_DATA_DIR"], "/var/lib/leapview/") {
		return errors.New("only contained local managed data is supported")
	}
	for key, database := range map[string]string{"LEAPVIEW_POSTGRES_CONTROL_URL": "leapview_control", "LEAPVIEW_POSTGRES_DUCKLAKE_URL": "leapview_ducklake"} {
		parsed, err := url.Parse(env[key])
		if err != nil || parsed.Hostname() != e.request.Profile.Postgres || parsed.Path != "/"+database {
			return errors.New("database binding mismatch")
		}
	}
	migrator, err := e.migratorURL(app)
	if err != nil {
		return err
	}
	parsedMigrator, err := url.Parse(migrator)
	if err != nil || parsedMigrator.Hostname() != e.request.Profile.Postgres || parsedMigrator.Path != "/leapview_control" || parsedMigrator.User == nil || parsedMigrator.User.Username() != "leapview_control_migrator" || parsedMigrator.Query().Get("sslmode") != "verify-full" {
		return errors.New("control migrator identity and verified TLS are required")
	}
	if _, err := migrationTLSMounts(migrator, app); err != nil {
		return err
	}
	raw, err := e.docker(ctx, "exec", e.app(), "leapview", "version", "--json")
	if err != nil {
		return err
	}
	var version buildinfo.Identity
	if err = json.Unmarshal([]byte(raw), &version); err != nil || version.Dirty || version.Revision != e.request.PredecessorRevision {
		return errors.New("predecessor source identity mismatch")
	}
	// Native cold copies cannot include unobserved external storage roots.
	roots, err := e.pgQuery(ctx, e.request.Profile.Postgres, "leapview_control", `SELECT COALESCE(json_agg(storage_location),'[]')::text FROM physical_pool.physical_pools`)
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
	if err = validateImageSupplies(ctx, e.request); err != nil {
		return err
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
	if _, err = e.candidateEnvironment(); err != nil {
		return err
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
	payload, err := extractCandidatePayload(ctx, "docker", e.id.Candidate, e.log)
	if err != nil {
		return err
	}
	for _, name := range []string{"compose.yaml", "compose.https.yaml", "Caddyfile", "deployment.env.example"} {
		installed, err := os.ReadFile(filepath.Join(e.root, name))
		if err != nil || !bytes.Equal(installed, payload[name]) {
			return fmt.Errorf("deployment topology changed: %s", name)
		}
	}
	_, err = stageGeneration(InstalledPaths(e.root), e.id.Candidate, payload)
	return err
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
		for key, value := range map[string]string{"CADDY_HTTP_BIND": e.request.Profile.HTTPBinding, "CADDY_HTTPS_BIND": e.request.Profile.HTTPSBinding, "CADDY_HTTPS_UDP_BIND": e.request.Profile.HTTPSBinding} {
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
	if err := e.compose(ctx, "up", "-d", "--no-deps", e.request.Profile.ProxyService); err != nil {
		return err
	}
	gate, err := e.inspect(ctx, e.proxy())
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
	for _, name := range []string{e.app(), e.request.Profile.Postgres} {
		if _, err = e.docker(ctx, "update", "--restart=no", name); err != nil {
			return err
		}
	}
	if _, err = e.docker(ctx, "stop", "--time", "120", e.app()); err != nil {
		return err
	}
	jobs, err := e.pgQuery(ctx, e.request.Profile.Postgres, "leapview_control", `SELECT count(*) FROM public.river_job WHERE state NOT IN ('completed','cancelled','discarded')`)
	if err != nil || jobs != "0" {
		return errors.New("pending jobs require an explicitly drained maintenance window")
	}
	for _, name := range []string{e.proxy(), e.request.Profile.Postgres} {
		if _, err = e.docker(ctx, "stop", "--time", "120", name); err != nil {
			return err
		}
	}
	return e.stopped(ctx)
}
func (e *NativeEffects) stopped(ctx context.Context) error {
	for _, name := range []string{e.app(), e.request.Profile.Postgres, e.proxy()} {
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

func (e *NativeEffects) app() string   { return strings.TrimPrefix(e.original.App.Name, "/") }
func (e *NativeEffects) proxy() string { return strings.TrimPrefix(e.original.Caddy.Name, "/") }
func (e *NativeEffects) service(ctx context.Context, service string) (string, error) {
	value, err := e.docker(ctx, "ps", "-a", "--filter", "label=com.docker.compose.project="+e.request.Profile.Project, "--filter", "label=com.docker.compose.service="+service, "--format", "{{.Names}}")
	if err != nil {
		return "", err
	}
	if !dockerSelector.MatchString(value) {
		return "", errors.New("expected exactly one installed service container")
	}
	return value, nil
}
