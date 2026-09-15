package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/stretchr/testify/require"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"
const testImage = "ghcr.io/flidai/leapview@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeEndpoint struct {
	host, server, fingerprint string
	verifyErr                 error
	verifications             atomic.Int64
}

func (endpoint *fakeEndpoint) Host() string        { return endpoint.host }
func (endpoint *fakeEndpoint) ServerID() string    { return endpoint.server }
func (endpoint *fakeEndpoint) Fingerprint() string { return endpoint.fingerprint }
func (endpoint *fakeEndpoint) Verify(context.Context) error {
	endpoint.verifications.Add(1)
	return endpoint.verifyErr
}
func (endpoint *fakeEndpoint) DockerArguments(arguments ...string) []string {
	return append([]string{"--host", endpoint.host}, arguments...)
}
func (endpoint *fakeEndpoint) Environment(base []string) []string {
	return append([]string(nil), base...)
}

type fakeRunner struct {
	mu           sync.Mutex
	commands     [][]string
	environments [][]string
	artifacts    adminoffline.QualificationPoolArtifacts
	failOnce     string
	failError    string
	responses    map[string][]byte
}

func TestInstalledRuntimePackageFollowsAuthoringExecutableSymlink(t *testing.T) {
	root := t.TempDir()
	installation := filepath.Join(root, "lib", "leapview", "v1")
	require.NoError(t, os.MkdirAll(filepath.Join(installation, "local-runtime"), 0o755))
	executable := filepath.Join(installation, "leapview")
	require.NoError(t, os.WriteFile(executable, []byte("binary"), 0o755))
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	link := filepath.Join(bin, "leapview")
	require.NoError(t, os.Symlink(executable, link))

	got, err := installedRuntimePackageRoot(link)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(installation, "local-runtime"), got)
}

func (runner *fakeRunner) Run(_ context.Context, environment []string, arguments ...string) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.commands = append(runner.commands, append([]string(nil), arguments...))
	runner.environments = append(runner.environments, append([]string(nil), environment...))
	joined := strings.Join(arguments, " ")
	for contains, response := range runner.responses {
		if strings.Contains(joined, contains) {
			return append([]byte(nil), response...), nil
		}
	}
	if runner.failOnce != "" && strings.Contains(joined, runner.failOnce) {
		runner.failOnce = ""
		if runner.failError != "" {
			return nil, errors.New(runner.failError)
		}
		return nil, fmt.Errorf("interrupted")
	}
	switch {
	case strings.HasSuffix(joined, "compose version --short"):
		return []byte("2.17.0\n"), nil
	case strings.Contains(joined, "admin initialize --format json"):
		return json.Marshal(adminoffline.InitialCredentials{Email: "admin@localhost", TemporaryPassword: "temporary", PublisherToken: "publisher", PublisherTokenExpiresAt: "2099-01-01T00:00:00Z"})
	case strings.Contains(joined, "admin delivery pool qualify"):
		return adminoffline.MarshalQualificationPoolArtifacts(runner.artifacts)
	case strings.Contains(joined, "admin delivery pool bootstrap"):
		pool, err := physicalpool.NewPhysicalPool(runner.artifacts.Pool)
		if err != nil {
			return nil, err
		}
		compatibility, err := runner.artifacts.Evidence.Evidence.Compatibility.Digest()
		if err != nil {
			return nil, err
		}
		applied := strings.HasSuffix(joined, " --apply")
		return []byte(fmt.Sprintf("pool_id: %s\ncompatibility_digest: %s\nevidence_digest: %s\nconformance_version: %s\napplied: %t\n", pool.ID, compatibility, runner.artifacts.Evidence.Evidence.Digest, runner.artifacts.Evidence.Evidence.ConformanceVersion, applied)), nil
	case strings.Contains(joined, "admin project-claim"):
		return []byte(`{"instanceId":"instance-local","projectUid":"lvproject_test","environment":"dev","claimedBy":"email_admin","claimedAt":"2026-09-15T12:00:00Z"}` + "\n"), nil
	case strings.Contains(joined, " down --timeout"):
		runner.responses = nil
		return nil, nil
	default:
		return nil, nil
	}
}

type localHTTPTransport struct {
	mu sync.Mutex
}

func (transport *localHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, ""
	switch request.URL.Path {
	case "/readyz":
	case "/api/v1/instance":
		body = fmt.Sprintf(`{"canonicalOrigin":%q,"environment":"dev","id":"instance-local"}`, request.URL.Scheme+"://"+request.URL.Host)
	default:
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestStartPersistsExactIntentAndCompletesThroughExistingAuthorities(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t)}
	var sessionRequest SessionRequest
	var sessionCalls int
	controller, err := New(Options{
		CheckoutRoot: checkout, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: endpoint, Runner: runner, BuildIdentity: testBuildIdentity(),
		ResolveProjectAuthority: testProjectAuthority,
		EstablishSessions: func(_ context.Context, request SessionRequest) (SessionResult, error) {
			sessionCalls++
			sessionRequest = request
			return SessionResult{TargetName: request.TargetName, SessionID: "session-local"}, nil
		},
		HTTPClient: &http.Client{Transport: &localHTTPTransport{}},
		Stdout:     io.Discard,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	require.NoError(t, err)
	state, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, statusApplied, state.Status)
	require.Equal(t, phaseReady, state.Phase)
	require.Equal(t, "instance-local", state.Authority.InstanceID)
	require.NotEmpty(t, state.Authority.PoolID)
	require.Equal(t, state.Network.URL, sessionRequest.Origin)
	require.Equal(t, state.Authority.ProjectUID, sessionRequest.ProjectID)
	require.Equal(t, "session-local", state.Session.SessionID)
	require.Greater(t, endpoint.verifications.Load(), int64(len(runner.commands)))
	for _, command := range runner.commands {
		require.GreaterOrEqual(t, len(command), 3)
		require.Equal(t, []string{"--host", endpoint.host}, command[:2])
	}
	initializationIndex := commandIndex(runner.commands, "admin initialize --format json")
	require.NotEqual(t, -1, initializationIndex)
	require.Contains(t, strings.Join(runner.commands[initializationIndex], " "), "--env LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	require.Contains(t, strings.Join(runner.environments[initializationIndex], "\n"), "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgresql://leapview_control_migrator:")
	poolIndex := commandIndex(runner.commands, "admin delivery pool bootstrap")
	require.NotEqual(t, -1, poolIndex)
	require.Contains(t, strings.Join(runner.commands[poolIndex], " "), "--env LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	require.Contains(t, strings.Join(runner.environments[poolIndex], "\n"), "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgresql://leapview_control_migrator:")
	root := stateDirectory(stateRoot, state.Checkout.ID)
	encodedState := string(mustReadFile(t, filepath.Join(root, stateFileName)))
	require.Equal(t, attachmentSchemaVersion, state.AttachmentRegistryVersion)
	require.NotContains(t, encodedState, "temporary")
	require.NotContains(t, encodedState, "publisher")
	attachmentInfo, err := os.Stat(filepath.Join(root, attachmentsFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), attachmentInfo.Mode().Perm())
	env := mustReadFile(t, filepath.Join(root, runtimeEnvFileName))
	require.Contains(t, string(env), "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=")
	info, err := os.Stat(filepath.Join(root, runtimeEnvFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	mutationCalls := countCommands(runner.commands, "admin initialize --format json") + countCommands(runner.commands, "admin delivery pool qualify") + countCommands(runner.commands, "admin delivery pool bootstrap")
	repeated, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, phaseReady, repeated.Phase)
	require.Equal(t, 2, sessionCalls)
	require.Equal(t, mutationCalls, countCommands(runner.commands, "admin initialize --format json")+countCommands(runner.commands, "admin delivery pool qualify")+countCommands(runner.commands, "admin delivery pool bootstrap"))
}

func TestStartRejectsEmptySessionIdentity(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	controller, err := New(Options{
		CheckoutRoot: checkout, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"},
		Runner:   &fakeRunner{artifacts: testQualificationArtifacts(t)}, BuildIdentity: testBuildIdentity(),
		ResolveProjectAuthority: testProjectAuthority,
		EstablishSessions: func(_ context.Context, request SessionRequest) (SessionResult, error) {
			return SessionResult{TargetName: request.TargetName}, nil
		},
		HTTPClient: &http.Client{Transport: &localHTTPTransport{}}, Stdout: io.Discard,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	require.NoError(t, err)
	state, err := controller.Start(t.Context())
	require.ErrorContains(t, err, "invalid session identity")
	require.Equal(t, statusIncomplete, state.Status)
	require.Empty(t, state.Session.SessionID)
}

func TestStartResumesExactIncompletePhaseWithoutReplacingCredentials(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: "admin delivery pool bootstrap"}
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
	controller, err := New(options)
	require.NoError(t, err)
	failed, err := controller.Start(t.Context())
	require.ErrorContains(t, err, "validate local physical-pool admission")
	require.Equal(t, statusIncomplete, failed.Status)
	require.Equal(t, phaseProject, failed.Phase)
	root := stateDirectory(stateRoot, failed.Checkout.ID)
	credentials := mustReadFile(t, filepath.Join(root, credentialsFileName))
	// The single retained qualification envelope is authoritative; partial
	// mounted artifact writes are regenerated during recovery.
	require.NoError(t, os.WriteFile(filepath.Join(root, poolFileName), []byte("corrupt"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(root, evidenceFileName)))

	controller, err = New(options)
	require.NoError(t, err)
	resumed, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, statusApplied, resumed.Status)
	require.Equal(t, string(credentials), string(mustReadFile(t, filepath.Join(root, credentialsFileName))))
}

func TestStartRecoversAttachmentRegistryInitializationBoundary(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t)}
	controller, err := New(testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner))
	require.NoError(t, err)
	canonicalCheckout, checkoutID, err := checkoutIdentity(checkout)
	require.NoError(t, err)
	manifest, manifestDigest, err := loadManifest(packageRoot, testBuildIdentity())
	require.NoError(t, err)
	state, err := controller.newIntent(canonicalCheckout, checkoutID, manifestDigest)
	require.NoError(t, err)
	root := stateDirectory(stateRoot, checkoutID)
	require.NoError(t, os.MkdirAll(root, 0o700))
	values, err := initialEnvironment(state, manifest)
	require.NoError(t, err)
	require.NoError(t, writeEnvironment(filepath.Join(root, runtimeEnvFileName), values))
	require.NoError(t, saveState(filepath.Join(root, stateFileName), state))

	resumed, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, attachmentSchemaVersion, resumed.AttachmentRegistryVersion)
	registry, err := loadAttachmentRegistry(filepath.Join(root, attachmentsFileName), attachmentBindingFor(resumed), false)
	require.NoError(t, err)
	require.Empty(t, registry.Attachments)
}

func TestStartRejectsEditedRetainedPoolQualificationIntent(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: "admin delivery pool bootstrap"}
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
	controller, err := New(options)
	require.NoError(t, err)
	failed, err := controller.Start(t.Context())
	require.Error(t, err)

	changed := testQualificationArtifacts(t)
	changed.Pool.Tenant = "edited"
	encoded, err := adminoffline.MarshalQualificationPoolArtifacts(changed)
	require.NoError(t, err)
	root := stateDirectory(stateRoot, failed.Checkout.ID)
	require.NoError(t, os.WriteFile(filepath.Join(root, qualificationFileName), append(encoded, '\n'), 0o644))
	runner.artifacts = changed

	controller, err = New(options)
	require.NoError(t, err)
	_, err = controller.Start(t.Context())
	require.ErrorContains(t, err, "differs from durable admission intent")
}

func TestStartRetriesLoopbackPortConflictInsideOwnedProject(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: "up -d postgres", failError: "port is already allocated"}
	controller, err := New(testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner))
	require.NoError(t, err)
	state, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, phaseReady, state.Phase)
	var removedFailedContainer bool
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command, " "), "rm --stop --force leapview postgres") {
			removedFailedContainer = true
		}
	}
	require.True(t, removedFailedContainer)
	oldPort, oldTarget := state.Network.AppPort, state.Session.TargetName
	occupied, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", oldPort))
	require.NoError(t, err)
	defer occupied.Close()
	runner.failOnce, runner.failError = "up -d postgres", "address already in use"
	controller, err = New(testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner))
	require.NoError(t, err)
	restarted, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, oldPort, restarted.Network.AppPort)
	require.Equal(t, oldTarget, restarted.Session.TargetName)
	require.Equal(t, sessionTargetName(restarted), restarted.Session.TargetName)
}

func TestStartResumesAfterEveryContainerBootstrapBoundary(t *testing.T) {
	for _, test := range []struct {
		name, fail, phase string
	}{
		{name: "image pull", fail: "pull postgres leapview", phase: phaseIntent},
		{name: "postgres start", fail: "up -d postgres", phase: phaseIntent},
		{name: "instance initialization", fail: "admin initialize --format json", phase: phasePostgres},
		{name: "pool qualification", fail: "admin delivery pool qualify", phase: phaseProject},
		{name: "pool admission", fail: "admin delivery pool bootstrap", phase: phaseProject},
		{name: "application start", fail: "up -d leapview", phase: phasePool},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
			endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
			runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: test.fail}
			options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
			controller, err := New(options)
			require.NoError(t, err)
			failed, err := controller.Start(t.Context())
			require.Error(t, err)
			require.Equal(t, statusIncomplete, failed.Status)
			require.Equal(t, test.phase, failed.Phase)
			controller, err = New(options)
			require.NoError(t, err)
			resumed, err := controller.Start(t.Context())
			require.NoError(t, err)
			require.Equal(t, phaseReady, resumed.Phase)
		})
	}
}

func TestStartReconcilesLostProjectClaimAcknowledgementWithExactIdentity(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: "admin project-claim", failError: "project claim acknowledgement lost"}
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
	controller, err := New(options)
	require.NoError(t, err)
	failed, err := controller.Start(t.Context())
	require.ErrorContains(t, err, "acknowledgement lost")
	require.Equal(t, phaseInstance, failed.Phase)
	controller, err = New(options)
	require.NoError(t, err)
	resumed, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, phaseReady, resumed.Phase)
	var claims [][]string
	for _, command := range runner.commands {
		if strings.Contains(strings.Join(command, " "), "admin project-claim") {
			claims = append(claims, command)
		}
	}
	require.Len(t, claims, 2)
	require.Equal(t, claims[0], claims[1])
}

func TestStartRetriesNormalSessionEstablishmentWithoutRebootstrapping(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:endpoint"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t)}
	options := testControllerOptions(checkout, packageRoot, stateRoot, endpoint, runner)
	fail := true
	options.EstablishSessions = func(_ context.Context, request SessionRequest) (SessionResult, error) {
		if fail {
			fail = false
			return SessionResult{}, errors.New("browser authorization interrupted")
		}
		return SessionResult{TargetName: request.TargetName, SessionID: "session-local"}, nil
	}
	controller, err := New(options)
	require.NoError(t, err)
	failed, err := controller.Start(t.Context())
	require.ErrorContains(t, err, "browser authorization interrupted")
	require.Equal(t, phaseApplication, failed.Phase)
	bootstrapCalls := countCommands(runner.commands, "admin delivery pool bootstrap")
	controller, err = New(options)
	require.NoError(t, err)
	resumed, err := controller.Start(t.Context())
	require.NoError(t, err)
	require.Equal(t, phaseReady, resumed.Phase)
	require.Equal(t, bootstrapCalls, countCommands(runner.commands, "admin delivery pool bootstrap"))
}

func TestRetainedEndpointChangeFailsBeforeDockerCommand(t *testing.T) {
	checkout, packageRoot, stateRoot := t.TempDir(), testRuntimePackage(t), t.TempDir()
	first := &fakeEndpoint{host: "unix:///var/run/docker.sock", server: "daemon-1", fingerprint: "sha256:first"}
	runner := &fakeRunner{artifacts: testQualificationArtifacts(t), failOnce: "compose version"}
	controller, err := New(testControllerOptions(checkout, packageRoot, stateRoot, first, runner))
	require.NoError(t, err)
	_, err = controller.Start(t.Context())
	require.Error(t, err)
	previousCommands := len(runner.commands)
	changed := &fakeEndpoint{host: first.host, server: "daemon-2", fingerprint: "sha256:second"}
	controller, err = New(testControllerOptions(checkout, packageRoot, stateRoot, changed, runner))
	require.NoError(t, err)
	_, err = controller.Start(t.Context())
	require.ErrorContains(t, err, "different Docker endpoint")
	require.Equal(t, previousCommands, len(runner.commands))
}

func TestManifestRejectsUnknownFieldsAndMismatchedCLIWithoutDocker(t *testing.T) {
	packageRoot := testRuntimePackage(t)
	manifestPath := filepath.Join(packageRoot, manifestFileName)
	var document map[string]any
	require.NoError(t, json.Unmarshal(mustReadFile(t, manifestPath), &document))
	document["unknown"] = true
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, encoded, 0o600))
	_, _, err = loadManifest(packageRoot, testBuildIdentity())
	require.Error(t, err)

	packageRoot = testRuntimePackage(t)
	identity := testBuildIdentity()
	identity.Revision = strings.Repeat("b", 40)
	_, _, err = loadManifest(packageRoot, identity)
	require.ErrorContains(t, err, "CLI identifies")
}

func TestManifestDigestBindsEveryExecutablePayload(t *testing.T) {
	packageRoot := testRuntimePackage(t)
	_, before, err := loadManifest(packageRoot, testBuildIdentity())
	require.NoError(t, err)

	composePath := filepath.Join(packageRoot, composeFileName)
	require.NoError(t, os.WriteFile(composePath, append(mustReadFile(t, composePath), []byte("# changed\n")...), 0o600))
	_, after, err := loadManifest(packageRoot, testBuildIdentity())
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

func TestComposeProcessEnvironmentRejectsAmbientLeapViewAndComposeSelection(t *testing.T) {
	t.Setenv("LEAPVIEW_IMAGE", "production-secret-image")
	t.Setenv("LEAPVIEW_TARGET", "production")
	t.Setenv("COMPOSE_PROJECT_NAME", "foreign")
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "linux/foreign")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "host-cloud-secret")
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock"}
	controller := &Controller{endpoint: endpoint}
	environment := controller.processEnvironment([]string{"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=operation-only"})
	joined := strings.Join(environment, "\n")
	require.NotContains(t, joined, "production-secret-image")
	require.NotContains(t, joined, "LEAPVIEW_TARGET=")
	require.NotContains(t, joined, "COMPOSE_PROJECT_NAME=")
	require.NotContains(t, joined, "DOCKER_DEFAULT_PLATFORM=")
	require.NotContains(t, joined, "host-cloud-secret")
	require.Contains(t, joined, "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=operation-only")
}

func TestMissingStateRefusesExistingComposeResources(t *testing.T) {
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock"}
	runner := &fakeRunner{responses: map[string][]byte{"container ls": []byte("foreign-container\n")}}
	controller := &Controller{endpoint: endpoint, runner: runner}
	state := State{Runtime: runtimeID{ComposeProject: "leapview-owned"}}
	err := controller.verifyResourceOwnership(t.Context(), state, true)
	require.ErrorContains(t, err, "refusing adoption")
	require.Len(t, runner.commands, 1)
}

func TestRetainedStateRejectsWrongOwnershipLabels(t *testing.T) {
	endpoint := &fakeEndpoint{host: "unix:///var/run/docker.sock"}
	runner := &fakeRunner{responses: map[string][]byte{
		"container ls":      []byte("container-one\n"),
		"container inspect": []byte(`{"io.leapview.local-runtime":"true","io.leapview.local-runtime.schema":"1","io.leapview.local-runtime.checkout":"sha256:other","io.leapview.local-runtime.owner":"owner"}`),
	}}
	controller := &Controller{endpoint: endpoint, runner: runner}
	state := State{Checkout: checkout{ID: "sha256:checkout"}, Runtime: runtimeID{ComposeProject: "leapview-owned", OwnerID: "owner"}}
	err := controller.verifyResourceOwnership(t.Context(), state, false)
	require.ErrorContains(t, err, "exact checkout ownership")
}

func TestDiscoverCheckoutRootCollapsesSubdirectoriesAndSymlinks(t *testing.T) {
	checkout := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: elsewhere\n"), 0o600))
	inner := filepath.Join(checkout, "analytics", "dashboards")
	require.NoError(t, os.MkdirAll(inner, 0o700))
	linkRoot := t.TempDir()
	link := filepath.Join(linkRoot, "checkout-link")
	require.NoError(t, os.Symlink(checkout, link))
	resolved, err := discoverCheckoutRoot(filepath.Join(link, "analytics", "dashboards"))
	require.NoError(t, err)
	canonical, err := canonicalDirectory(checkout)
	require.NoError(t, err)
	require.Equal(t, canonical, resolved)
}

func TestSameNamedWorktreesHaveDifferentRuntimeNamespaces(t *testing.T) {
	leftParent, rightParent := t.TempDir(), t.TempDir()
	left, right := filepath.Join(leftParent, "analytics"), filepath.Join(rightParent, "analytics")
	require.NoError(t, os.Mkdir(left, 0o700))
	require.NoError(t, os.Mkdir(right, 0o700))
	_, leftID, err := checkoutIdentity(left)
	require.NoError(t, err)
	_, rightID, err := checkoutIdentity(right)
	require.NoError(t, err)
	require.NotEqual(t, leftID, rightID)
}

func testRuntimePackage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	manifest := fmt.Sprintf(`{"schemaVersion":1,"persistentStateSchemaVersion":1,"composeMinimumVersion":"2.17.0","leapview":{"version":"1.2.3","revision":%q,"image":%q},"postgres":{"major":18,"image":%q}}`, testRevision, testImage, postgresImage)
	for name, contents := range map[string]string{manifestFileName: manifest, manifestSchemaName: "{}\n", composeFileName: "services: {}\n", postgresInitName: "#!/bin/sh\n"} {
		mode := os.FileMode(0o600)
		if name == postgresInitName {
			mode = 0o700
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(contents), mode))
	}
	return root
}

func testBuildIdentity() buildinfo.Identity {
	return buildinfo.Identity{Version: "1.2.3", Revision: testRevision, BuildTime: "2026-09-15T00:00:00Z"}
}

func testProjectAuthority() (ProjectAuthority, error) {
	return ProjectAuthority{IssuerID: "lvissuer_test", ProjectUID: "lvproject_test"}, nil
}

func testControllerOptions(checkout, packageRoot, stateRoot string, endpoint Endpoint, runner Runner) Options {
	return Options{
		CheckoutRoot: checkout, RuntimePackage: packageRoot, StateRoot: stateRoot,
		Endpoint: endpoint, Runner: runner, BuildIdentity: testBuildIdentity(),
		ResolveProjectAuthority: testProjectAuthority,
		EstablishSessions: func(_ context.Context, request SessionRequest) (SessionResult, error) {
			return SessionResult{TargetName: request.TargetName, SessionID: "session-local"}, nil
		},
		HTTPClient: &http.Client{Transport: &localHTTPTransport{}},
		Stdout:     io.Discard,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
}

func testQualificationArtifacts(t *testing.T) adminoffline.QualificationPoolArtifacts {
	t.Helper()
	compatibility := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{Compatibility: compatibility, ConformanceVersion: "test/v1", Checks: []physicalpool.EvidenceCheck{{ID: "shared_pool_check", Passed: true, ObservationDigest: "sha256:" + strings.Repeat("0", 64)}}})
	require.NoError(t, err)
	return adminoffline.QualificationPoolArtifacts{SchemaVersion: adminoffline.QualificationPoolArtifactsSchemaVersion, Pool: physicalpool.PoolIdentity{StorageLocation: "/var/lib/leapview/data", StorageNamespace: "delivery", Region: "local", Tenant: "qualification", EncryptionDomain: "local", IsolationBoundary: "qualification", RetentionAuthority: "qualification", RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600}, Compatibility: compatibility}, Evidence: physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: evidence}}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}

func countCommands(commands [][]string, contains string) int {
	count := 0
	for _, command := range commands {
		if strings.Contains(strings.Join(command, " "), contains) {
			count++
		}
	}
	return count
}

func commandIndex(commands [][]string, contains string) int {
	for index, command := range commands {
		if strings.Contains(strings.Join(command, " "), contains) {
			return index
		}
	}
	return -1
}
