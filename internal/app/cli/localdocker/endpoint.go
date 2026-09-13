// Package localdocker resolves and verifies the Docker endpoint used by local
// analytics development. It deliberately accepts only documented local Unix
// sockets and returns an endpoint that can be pinned for every later command.
package localdocker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

const dockerInfoFormat = "{{json .}}"

type Source string

const (
	SourceExplicitContext    Source = "explicit-context"
	SourceExplicitHost       Source = "explicit-host"
	SourceEnvironmentContext Source = "DOCKER_CONTEXT"
	SourceEnvironmentHost    Source = "DOCKER_HOST"
	SourceActiveContext      Source = "active-context"
)

type Kind string

const (
	KindEngine   Kind = "engine"
	KindRootless Kind = "rootless-engine"
	KindDesktop  Kind = "docker-desktop"
)

// Endpoint is the immutable result of local endpoint selection. Host is a
// credential-free canonical Unix socket URI and ServerID identifies the daemon
// reached through that trusted socket at selection time.
type Endpoint struct {
	context    string
	host       string
	socketPath string
	serverID   string
	source     Source
	kind       Kind
	verify     func(context.Context) error
}

func (endpoint Endpoint) Context() string  { return endpoint.context }
func (endpoint Endpoint) Host() string     { return endpoint.host }
func (endpoint Endpoint) ServerID() string { return endpoint.serverID }
func (endpoint Endpoint) Source() Source   { return endpoint.source }
func (endpoint Endpoint) Kind() Kind       { return endpoint.kind }

func (endpoint Endpoint) Verify(ctx context.Context) error {
	if endpoint.verify == nil {
		return errors.New("Docker endpoint has no verification authority")
	}
	return endpoint.verify(ctx)
}

// Fingerprint is a credential-free resource-label identity for the selected
// socket and daemon. It changes when either authority changes.
func (endpoint Endpoint) Fingerprint() string {
	identity := endpoint.host + "\x00" + endpoint.serverID + "\x00" + string(endpoint.kind)
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(identity)))
}

// DockerArguments pins a Docker CLI invocation independently of later context
// or environment changes.
func (endpoint Endpoint) DockerArguments(arguments ...string) []string {
	return append([]string{"--host", endpoint.host}, arguments...)
}

// Environment removes ambient endpoint and TLS selection, then pins the
// verified Unix endpoint. DOCKER_CONFIG remains available for registry auth.
func (endpoint Endpoint) Environment(base []string) []string {
	result := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, _, present := strings.Cut(entry, "=")
		if !present || slices.Contains([]string{
			"DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
		}, name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "DOCKER_HOST="+endpoint.host)
}

type commandRunner func(context.Context, []string, ...string) ([]byte, error)

type Options struct {
	DockerBin       string
	Environment     []string
	ExplicitContext string
	ExplicitHost    string
	Run             commandRunner
	Platform        string
	HomeDir         string

	trustedSocketPaths []string
	evaluateSymlinks   func(string) (string, error)
	stat               func(string) (os.FileInfo, error)
}

type resolver struct {
	environment        []string
	explicitContext    string
	explicitHost       string
	run                commandRunner
	platform           string
	homeDir            string
	uid                int
	trustedSocketPaths []string
	evaluateSymlinks   func(string) (string, error)
	stat               func(string) (os.FileInfo, error)
}

type contextDescription struct {
	Name        string              `json:"Name"`
	TLSMaterial map[string][]string `json:"TLSMaterial"`
	Endpoints   struct {
		Docker struct {
			Host          string `json:"Host"`
			SkipTLSVerify bool   `json:"SkipTLSVerify"`
		} `json:"docker"`
	} `json:"Endpoints"`
}

type serverDescription struct {
	ID string `json:"ID"`
}

func Resolve(ctx context.Context, options Options) (Endpoint, error) {
	return newResolver(options).Resolve(ctx)
}

// Verify rechecks socket trust and exact daemon identity without falling back
// to ambient Docker selection.
func Verify(ctx context.Context, endpoint Endpoint, options Options) error {
	return newResolver(options).Verify(ctx, endpoint)
}

func newResolver(options Options) *resolver {
	dockerBin := strings.TrimSpace(options.DockerBin)
	if dockerBin == "" {
		dockerBin = "docker"
	}
	environment := append([]string(nil), options.Environment...)
	if options.Environment == nil {
		environment = os.Environ()
	}
	platform := strings.TrimSpace(options.Platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	homeDir := strings.TrimSpace(options.HomeDir)
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	run := options.Run
	if run == nil {
		run = func(ctx context.Context, environment []string, arguments ...string) ([]byte, error) {
			command := exec.CommandContext(ctx, dockerBin, arguments...)
			command.Env = environment
			output, err := command.Output()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return nil, fmt.Errorf("Docker command failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
				}
				return nil, fmt.Errorf("run Docker command: %w", err)
			}
			return output, nil
		}
	}
	result := &resolver{
		environment:     environment,
		explicitContext: options.ExplicitContext, explicitHost: options.ExplicitHost, run: run,
		platform: platform, homeDir: homeDir, uid: platformUID(),
		trustedSocketPaths: append([]string(nil), options.trustedSocketPaths...),
		evaluateSymlinks:   options.evaluateSymlinks,
		stat:               options.stat,
	}
	if result.evaluateSymlinks == nil {
		result.evaluateSymlinks = filepath.EvalSymlinks
	}
	if result.stat == nil {
		result.stat = os.Stat
	}
	return result
}

func (resolver *resolver) Resolve(ctx context.Context) (Endpoint, error) {
	if resolver.platform != "linux" && resolver.platform != "darwin" {
		return Endpoint{}, fmt.Errorf("local Docker development is unsupported on %s", resolver.platform)
	}
	if resolver.explicitContext != "" && resolver.explicitHost != "" {
		return Endpoint{}, errors.New("choose either an explicit Docker context or host, not both")
	}
	if resolver.explicitContext != "" {
		if resolver.explicitContext != strings.TrimSpace(resolver.explicitContext) {
			return Endpoint{}, errors.New("explicit Docker context contains surrounding whitespace")
		}
		return resolver.resolveContext(ctx, resolver.explicitContext, SourceExplicitContext)
	}
	if resolver.explicitHost != "" {
		if resolver.explicitHost != strings.TrimSpace(resolver.explicitHost) {
			return Endpoint{}, errors.New("explicit Docker host contains surrounding whitespace")
		}
		return resolver.resolveHost(ctx, resolver.explicitHost, "", SourceExplicitHost)
	}
	if raw := resolver.optionValue("DOCKER_CONTEXT"); raw != "" {
		if raw != strings.TrimSpace(raw) {
			return Endpoint{}, errors.New("DOCKER_CONTEXT contains surrounding whitespace")
		}
		return resolver.resolveContext(ctx, raw, SourceEnvironmentContext)
	}
	if raw := resolver.optionValue("DOCKER_HOST"); raw != "" {
		if raw != strings.TrimSpace(raw) {
			return Endpoint{}, errors.New("DOCKER_HOST contains surrounding whitespace")
		}
		return resolver.resolveHost(ctx, raw, "", SourceEnvironmentHost)
	}
	output, err := resolver.run(ctx, withoutDockerSelection(resolver.environment), "context", "show")
	if err != nil {
		return Endpoint{}, fmt.Errorf("resolve active Docker context: %w", err)
	}
	name := strings.TrimSpace(string(output))
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return Endpoint{}, errors.New("Docker returned an invalid active context")
	}
	return resolver.resolveContext(ctx, name, SourceActiveContext)
}

func (resolver *resolver) resolveContext(ctx context.Context, name string, source Source) (Endpoint, error) {
	if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "\r\n") {
		return Endpoint{}, errors.New("Docker context selection is invalid")
	}
	output, err := resolver.run(
		ctx, withoutDockerSelection(resolver.environment),
		"context", "inspect", name, "--format", dockerInfoFormat,
	)
	if err != nil {
		return Endpoint{}, fmt.Errorf("inspect Docker context %q: %w", name, err)
	}
	var description contextDescription
	if err := decodeOneJSON(output, &description); err != nil {
		return Endpoint{}, fmt.Errorf("inspect Docker context %q: %w", name, err)
	}
	if description.Name != name {
		return Endpoint{}, fmt.Errorf("Docker context inspection returned %q instead of %q", description.Name, name)
	}
	if description.Endpoints.Docker.SkipTLSVerify {
		return Endpoint{}, fmt.Errorf("Docker context %q uses unsupported TLS endpoint settings", name)
	}
	if len(description.TLSMaterial) != 0 {
		return Endpoint{}, fmt.Errorf("Docker context %q contains unsupported TLS material", name)
	}
	return resolver.resolveHost(ctx, description.Endpoints.Docker.Host, name, source)
}

func (resolver *resolver) resolveHost(ctx context.Context, rawHost, contextName string, source Source) (Endpoint, error) {
	host, socketPath, kind, err := resolver.verifyLocalSocket(rawHost)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint := Endpoint{context: contextName, host: host, socketPath: socketPath, source: source, kind: kind}
	serverID, err := resolver.probe(ctx, endpoint)
	if err != nil {
		return Endpoint{}, fmt.Errorf("inspect verified local Docker endpoint %s: %w", redactedEndpoint(host), err)
	}
	endpoint.serverID = serverID
	endpoint.verify = func(verifyContext context.Context) error {
		return resolver.Verify(verifyContext, endpoint)
	}
	return endpoint, nil
}

func (resolver *resolver) Verify(ctx context.Context, endpoint Endpoint) error {
	host, socketPath, kind, err := resolver.verifyLocalSocket(endpoint.host)
	if err != nil {
		return err
	}
	if host != endpoint.host || socketPath != endpoint.socketPath || kind != endpoint.kind {
		return errors.New("verified Docker endpoint no longer matches its recorded identity")
	}
	serverID, err := resolver.probe(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("reverify local Docker endpoint: %w", err)
	}
	if serverID != endpoint.serverID {
		return fmt.Errorf("Docker daemon identity changed from %q to %q", endpoint.serverID, serverID)
	}
	return nil
}

func (resolver *resolver) probe(ctx context.Context, endpoint Endpoint) (string, error) {
	output, err := resolver.run(
		ctx, endpoint.Environment(resolver.environment),
		endpoint.DockerArguments("info", "--format", dockerInfoFormat)...,
	)
	if err != nil {
		return "", err
	}
	var description serverDescription
	if err := decodeOneJSON(output, &description); err != nil {
		return "", err
	}
	if description.ID == "" || description.ID != strings.TrimSpace(description.ID) {
		return "", errors.New("Docker daemon returned no stable server identity")
	}
	return description.ID, nil
}

func (resolver *resolver) verifyLocalSocket(rawHost string) (string, string, Kind, error) {
	parsed, err := url.Parse(rawHost)
	if err != nil {
		return "", "", "", errors.New("Docker endpoint is malformed")
	}
	if parsed.User != nil {
		return "", "", "", errors.New("Docker endpoint contains credentials and is not supported")
	}
	if parsed.Scheme != "unix" {
		return "", "", "", fmt.Errorf("Docker endpoint %s uses an unsupported local-development transport", redactedEndpoint(rawHost))
	}
	if parsed.Host != "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", "", "", fmt.Errorf("Docker endpoint %s is not a canonical Unix socket", redactedEndpoint(rawHost))
	}
	path := filepath.Clean(parsed.Path)
	if path == "." || !filepath.IsAbs(path) {
		return "", "", "", errors.New("Docker Unix socket path must be absolute")
	}
	kind, trusted := resolver.trustedPath(path)
	if !trusted {
		return "", "", "", fmt.Errorf("Docker endpoint %s is not a recognized local Engine or Docker Desktop socket", redactedEndpoint(rawHost))
	}
	resolved, err := resolver.evaluateSymlinks(path)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve Docker socket %q: %w", path, err)
	}
	resolved = filepath.Clean(resolved)
	resolvedKind, resolvedTrusted := resolver.trustedPath(resolved)
	if !resolvedTrusted || resolvedKind != kind {
		return "", "", "", fmt.Errorf("Docker socket %q resolves outside recognized local runtime paths", path)
	}
	info, err := resolver.stat(resolved)
	if err != nil {
		return "", "", "", fmt.Errorf("inspect Docker socket %q: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return "", "", "", fmt.Errorf("Docker endpoint %q is not a Unix socket", path)
	}
	owner, ownerKnown := fileOwnerUID(info)
	if ownerKnown && !resolver.trustedOwner(kind, owner) {
		return "", "", "", fmt.Errorf("Docker socket %q has unexpected owner uid %d", path, owner)
	}
	return "unix://" + resolved, resolved, resolvedKind, nil
}

func (resolver *resolver) trustedPath(path string) (Kind, bool) {
	for _, trusted := range resolver.trustedSocketPaths {
		if filepath.Clean(trusted) == path {
			return KindRootless, true
		}
	}
	if path == "/var/run/docker.sock" || path == "/run/docker.sock" {
		if resolver.platform == "darwin" {
			return KindDesktop, true
		}
		return KindEngine, true
	}
	if resolver.homeDir != "" {
		if resolver.platform == "linux" && path == filepath.Join(resolver.homeDir, ".docker", "desktop", "docker.sock") {
			return KindDesktop, true
		}
		if resolver.platform == "darwin" && path == filepath.Join(resolver.homeDir, ".docker", "run", "docker.sock") {
			return KindDesktop, true
		}
	}
	if resolver.platform == "linux" && resolver.uid >= 0 && path == filepath.Join("/run/user", strconv.Itoa(resolver.uid), "docker.sock") {
		return KindRootless, true
	}
	return "", false
}

func (resolver *resolver) trustedOwner(kind Kind, owner int) bool {
	if resolver.uid < 0 {
		return false
	}
	if kind == KindEngine && resolver.platform == "linux" {
		return owner == 0
	}
	return owner == resolver.uid || (resolver.platform == "darwin" && owner == 0)
}

func (resolver *resolver) optionValue(name string) string {
	var value string
	for _, entry := range resolver.environment {
		entryName, entryValue, present := strings.Cut(entry, "=")
		if present && entryName == name {
			value = entryValue
		}
	}
	return value
}

func withoutDockerSelection(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, present := strings.Cut(entry, "=")
		if present && slices.Contains([]string{
			"DOCKER_CONTEXT", "DOCKER_HOST", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
		}, name) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func decodeOneJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Docker response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("Docker response contains more than one JSON value")
}

func redactedEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	if parsed.Opaque != "" {
		if parsed.Scheme == "" {
			return "<redacted>"
		}
		return parsed.Scheme + ":<redacted>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}
