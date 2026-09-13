package localdocker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type recordedCommand struct {
	environment []string
	arguments   []string
}

type fakeDocker struct {
	commands []recordedCommand
	active   string
	contexts map[string]string
	serverID string
}

func (docker *fakeDocker) run(_ context.Context, environment []string, arguments ...string) ([]byte, error) {
	docker.commands = append(docker.commands, recordedCommand{
		environment: append([]string(nil), environment...),
		arguments:   append([]string(nil), arguments...),
	})
	if slices.Equal(arguments, []string{"context", "show"}) {
		return []byte(docker.active + "\n"), nil
	}
	if len(arguments) == 5 && arguments[0] == "context" && arguments[1] == "inspect" {
		payload := map[string]any{
			"Name": arguments[2],
			"Endpoints": map[string]any{"docker": map[string]any{
				"Host": docker.contexts[arguments[2]], "SkipTLSVerify": false,
			}},
		}
		return json.Marshal(payload)
	}
	if len(arguments) == 5 && arguments[0] == "--host" && arguments[2] == "info" {
		return json.Marshal(map[string]any{"ID": docker.serverID, "OperatingSystem": "Docker Engine"})
	}
	return nil, nil
}

func TestResolveUsesDockerPrecedenceAndPinsTheEndpoint(t *testing.T) {
	socket := testSocket(t)
	tests := []struct {
		name            string
		explicitContext string
		explicitHost    string
		environment     []string
		active          string
		contexts        map[string]string
		wantContext     string
		wantSource      Source
		wantShow        bool
	}{
		{
			name: "explicit context overrides environment", explicitContext: "explicit",
			environment: []string{"DOCKER_CONTEXT=ambient", "DOCKER_HOST=tcp://remote:2375"},
			contexts:    map[string]string{"explicit": "unix://" + socket},
			wantContext: "explicit", wantSource: SourceExplicitContext,
		},
		{
			name: "explicit host overrides environment", explicitHost: "unix://" + socket,
			environment: []string{"DOCKER_CONTEXT=ambient", "DOCKER_HOST=tcp://remote:2375"},
			wantSource:  SourceExplicitHost,
		},
		{
			name: "DOCKER_CONTEXT overrides DOCKER_HOST", environment: []string{"DOCKER_CONTEXT=desktop", "DOCKER_HOST=tcp://remote:2375"},
			contexts:    map[string]string{"desktop": "unix://" + socket},
			wantContext: "desktop", wantSource: SourceEnvironmentContext,
		},
		{
			name: "DOCKER_HOST overrides active context", environment: []string{"DOCKER_HOST=unix://" + socket},
			active: "remote", contexts: map[string]string{"remote": "tcp://remote:2375"},
			wantSource: SourceEnvironmentHost,
		},
		{
			name: "active context is the fallback", active: "engine",
			contexts:    map[string]string{"engine": "unix://" + socket},
			wantContext: "engine", wantSource: SourceActiveContext, wantShow: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docker := &fakeDocker{active: tc.active, contexts: tc.contexts, serverID: "engine-1"}
			endpoint, err := Resolve(t.Context(), Options{
				Environment: tc.environment, ExplicitContext: tc.explicitContext, ExplicitHost: tc.explicitHost,
				Run: docker.run, trustedSocketPaths: []string{socket},
			})
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.Context() != tc.wantContext || endpoint.Source() != tc.wantSource {
				t.Fatalf("selection = context %q source %q", endpoint.Context(), endpoint.Source())
			}
			if endpoint.Host() != "unix://"+socket || endpoint.ServerID() != "engine-1" {
				t.Fatalf("endpoint = %#v", endpoint)
			}
			var sawShow bool
			for _, command := range docker.commands {
				if slices.Equal(command.arguments, []string{"context", "show"}) {
					sawShow = true
				}
			}
			if sawShow != tc.wantShow {
				t.Fatalf("context show called = %v, want %v", sawShow, tc.wantShow)
			}
			last := docker.commands[len(docker.commands)-1]
			if !slices.Equal(last.arguments, []string{"--host", "unix://" + socket, "info", "--format", "{{json .}}"}) {
				t.Fatalf("probe arguments = %q", last.arguments)
			}
		})
	}
}

func TestResolveRejectsUntrustedEndpointsBeforeContact(t *testing.T) {
	tests := []string{
		"tcp://127.0.0.1:2375",
		"tcp://remote.example:2376",
		"ssh://developer@remote.example",
		"unix:///tmp/docker.sock",
	}
	for _, host := range tests {
		t.Run(host, func(t *testing.T) {
			docker := &fakeDocker{serverID: "must-not-be-contacted"}
			_, err := Resolve(t.Context(), Options{
				Environment: []string{"DOCKER_HOST=" + host}, Run: docker.run,
			})
			if err == nil {
				t.Fatal("untrusted endpoint was accepted")
			}
			if len(docker.commands) != 0 {
				t.Fatalf("untrusted endpoint contacted Docker: %#v", docker.commands)
			}
		})
	}
}

func TestResolveRejectsRemoteContextsBeforeDaemonContact(t *testing.T) {
	tests := []struct {
		name        string
		environment []string
		active      string
	}{
		{name: "DOCKER_CONTEXT", environment: []string{"DOCKER_CONTEXT=default"}},
		{name: "active context", active: "default"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docker := &fakeDocker{
				active: tc.active, contexts: map[string]string{"default": "tcp://127.0.0.1:2375"},
				serverID: "must-not-be-contacted",
			}
			_, err := Resolve(t.Context(), Options{Environment: tc.environment, Run: docker.run})
			if err == nil {
				t.Fatal("loopback remote context was accepted")
			}
			for _, command := range docker.commands {
				if slices.Contains(command.arguments, "info") {
					t.Fatalf("rejected context contacted its daemon: %#v", docker.commands)
				}
			}
		})
	}
}

func TestResolveRedactsRejectedEndpointCredentials(t *testing.T) {
	docker := &fakeDocker{}
	_, err := Resolve(t.Context(), Options{
		Environment: []string{"DOCKER_HOST=tcp://author:super-secret@remote.example:2376/path?token=also-secret"},
		Run:         docker.run,
	})
	if err == nil {
		t.Fatal("credential-bearing endpoint was accepted")
	}
	message := err.Error()
	for _, secret := range []string{"author", "super-secret", "also-secret"} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked %q: %s", secret, message)
		}
	}
}

func TestResolveRedactsOpaqueEndpointText(t *testing.T) {
	docker := &fakeDocker{}
	_, err := Resolve(t.Context(), Options{
		Environment: []string{"DOCKER_HOST=unix:author:super-secret"}, Run: docker.run,
	})
	if err == nil {
		t.Fatal("opaque endpoint was accepted")
	}
	if strings.Contains(err.Error(), "author") || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("opaque endpoint leaked through error: %v", err)
	}
	if len(docker.commands) != 0 {
		t.Fatalf("opaque endpoint contacted Docker: %#v", docker.commands)
	}
}

func TestEndpointEnvironmentRemovesAmbientSelection(t *testing.T) {
	endpoint := Endpoint{host: "unix:///run/user/1000/docker.sock"}
	got := endpoint.Environment([]string{
		"PATH=/usr/bin", "DOCKER_CONTEXT=remote", "DOCKER_HOST=tcp://remote:2375",
		"DOCKER_TLS_VERIFY=1", "DOCKER_CERT_PATH=/secret", "DOCKER_CONFIG=/config",
	})
	values := environmentMap(got)
	if values["DOCKER_HOST"] != endpoint.Host() {
		t.Fatalf("DOCKER_HOST = %q", values["DOCKER_HOST"])
	}
	for _, name := range []string{"DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if _, exists := values[name]; exists {
			t.Fatalf("pinned environment retained %s", name)
		}
	}
	if values["DOCKER_CONFIG"] != "/config" || values["PATH"] != "/usr/bin" {
		t.Fatalf("unrelated environment was not retained: %#v", values)
	}
}

func TestVerifyRejectsChangedDaemonIdentity(t *testing.T) {
	socket := testSocket(t)
	docker := &fakeDocker{serverID: "engine-1"}
	resolver := newResolver(Options{Run: docker.run, trustedSocketPaths: []string{socket}})
	endpoint, err := resolver.resolveHost(t.Context(), "unix://"+socket, "", SourceEnvironmentHost)
	if err != nil {
		t.Fatal(err)
	}
	docker.serverID = "engine-2"
	if err := resolver.Verify(t.Context(), endpoint); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestResolveRejectsExplicitHostAndContextConflictBeforeDocker(t *testing.T) {
	docker := &fakeDocker{}
	_, err := Resolve(t.Context(), Options{
		ExplicitContext: "desktop-linux", ExplicitHost: "unix:///var/run/docker.sock",
		Run: docker.run,
	})
	if err == nil || !strings.Contains(err.Error(), "either") {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(docker.commands) != 0 {
		t.Fatalf("conflicting selection contacted Docker: %#v", docker.commands)
	}
}

func TestTrustedSocketPoliciesAreExact(t *testing.T) {
	linux := newResolver(Options{Platform: "linux", HomeDir: "/home/author", Environment: []string{}})
	for path, want := range map[string]Kind{
		"/var/run/docker.sock":                     KindEngine,
		"/run/docker.sock":                         KindEngine,
		"/home/author/.docker/desktop/docker.sock": KindDesktop,
		filepath.Join("/run/user", strconv.Itoa(linux.uid), "docker.sock"): KindRootless,
	} {
		kind, ok := linux.trustedPath(path)
		if !ok || kind != want {
			t.Errorf("trustedPath(%q) = %q, %v; want %q", path, kind, ok, want)
		}
	}
	for _, path := range []string{"/tmp/docker.sock", "/home/author/docker.sock", "/var/run/other.sock"} {
		if _, ok := linux.trustedPath(path); ok {
			t.Errorf("trustedPath(%q) unexpectedly succeeded", path)
		}
	}

	darwin := newResolver(Options{Platform: "darwin", HomeDir: "/Users/author", Environment: []string{}})
	for _, path := range []string{"/var/run/docker.sock", "/Users/author/.docker/run/docker.sock"} {
		if kind, ok := darwin.trustedPath(path); !ok || kind != KindDesktop {
			t.Errorf("Darwin trustedPath(%q) = %q, %v", path, kind, ok)
		}
	}
}

func TestEndpointFingerprintChangesWithDaemonIdentity(t *testing.T) {
	first := Endpoint{host: "unix:///run/docker.sock", serverID: "one", kind: KindEngine}
	same := Endpoint{host: "unix:///run/docker.sock", serverID: "one", kind: KindEngine}
	changed := Endpoint{host: "unix:///run/docker.sock", serverID: "two", kind: KindEngine}
	if first.Fingerprint() != same.Fingerprint() || first.Fingerprint() == changed.Fingerprint() {
		t.Fatal("endpoint fingerprint does not bind exact daemon identity")
	}
}

func TestResolveLocalDockerIntegration(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_CONTAINERS") != "1" {
		t.Skip("set LEAPVIEW_TEST_CONTAINERS=1 to exercise the installed Docker runtime")
	}
	endpoint, err := Resolve(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host() == "" || endpoint.ServerID() == "" || endpoint.Fingerprint() == "" {
		t.Fatalf("incomplete endpoint identity: %#v", endpoint)
	}
	if err := Verify(t.Context(), endpoint, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestResolvedSocketPathIsPinnedAcrossSymlinkChange(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.sock")
	second := filepath.Join(directory, "second.sock")
	for _, path := range []string{first, second} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
	}
	link := filepath.Join(directory, "docker.sock")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	docker := &fakeDocker{serverID: "engine-1"}
	resolver := newResolver(Options{
		Environment: []string{}, Run: docker.run,
		trustedSocketPaths: []string{link, first, second},
	})
	endpoint, err := resolver.resolveHost(t.Context(), "unix://"+link, "", SourceExplicitHost)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host() != "unix://"+first {
		t.Fatalf("resolved host = %q", endpoint.Host())
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Verify(t.Context(), endpoint); err != nil {
		t.Fatalf("pinned endpoint followed changed symlink: %v", err)
	}
	last := docker.commands[len(docker.commands)-1]
	if !slices.Equal(last.arguments[:2], []string{"--host", "unix://" + first}) {
		t.Fatalf("verification was redirected: %q", last.arguments)
	}
}

func testSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return path
}

func environmentMap(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}
