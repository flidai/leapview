package composectl

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/stretchr/testify/require"
)

const (
	v010TestContainerID          = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	v010CandidateTestContainerID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	v010CandidateTestImageID     = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
)

type v010DockerFixture struct {
	t                        *testing.T
	index                    []byte
	manifest                 []byte
	requests                 [][]string
	stopped                  bool
	imageOS                  string
	imageArch                string
	inspectMutate            func(*v010ContainerInspect)
	rootError                error
	readinessError           bool
	bootstrapError           bool
	authenticationError      bool
	workloadMismatch         bool
	projectVerified          bool
	restarted                bool
	missingPrincipal         bool
	alteredProject           bool
	alteredManagedData       bool
	missingPublish           bool
	candidateImage           string
	candidateVersion         string
	candidateRevision        string
	candidateStopped         bool
	candidateInitialized     bool
	candidateLegacyVisible   bool
	candidateDenialMutates   bool
	candidateDenialAttempted bool
}

func newV010DockerFixture(t *testing.T) *v010DockerFixture {
	t.Helper()
	policy, err := compatibility.ParsePolicy(v010CandidateBoundTestPolicy(t))
	require.NoError(t, err)
	candidate, ok := policy.ReleaseByID(policy.CandidateRelease)
	require.True(t, ok)
	identity := candidate.IdentityForPlatform(compatibility.ReleasedV010Platform)
	return &v010DockerFixture{
		t:                 t,
		index:             readExactV010RegistryFixture(t, "testdata/v010-oci-index.json"),
		manifest:          readExactV010RegistryFixture(t, "testdata/v010-oci-manifest.json"),
		imageOS:           "linux",
		imageArch:         "amd64",
		candidateImage:    identity.Image,
		candidateVersion:  identity.Version,
		candidateRevision: identity.SourceRevision,
	}
}

func v010CandidateBoundTestPolicy(t *testing.T) []byte {
	t.Helper()
	base, err := compatibility.EmbeddedPolicy()
	require.NoError(t, err)
	bound, err := base.BindCandidate(compatibility.ReleaseIdentity{
		Version: "0.2.0-rc.2", SourceRevision: strings.Repeat("4", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("4", 64), Distribution: "public",
	}, []string{"linux/amd64", "linux/arm64"})
	require.NoError(t, err)
	document, err := compatibility.MarshalPolicy(bound)
	require.NoError(t, err)
	return document
}

func (fixture *v010DockerFixture) execute(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
	arguments := append([]string(nil), request.Arguments...)
	fixture.requests = append(fixture.requests, arguments)
	switch {
	case slices.Equal(arguments, []string{"buildx", "imagetools", "inspect", "--raw", compatibility.ReleasedV010Image}):
		if fixture.rootError != nil {
			return nil, fixture.rootError
		}
		return append([]byte(nil), fixture.index...), nil
	case slices.Equal(arguments, []string{"buildx", "imagetools", "inspect", "--raw", compatibility.ReleasedV010Repository + "@" + compatibility.ReleasedV010PlatformManifest}):
		return append([]byte(nil), fixture.manifest...), nil
	case slices.Equal(arguments, []string{"pull", "--platform", compatibility.ReleasedV010Platform, compatibility.ReleasedV010Image}):
		return []byte("pulled exact digest\n"), nil
	case slices.Equal(arguments, []string{"image", "inspect", "--format", "{{json .}}", compatibility.ReleasedV010Image}):
		return json.Marshal(map[string]any{
			"Id": compatibility.ReleasedV010ConfigDigest, "RepoDigests": []string{compatibility.ReleasedV010Image},
			"Architecture": fixture.imageArch, "Os": fixture.imageOS,
			"Config": map[string]any{"Labels": map[string]string{
				"org.opencontainers.image.source":   compatibility.ReleasedV010SourceRepository,
				"org.opencontainers.image.version":  "0.1.0",
				"org.opencontainers.image.revision": "5bf4aded574df459e80d81b77d1989ecd4fa7de0",
			}},
		})
	case slices.Equal(arguments, []string{"pull", "--platform", compatibility.ReleasedV010Platform, fixture.candidateImage}):
		return []byte("pulled admitted candidate\n"), nil
	case slices.Equal(arguments, []string{"image", "inspect", "--format", "{{json .}}", fixture.candidateImage}):
		return json.Marshal(map[string]any{
			"Id": v010CandidateTestImageID, "RepoDigests": []string{fixture.candidateImage},
			"Architecture": "amd64", "Os": "linux",
			"Config": map[string]any{"Labels": map[string]string{
				"org.opencontainers.image.version":  fixture.candidateVersion,
				"org.opencontainers.image.revision": fixture.candidateRevision,
			}},
		})
	case len(arguments) > 0 && arguments[0] == "run" && slices.Contains(arguments, fixture.candidateImage):
		return fixture.executeCandidateRun(arguments)
	case len(arguments) > 0 && arguments[0] == "exec" && slices.Contains(arguments, "leapview"):
		return fixture.executeCandidateCLI(arguments)
	case len(arguments) > 0 && arguments[0] == "run":
		if err := fixture.verifyV010Project(); err != nil {
			return nil, err
		}
		return []byte(v010TestContainerID + "\n"), nil
	case len(arguments) > 0 && arguments[0] == "exec":
		return fixture.executeV010CLI(arguments)
	case len(arguments) > 0 && arguments[0] == "logs":
		return []byte("bounded v0.1 server diagnostics\n"), nil
	case len(arguments) > 0 && arguments[0] == "inspect":
		name := arguments[len(arguments)-1]
		if strings.HasPrefix(name, "leapview-v010-candidate-") {
			return fixture.candidateContainerInspect(name)
		}
		return fixture.containerInspect(name)
	case len(arguments) > 0 && arguments[0] == "stop":
		if strings.HasPrefix(arguments[len(arguments)-1], "leapview-v010-candidate-") {
			fixture.candidateStopped = true
			return []byte(arguments[len(arguments)-1] + "\n"), nil
		}
		fixture.stopped = true
		return []byte(arguments[len(arguments)-1] + "\n"), nil
	case len(arguments) > 0 && arguments[0] == "start":
		fixture.stopped = false
		fixture.restarted = true
		return []byte(arguments[len(arguments)-1] + "\n"), nil
	case len(arguments) > 0 && (arguments[0] == "ps" ||
		(len(arguments) > 1 && arguments[1] == "ls")):
		return nil, nil
	default:
		return []byte("ok\n"), nil
	}
}

func (fixture *v010DockerFixture) verifyV010Project() error {
	root := fixture.runDirectory()
	files := []struct {
		path string
		want string
	}{
		{path: "libredash.yaml", want: "kind: Project"},
		{path: "data/orders.csv", want: "o-003,delivered"},
		{path: "workspaces/compatibility/dashboards/preservation.yaml", want: "name: preservation"},
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		document, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(document), file.want) {
			return fmt.Errorf("v0.1 project fixture %s omitted %q", file.path, file.want)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o044 == 0 {
			return fmt.Errorf("v0.1 project fixture %s is unreadable by the released image", file.path)
		}
	}
	fixture.projectVerified = true
	return nil
}

func (fixture *v010DockerFixture) executeCandidateRun(arguments []string) ([]byte, error) {
	joined := strings.Join(arguments, " ")
	isCandidateVolume := strings.Contains(joined, "source=leapview-v010-candidate-")
	isPredecessorVolume := strings.Contains(joined, "source=leapview-v010-") && !isCandidateVolume
	if slices.Contains(arguments, "tar") {
		switch {
		case isCandidateVolume && fixture.candidateInitialized:
			if fixture.candidateDenialMutates && fixture.candidateDenialAttempted {
				return v010TestStateArchive(fixture.t, "candidate-state-mutated-by-denial"), nil
			}
			return v010TestStateArchive(fixture.t, "candidate-initialized-state"), nil
		case isCandidateVolume:
			return v010TestStateArchive(fixture.t, ""), nil
		case isPredecessorVolume:
			if fixture.candidateDenialMutates && fixture.candidateDenialAttempted {
				return v010TestStateArchive(fixture.t, "predecessor-state-mutated-by-denial"), nil
			}
			return v010TestStateArchive(fixture.t, "preserved-v010-state"), nil
		default:
			return nil, errors.New("unknown qualification volume")
		}
	}
	if strings.Contains(joined, " admin initialize --format json") {
		if isPredecessorVolume {
			fixture.candidateDenialAttempted = true
			return []byte(compatibility.ErrV010FreshInstallOnly.Error()), compatibility.ErrV010FreshInstallOnly
		}
		fixture.candidateInitialized = true
		return []byte(`{"email":"fai-517-candidate@qualification.invalid","temporaryPassword":"transient","publisherToken":"candidate-publisher-token","publisherTokenExpiresAt":"2026-07-14T15:45:00Z"}`), nil
	}
	if slices.Contains(arguments, "--detach") {
		return []byte(v010CandidateTestContainerID + "\n"), nil
	}
	return nil, fmt.Errorf("unexpected candidate run %v", arguments)
}

func v010TestStateArchive(t *testing.T, contents string) []byte {
	t.Helper()
	var document bytes.Buffer
	writer := tar.NewWriter(&document)
	if contents != "" {
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name: "state", Mode: 0o600, Size: int64(len(contents)), Typeflag: tar.TypeReg,
		}))
		_, err := writer.Write([]byte(contents))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return document.Bytes()
}

func (fixture *v010DockerFixture) executeCandidateCLI(arguments []string) ([]byte, error) {
	joined := strings.Join(arguments, " ")
	switch {
	case strings.Contains(joined, "listPrincipals") && strings.Contains(joined, v010CandidateAdminEmail):
		return []byte(`{"items":[{"id":"candidate_admin","email":"fai-517-candidate@qualification.invalid"}],"page":{}}`), nil
	case strings.Contains(joined, "listPrincipals"):
		if fixture.candidateLegacyVisible {
			return []byte(`{"items":[{"id":"legacy_principal","email":"fai-517-user@qualification.invalid"}],"page":{}}`), nil
		}
		return []byte(`{"items":[],"page":{}}`), nil
	case strings.Contains(joined, "listDashboards"):
		if fixture.candidateLegacyVisible {
			return []byte(`{"items":[{"id":"preservation"}],"page":{}}`), nil
		}
		return []byte(`{"items":[],"page":{}}`), nil
	case strings.Contains(joined, "listSemanticModels"):
		return []byte(`{"items":[],"page":{}}`), nil
	default:
		return nil, fmt.Errorf("unexpected candidate CLI command %v", arguments)
	}
}

func (fixture *v010DockerFixture) candidateContainerInspect(name string) ([]byte, error) {
	network := name + "-network"
	volume := name + "-state"
	status := "running"
	running := true
	if fixture.candidateStopped {
		status = "exited"
		running = false
	}
	return json.Marshal(map[string]any{
		"Id": v010CandidateTestContainerID, "Name": "/" + name, "Image": v010CandidateTestImageID,
		"State":      map[string]any{"Status": status, "Running": running},
		"Config":     map[string]any{"Image": fixture.candidateImage},
		"HostConfig": map[string]any{"NetworkMode": network},
		"Mounts": []map[string]any{
			{"Type": "volume", "Name": volume, "Source": "/var/lib/docker/volumes/" + volume, "Destination": v010CandidateStateMount, "RW": true},
			{"Type": "bind", "Source": fixture.candidateRunDirectory(), "Destination": v010CandidateRunMount, "RW": false},
		},
		"NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]any{}}},
	})
}

func (fixture *v010DockerFixture) candidateRunDirectory() string {
	for _, arguments := range fixture.requests {
		for _, argument := range arguments {
			if !strings.Contains(argument, ".v010-candidate-run-") || !strings.Contains(argument, "source=") {
				continue
			}
			for _, part := range strings.Split(argument, ",") {
				if strings.HasPrefix(part, "source=") {
					return strings.TrimPrefix(part, "source=")
				}
			}
		}
	}
	return ""
}

func (fixture *v010DockerFixture) executeV010CLI(arguments []string) ([]byte, error) {
	binary := slices.Index(arguments, "libredash")
	if binary < 0 || binary+1 >= len(arguments) {
		return nil, errors.New("missing v0.1 CLI")
	}
	command := arguments[binary+1:]
	switch {
	case slices.Equal(command, []string{"healthcheck", "--timeout", "5s"}):
		if fixture.readinessError {
			return nil, errors.New("connection refused")
		}
		return []byte("ready\n"), nil
	case slices.Equal(command, []string{"admin", "bootstrap"}):
		if fixture.bootstrapError {
			return nil, errors.New("bootstrap failed")
		}
		return []byte("ld_test_bootstrap_token\n"), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "getCurrentPrincipal"}):
		if fixture.authenticationError {
			return nil, errors.New("unauthorized")
		}
		return []byte(`{"id":"principal_admin","email":"fai-517-admin@qualification.invalid"}`), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "createPrincipal"}):
		return []byte(`{"principal":{"id":"principal_user","email":"fai-517-user@qualification.invalid"},"temporaryPassword":"temporary-secret"}`), nil
	case len(command) > 0 && command[0] == "publish":
		return []byte("published compatibility publish=publish_001 environment=fai-517 digest=" + strings.Repeat("a", 64) + " localDigest=" + strings.Repeat("b", 64) + " status=active\n"), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "listDashboards"}):
		return []byte(`{"items":[{"id":"preservation"}],"page":{}}`), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "querySemanticDataset"}):
		if fixture.workloadMismatch {
			return []byte(`{"items":[{"status":"delivered","order_count":1},{"status":"shipped","order_count":1}]}`), nil
		}
		return []byte(`{"items":[{"status":"delivered","order_count":2},{"status":"shipped","order_count":1}]}`), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "queryDashboardVisualData"}):
		return []byte(`{"data":[{"label":"Orders","series":"","value":3}]}`), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "listPrincipals"}):
		joined := strings.Join(command, " ")
		if strings.Contains(joined, v010AdminEmail) {
			return []byte(`{"items":[{"id":"principal_admin","kind":"user","email":"fai-517-admin@qualification.invalid","displayName":"fai-517-admin@qualification.invalid"}],"page":{}}`), nil
		}
		if fixture.restarted && fixture.missingPrincipal {
			return []byte(`{"items":[],"page":{}}`), nil
		}
		return []byte(`{"items":[{"id":"principal_user","kind":"user","email":"fai-517-user@qualification.invalid","displayName":"FAI-517 User"}],"page":{}}`), nil
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "listWorkspaces"}):
		title := "FAI-517 Compatibility"
		if fixture.restarted && fixture.alteredProject {
			title = "Changed Project"
		}
		return json.Marshal(map[string]any{"items": []map[string]any{{
			"id": "compatibility", "title": title, "activeServingStateId": "serving_001",
		}}, "page": map[string]any{}})
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "listPublishes"}):
		if fixture.restarted && fixture.missingPublish {
			return []byte(`{"items":[],"page":{}}`), nil
		}
		return json.Marshal(map[string]any{"items": []map[string]any{{
			"id": "publish_001", "workspaceId": "compatibility", "environment": "fai-517",
			"status": "active", "digest": strings.Repeat("a", 64),
		}}, "page": map[string]any{}})
	case len(command) >= 3 && slices.Equal(command[:3], []string{"api", "call", "getWorkspaceActiveAssetGraph"}):
		sourceHash := strings.Repeat("c", 64)
		if fixture.restarted && fixture.alteredManagedData {
			sourceHash = strings.Repeat("0", 64)
		}
		return json.Marshal(map[string]any{"assets": []map[string]any{
			{"id": "source:qualification.orders", "type": "source", "key": "qualification.orders", "servingStateId": "serving_001", "contentHash": sourceHash},
			{"id": "model_table:compatibility.orders", "type": "model_table", "key": "compatibility.orders", "servingStateId": "serving_001", "contentHash": strings.Repeat("d", 64)},
			{"id": "semantic_model:compatibility.compatibility", "type": "semantic_model", "key": "compatibility.compatibility", "servingStateId": "serving_001", "contentHash": strings.Repeat("e", 64)},
			{"id": "dashboard:compatibility.preservation", "type": "dashboard", "key": "compatibility.preservation", "servingStateId": "serving_001", "contentHash": strings.Repeat("f", 64)},
		}})
	default:
		return nil, fmt.Errorf("unexpected v0.1 CLI command %v", command)
	}
}

func (fixture *v010DockerFixture) containerInspect(name string) ([]byte, error) {
	network := name + "-network"
	volume := name + "-state"
	runDirectory := fixture.runDirectory()
	status := "running"
	running := true
	if fixture.stopped {
		status = "exited"
		running = false
	}
	var inspected v010ContainerInspect
	inspected.ID = v010TestContainerID
	inspected.Name = "/" + name
	inspected.Image = compatibility.ReleasedV010ConfigDigest
	inspected.State.Status = status
	inspected.State.Running = running
	inspected.Config.Image = compatibility.ReleasedV010Image
	inspected.HostConfig.NetworkMode = network
	inspected.NetworkSettings.Networks = map[string]json.RawMessage{network: json.RawMessage(`{}`)}
	inspected.Mounts = append(inspected.Mounts,
		struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		}{Type: "volume", Name: volume, Destination: v010StateMount, RW: true},
		struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		}{Type: "bind", Source: runDirectory, Destination: v010QualificationMount, RW: false},
	)
	if fixture.inspectMutate != nil {
		fixture.inspectMutate(&inspected)
	}
	return json.Marshal(inspected)
}

func (fixture *v010DockerFixture) runDirectory() string {
	fixture.t.Helper()
	for _, arguments := range fixture.requests {
		if len(arguments) == 0 || arguments[0] != "run" {
			continue
		}
		for index, argument := range arguments {
			if argument != "--mount" || index+1 >= len(arguments) {
				continue
			}
			value := arguments[index+1]
			if strings.Contains(value, "target="+v010QualificationMount) {
				for _, field := range strings.Split(value, ",") {
					if source, ok := strings.CutPrefix(field, "source="); ok {
						return source
					}
				}
			}
		}
	}
	fixture.t.Fatal("v0.1 run directory was not mounted")
	return ""
}

func readExactV010RegistryFixture(t *testing.T, path string) []byte {
	t.Helper()
	document, err := os.ReadFile(path)
	require.NoError(t, err)
	return bytes.TrimSuffix(document, []byte("\n"))
}

func writeV010DockerCredentials(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	auth := base64.StdEncoding.EncodeToString([]byte("test:token"))
	document, err := json.Marshal(map[string]any{"auths": map[string]any{"ghcr.io": map[string]string{"auth": auth}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, document, 0o600))
	return path
}

func TestDockerV010ArtifactResolverUsesAuthenticatedExactRegistryGraph(t *testing.T) {
	fixture := newV010DockerFixture(t)
	resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(fixture.execute))
	resolver.configPath = writeV010DockerCredentials(t)
	resolved, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
		Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
	})
	require.NoError(t, err)
	require.Equal(t, compatibility.ReleasedV010Image, resolved.Image)
	require.Equal(t, compatibility.ReleasedV010PlatformManifest, resolved.PlatformManifestDigest)
	require.Equal(t, compatibility.ReleasedV010ConfigDigest, resolved.ConfigDigest)
	require.True(t, resolved.Authenticated)

	for _, arguments := range fixture.requests {
		joined := strings.Join(arguments, " ")
		require.NotContains(t, joined, ":latest")
		require.NotContains(t, joined, "ghcr.io/flidai/libredash")
		if strings.Contains(joined, "ghcr.io/") {
			require.Contains(t, joined, "ghcr.io/yacobolo/libredash@sha256:")
		}
	}
}

func TestDockerV010ArtifactResolverFailsClosed(t *testing.T) {
	t.Run("authentication missing", func(t *testing.T) {
		called := false
		resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
			called = true
			return nil, nil
		}))
		resolver.configPath = filepath.Join(t.TempDir(), "missing-config.json")
		_, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
			Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
		})
		require.ErrorContains(t, err, "authenticated registry credentials are required")
		require.False(t, called)
	})

	t.Run("artifact unavailable", func(t *testing.T) {
		fixture := newV010DockerFixture(t)
		fixture.rootError = errors.New("manifest unknown")
		resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(fixture.execute))
		resolver.configPath = writeV010DockerCredentials(t)
		_, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
			Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
		})
		require.ErrorContains(t, err, "manifest unknown")
	})

	t.Run("registry authentication rejected", func(t *testing.T) {
		fixture := newV010DockerFixture(t)
		fixture.rootError = errors.New("unauthorized: authentication required")
		resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(fixture.execute))
		resolver.configPath = writeV010DockerCredentials(t)
		_, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
			Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
		})
		require.ErrorContains(t, err, "unauthorized: authentication required")
	})

	t.Run("digest mismatch", func(t *testing.T) {
		fixture := newV010DockerFixture(t)
		fixture.index = append(fixture.index, ' ')
		resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(fixture.execute))
		resolver.configPath = writeV010DockerCredentials(t)
		_, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
			Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
		})
		require.ErrorContains(t, err, "policy-selected immutable digest")
	})

	t.Run("pulled image has wrong platform", func(t *testing.T) {
		fixture := newV010DockerFixture(t)
		fixture.imageArch = "arm64"
		resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(fixture.execute))
		resolver.configPath = writeV010DockerCredentials(t)
		_, err := resolver.ResolveExact(t.Context(), compatibility.V010ArtifactResolutionRequest{
			Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true,
		})
		require.ErrorContains(t, err, "local v0.1 image identity")
	})

	for _, test := range []struct {
		name    string
		request compatibility.V010ArtifactResolutionRequest
	}{
		{name: "wrong platform", request: compatibility.V010ArtifactResolutionRequest{Image: compatibility.ReleasedV010Image, Platform: "linux/arm64", RequireAuthentication: true}},
		{name: "tag fallback", request: compatibility.V010ArtifactResolutionRequest{Image: "ghcr.io/yacobolo/libredash:v0.1.0", Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true}},
		{name: "namespace override", request: compatibility.V010ArtifactResolutionRequest{Image: "ghcr.io/flidai/libredash@sha256:" + strings.Repeat("6", 64), Platform: compatibility.ReleasedV010Platform, RequireAuthentication: true}},
		{name: "unauthenticated request", request: compatibility.V010ArtifactResolutionRequest{Image: compatibility.ReleasedV010Image, Platform: compatibility.ReleasedV010Platform}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			resolver := newDockerV010ArtifactResolver(t.TempDir(), "docker-probe", qualificationExecutorFunc(func(context.Context, qualificationCommandRequest) ([]byte, error) {
				called = true
				return nil, nil
			}))
			resolver.configPath = writeV010DockerCredentials(t)
			_, err := resolver.ResolveExact(t.Context(), test.request)
			require.ErrorContains(t, err, "only the authenticated exact policy-selected")
			require.False(t, called)
		})
	}
}

func TestV010ArtifactExecutionUsesIsolatedIdentityAndCleansUp(t *testing.T) {
	fixture := newV010DockerFixture(t)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	now := time.Date(2026, time.July, 13, 15, 45, 27, 0, time.UTC)
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
		Now: func() time.Time { now = now.Add(time.Second); return now },
	})
	require.NoError(t, err)
	policyDocument := v010CandidateBoundTestPolicy(t)
	evidence, err := controller.qualifyV010ArtifactExecution(t.Context(), policyDocument)
	require.NoError(t, err)
	require.NotNil(t, evidence.Execution)
	require.Equal(t, v010TestContainerID, evidence.Execution.ContainerID)
	require.True(t, evidence.Execution.CleanShutdown)
	require.True(t, evidence.Execution.CleanupVerified)
	require.NotNil(t, evidence.Execution.Journey)
	require.True(t, evidence.Execution.Journey.AuthenticationVerified)
	require.True(t, evidence.Execution.Journey.ProjectActivated)
	require.True(t, evidence.Execution.Journey.ManagedDataVerified)
	require.True(t, evidence.Execution.Journey.WorkloadVerified)
	require.Equal(t, 3, evidence.Execution.Journey.ManagedDataRows)
	require.NotNil(t, evidence.Execution.Preservation)
	require.True(t, evidence.Execution.Preservation.StatePreserved)
	require.True(t, evidence.Execution.Preservation.RestartIdentityVerified)
	require.Equal(t, evidence.Execution.Preservation.BeforeSHA256, evidence.Execution.Preservation.AfterSHA256)
	require.Len(t, evidence.Execution.Preservation.Inventory.Principals, 2)
	require.Len(t, evidence.Execution.Preservation.Inventory.Assets, 4)
	require.NotNil(t, evidence.Execution.FreshCandidate)
	require.True(t, evidence.Execution.FreshCandidate.CleanStateVerified)
	require.True(t, evidence.Execution.FreshCandidate.LegacyStateUnavailable)
	require.True(t, evidence.Execution.FreshCandidate.MutationFree)
	require.True(t, evidence.Execution.FreshCandidate.CleanupVerified)
	require.Equal(t, fixture.candidateImage, evidence.Execution.FreshCandidate.Candidate.Image)
	require.Equal(t, compatibility.ReasonAllowedFreshInstall, evidence.Execution.FreshCandidate.FreshInstallDecision.ReasonCode)
	require.NotEqual(t, evidence.Execution.FreshCandidate.FreshStateBeforeSHA256, evidence.Execution.FreshCandidate.FreshStateAfterSHA256)
	require.Equal(t, evidence.Execution.FreshCandidate.CandidateBeforeDenialsSHA256, evidence.Execution.FreshCandidate.CandidateAfterDenialsSHA256)
	require.Equal(t, evidence.Execution.FreshCandidate.PredecessorBeforeDenialsSHA256, evidence.Execution.FreshCandidate.PredecessorAfterDenialsSHA256)
	require.Len(t, evidence.Execution.FreshCandidate.Denials, 3)
	require.Zero(t, evidence.Execution.FreshCandidate.CandidateInventory.LegacyPrincipalCount)
	require.Zero(t, evidence.Execution.FreshCandidate.CandidateInventory.DashboardCount)
	require.Zero(t, evidence.Execution.FreshCandidate.CandidateInventory.SemanticModelCount)
	require.True(t, fixture.projectVerified)
	_, err = compatibility.MarshalV010ReleaseIdentityEvidence(evidence, policyDocument)
	require.NoError(t, err)

	var runArguments []string
	for _, arguments := range fixture.requests {
		if len(arguments) > 0 && arguments[0] == "run" {
			runArguments = arguments
			break
		}
	}
	require.NotEmpty(t, runArguments)
	joined := strings.Join(runArguments, "\x00")
	for _, required := range []string{
		"--platform\x00linux/amd64", "--pull\x00never", "--read-only", "--network\x00" + evidence.Execution.NetworkName,
		"--env\x00LIBREDASH_LOCAL_AUTH=1", "--env\x00LIBREDASH_COOKIE_SECURE=1",
		"type=volume,source=" + evidence.Execution.StateVolumeName + ",target=" + v010StateMount,
		"target=" + v010QualificationMount + ",readonly", compatibility.ReleasedV010Image,
	} {
		require.Contains(t, joined, required)
	}
	requireV010CleanupCommand(t, fixture.requests, []string{"stop", "--time", "30", evidence.Execution.ContainerName})
	requireV010CleanupCommand(t, fixture.requests, []string{"start", evidence.Execution.ContainerName})
	require.Equal(t, 2, countV010Command(fixture.requests, []string{"stop", "--time", "30", evidence.Execution.ContainerName}))
	requireV010CleanupCommand(t, fixture.requests, []string{"rm", "--force", evidence.Execution.ContainerName})
	requireV010CleanupCommand(t, fixture.requests, []string{"volume", "rm", evidence.Execution.StateVolumeName})
	requireV010CleanupCommand(t, fixture.requests, []string{"network", "rm", evidence.Execution.NetworkName})
	require.NoDirExists(t, fixture.runDirectory())
	require.NoDirExists(t, fixture.candidateRunDirectory())
}

func TestV010FreshCandidateDeniesLegacyStateWithoutMutation(t *testing.T) {
	fixture := newV010DockerFixture(t)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)
	evidence, err := controller.qualifyV010ArtifactExecution(t.Context(), v010CandidateBoundTestPolicy(t))
	require.NoError(t, err)
	fresh := evidence.Execution.FreshCandidate
	require.NotNil(t, fresh)
	require.Equal(t, []string{
		"preserved-state-mount", "legacy-state-reference", "direct-legacy-artifact-adoption",
	}, []string{fresh.Denials[0].Scenario, fresh.Denials[1].Scenario, fresh.Denials[2].Scenario})
	for _, denial := range fresh.Denials {
		require.True(t, denial.DeniedBeforeMutation)
		require.Equal(t, denial.BeforeSHA256, denial.AfterSHA256)
	}
	joined := make([]string, 0, len(fixture.requests))
	for _, request := range fixture.requests {
		joined = append(joined, strings.Join(request, " "))
	}
	commands := strings.Join(joined, "\n")
	require.Contains(t, commands, "LEAPVIEW_HOME=/var/lib/leapview")
	require.Contains(t, commands, "LEAPVIEW_HOME=/legacy-v010")
	require.Contains(t, commands, "source="+evidence.Execution.StateVolumeName+",target=/var/lib/leapview")
	require.NotContains(t, candidateServerRunCommand(fixture.requests, fixture.candidateImage), evidence.Execution.StateVolumeName)
}

func TestV010FreshCandidateFailsClosedOnLegacyVisibilityOrMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*v010DockerFixture)
		want   string
	}{
		{name: "legacy state visible", mutate: func(f *v010DockerFixture) { f.candidateLegacyVisible = true }, want: "exposed preserved v0.1"},
		{name: "denial mutates state", mutate: func(f *v010DockerFixture) { f.candidateDenialMutates = true }, want: "changed preserved predecessor state"},
		{name: "candidate identity mismatch", mutate: func(f *v010DockerFixture) { f.candidateRevision = strings.Repeat("0", 40) }, want: "does not match the admitted policy artifact"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newV010DockerFixture(t)
			test.mutate(fixture)
			t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
			controller, err := New(Options{
				Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
			})
			require.NoError(t, err)
			_, err = controller.qualifyV010ArtifactExecution(t.Context(), v010CandidateBoundTestPolicy(t))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func candidateServerRunCommand(requests [][]string, image string) string {
	for _, request := range requests {
		if len(request) > 0 && request[0] == "run" && slices.Contains(request, "--detach") && slices.Contains(request, image) {
			return strings.Join(request, " ")
		}
	}
	return ""
}

func TestV010StoppedStateInventoryFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*v010DockerFixture)
		want   string
	}{
		{name: "missing principal", mutate: func(f *v010DockerFixture) { f.missingPrincipal = true }, want: "omitted or changed principal"},
		{name: "altered project state", mutate: func(f *v010DockerFixture) { f.alteredProject = true }, want: "omitted or changed the activated project"},
		{name: "altered managed-data metadata", mutate: func(f *v010DockerFixture) { f.alteredManagedData = true }, want: "inventory changed across clean shutdown"},
		{name: "missing published workload", mutate: func(f *v010DockerFixture) { f.missingPublish = true }, want: "omitted or changed the active published workload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newV010DockerFixture(t)
			test.mutate(fixture)
			t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
			controller, err := New(Options{
				Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
			})
			require.NoError(t, err)
			_, err = controller.qualifyV010ArtifactExecution(t.Context(), v010CandidateBoundTestPolicy(t))
			require.ErrorContains(t, err, test.want)
			name := v010ContainerNameFromRequests(fixture.requests)
			require.NotEmpty(t, name)
			requireV010CleanupCommand(t, fixture.requests, []string{"start", name})
			requireV010CleanupCommand(t, fixture.requests, []string{"rm", "--force", name})
		})
	}
}

func TestV010ApplicationJourneyFailsClosedAtSupportedBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*v010DockerFixture)
		want         string
		shortContext bool
	}{
		{name: "readiness failure", mutate: func(f *v010DockerFixture) { f.readinessError = true }, want: "readiness was not reached", shortContext: true},
		{name: "bootstrap failure", mutate: func(f *v010DockerFixture) { f.bootstrapError = true }, want: "bootstrap v0.1 application"},
		{name: "authentication failure", mutate: func(f *v010DockerFixture) { f.authenticationError = true }, want: "authenticate to v0.1 API"},
		{name: "workload mismatch", mutate: func(f *v010DockerFixture) { f.workloadMismatch = true }, want: "semantic workload result"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newV010DockerFixture(t)
			test.mutate(fixture)
			t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
			controller, err := New(Options{
				Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
			})
			require.NoError(t, err)
			ctx := t.Context()
			if test.shortContext {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			_, err = controller.qualifyV010ArtifactExecution(ctx, v010CandidateBoundTestPolicy(t))
			require.ErrorContains(t, err, test.want)
			name := v010ContainerNameFromRequests(fixture.requests)
			require.NotEmpty(t, name)
			requireV010CleanupCommand(t, fixture.requests, []string{"rm", "--force", name})
		})
	}
}

func TestV010ApplicationJourneyUsesOnlyReleasedCLIAndDoesNotExposeCredentialArguments(t *testing.T) {
	fixture := newV010DockerFixture(t)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)
	evidence, err := controller.qualifyV010ArtifactExecution(t.Context(), v010CandidateBoundTestPolicy(t))
	require.NoError(t, err)
	require.NotNil(t, evidence.Execution.Journey)
	require.Equal(t, v010ProjectID, evidence.Execution.Journey.ProjectID)
	require.Equal(t, v010Environment, evidence.Execution.Journey.Environment)
	for _, arguments := range fixture.requests {
		joined := strings.Join(arguments, " ")
		require.NotContains(t, joined, "ld_test_bootstrap_token")
		require.NotContains(t, joined, "temporary-secret")
		if len(arguments) > 0 && arguments[0] == "exec" {
			if slices.Contains(arguments, "leapview") {
				require.NotContains(t, joined, "candidate-publisher-token")
			} else {
				require.Contains(t, arguments, "libredash")
			}
		}
	}
}

func v010ContainerNameFromRequests(requests [][]string) string {
	for _, arguments := range requests {
		if len(arguments) == 0 || arguments[0] != "run" {
			continue
		}
		for index, argument := range arguments {
			if argument == "--name" && index+1 < len(arguments) {
				return arguments[index+1]
			}
		}
	}
	return ""
}

func TestV010ArtifactExecutionRejectsContainerIdentityMismatchAndStillCleansUp(t *testing.T) {
	fixture := newV010DockerFixture(t)
	fixture.inspectMutate = func(inspected *v010ContainerInspect) {
		inspected.Config.Image = "ghcr.io/yacobolo/libredash:v0.1.0"
	}
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	controller, err := New(Options{
		Root: t.TempDir(), DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)
	_, err = controller.qualifyV010ArtifactExecution(t.Context(), v010CandidateBoundTestPolicy(t))
	require.ErrorContains(t, err, "container identity")
	name := ""
	for _, arguments := range fixture.requests {
		if len(arguments) > 0 && arguments[0] == "run" {
			for index, argument := range arguments {
				if argument == "--name" && index+1 < len(arguments) {
					name = arguments[index+1]
				}
			}
		}
	}
	require.NotEmpty(t, name)
	requireV010CleanupCommand(t, fixture.requests, []string{"rm", "--force", name})
	requireV010CleanupCommand(t, fixture.requests, []string{"volume", "rm", name + "-state"})
	requireV010CleanupCommand(t, fixture.requests, []string{"network", "rm", name + "-network"})
}

func requireV010CleanupCommand(t *testing.T, requests [][]string, want []string) {
	t.Helper()
	for _, request := range requests {
		if slices.Equal(request, want) {
			return
		}
	}
	t.Fatalf("Docker command %v not found in %v", want, requests)
}

func countV010Command(requests [][]string, want []string) int {
	count := 0
	for _, request := range requests {
		if slices.Equal(request, want) {
			count++
		}
	}
	return count
}
