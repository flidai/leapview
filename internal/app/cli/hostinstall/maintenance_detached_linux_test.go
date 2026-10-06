//go:build linux

package hostinstall

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// Use the real snapshot, configuration, clone construction and migrator command
// paths. Only Docker execution is replaced. A clone failure after migration
// must preserve writes accepted since the capture, not replay live recovery.
func TestDetachedNativeFailurePreservesReopenedLiveState(t *testing.T) {
	e := nativeEffectsFixture(t)
	e.detached = true
	e.stdout = io.Discard
	e.reader = bufio.NewReader(strings.NewReader("commit " + e.id.ArtifactAdmissionDigest + " AWAITING_RECOVERY_BROWSER_VALIDATION\n"))
	e.original.Volumes = map[string]string{}
	live := map[string]string{}
	for _, name := range []string{"postgres", "home", "caddy-data", "caddy-config"} {
		dir := t.TempDir()
		e.original.Volumes[name] = dir
		path := filepath.Join(dir, "state")
		if err := os.WriteFile(path, []byte("captured"), 0600); err != nil {
			t.Fatal(err)
		}
		live[path] = "new live writes " + name
	}
	digest, err := CaptureStoppedDirectories(t.Context(), e.snapshot(), e.id.Target, e.original.Volumes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"leapview.env", "deployment.env", ".host-install.json", JournalName} {
		live[filepath.Join(e.root, name)] = "live state after capture " + name
	}
	for path, value := range live {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.original.App.Config.Image = e.id.Predecessor
	e.original.App.Config.Env = []string{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgresql://test-only"}
	if err := os.WriteFile(filepath.Join(e.operation, "captured-migrator.url"), []byte("postgresql://test-only"), 0600); err != nil {
		t.Fatal(err)
	}
	e.original.Postgres.Config.Image = nativePGImage
	e.original.Caddy.Config.Image = "test-caddy"
	e.detached = false
	candidateEnvironment, err := e.candidateContainerEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	e.detached = true
	schema := e.request.Plan.CurrentSchema
	images := map[string]string{}
	migrated := false
	e.execute = func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch args[0] {
		case "compose", "start", "stop", "update":
			t.Fatalf("detached rehearsal touched live lifecycle: %s", call)
		case "rm":
			if !strings.Contains(call, e.clonePrefix()) {
				t.Fatalf("non-clone removal: %s", call)
			}
		case "inspect":
			if !strings.HasPrefix(args[1], e.clonePrefix()) {
				t.Fatalf("non-clone inspection: %s", call)
			}
			info := dockerInspection{}
			info.State.Running = true
			info.Config.Image = images[args[1]]
			info.NetworkSettings.Networks = map[string]json.RawMessage{e.clonePrefix(): json.RawMessage(`{"IPAddress":"172.25.0.2"}`)}
			raw, _ := json.Marshal([]dockerInspection{info})
			return string(raw), nil
		case "network":
			if args[1] == "create" && !slices.Contains(args, "--internal") {
				t.Fatal("clone network has external egress")
			}
		case "exec":
			if !strings.HasPrefix(args[1], e.clonePrefix()) {
				t.Fatalf("non-clone command: %s", call)
			}
			if strings.Contains(call, "pg_isready") {
				return "accepting connections", nil
			}
			if strings.Contains(call, "max(version_id)") {
				return fmt.Sprint(schema), nil
			}
			if strings.Contains(call, "count(*)") {
				return "0", nil
			}
			if strings.Contains(call, "version --json") || strings.Contains(call, "version --format json") {
				revision := e.request.PredecessorRevision
				if images[args[1]] == e.id.Candidate {
					revision = e.request.CandidateRevision
				}
				raw, _ := json.Marshal(buildinfo.Identity{Revision: revision})
				return string(raw), nil
			}
		case "run":
			if slices.Contains(args, "migrate") || slices.Contains(args, "rehearse") {
				t.Fatalf("used live migration authority: %s", call)
			}
			if slices.Contains(args, "migrate-copy") {
				if !strings.Contains(call, detachedStateName) {
					t.Fatal("missing separate migration authority")
				}
				schema = e.request.Plan.CandidateSchema
				migrated = true
				return "", nil
			}
			name := ""
			for i, arg := range args {
				if arg == "--name" {
					name = args[i+1]
				}
			}
			if !strings.HasPrefix(name, e.clonePrefix()) {
				t.Fatalf("non-clone container: %s", call)
			}
			for _, root := range e.original.Volumes {
				if strings.Contains(call, "src="+root+",") {
					t.Fatalf("mounted live volume: %s", call)
				}
			}
			images[name] = e.id.Predecessor
			if slices.Contains(args, e.id.Candidate) {
				images[name] = e.id.Candidate
			}
		}
		return "", nil
	}
	state := DetachedRehearsalState{Version: 1, Identity: e.id, RecoveryDigest: digest, CandidateEnvironmentDigest: candidateEnvironmentDigest(candidateEnvironment), Phase: DetachedReady}
	if err := writeDetachedState(e.operation, state); err != nil {
		t.Fatal(err)
	}
	if err := runDetachedRehearsal(t.Context(), e.operation, e.id, nativeDetachedEffects{e}); err == nil {
		t.Fatal("candidate browser disconnect accepted")
	}
	if !migrated {
		t.Fatal("test did not reach candidate migration")
	}
	for path, want := range live {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("live state changed: %s: %q %v", path, got, err)
		}
	}
	state, err = readDetachedState(e.operation)
	if err != nil || state.Phase != DetachedFailed {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestLiveUpgradeRequiresExactPassedDetachedRehearsal(t *testing.T) {
	request := nativeRequestFixture(t)
	request.Profile.StateRoot = t.TempDir()
	request.Profile.Root = t.TempDir()
	if validatePreparedRehearsal(request) == nil {
		t.Fatal("missing rehearsal accepted")
	}
	id, err := request.Identity()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(request.Profile.StateRoot, "upgrade-operations", strings.TrimPrefix(id.ArtifactAdmissionDigest, "sha256:"))
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	if err := os.WriteFile(filepath.Join(root, "request.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deployment.env", "leapview.env", ".host-install.json"} {
		if err := os.MkdirAll(filepath.Join(root, "original-config"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{request.Profile.Root, filepath.Join(root, "original-config")} {
			contents := []byte("captured configuration")
			if name == "leapview.env" {
				contents = []byte("EXISTING_SETTING=preserved\n")
			}
			if err := os.WriteFile(filepath.Join(dir, name), contents, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	const generatedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := securefs.WritePrivateFileAtomic(filepath.Join(root, "agent-credential-key"), []byte(generatedKey)); err != nil {
		t.Fatal(err)
	}
	original := nativeOriginal{App: dockerInspection{}}
	original.App.Config.Env = []string{"LEAPVIEW_IMAGE=" + request.PredecessorImage}
	originalRaw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(root, "original.json"), originalRaw); err != nil {
		t.Fatal(err)
	}
	preparedEffects := &NativeEffects{root: filepath.Join(root, "original-config"), provider: request.Profile.StateRoot, operation: root, request: request, id: id, original: original}
	preparedEnvironment, err := preparedEffects.candidateContainerEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	state := DetachedRehearsalState{Version: 1, Identity: id, RecoveryDigest: "sha256:" + hex64('b'), CandidateEnvironmentDigest: candidateEnvironmentDigest(preparedEnvironment), Phase: DetachedPassed}
	if err = writeDetachedState(root, state); err != nil {
		t.Fatal(err)
	}
	request.PreparationDigest = id.ArtifactAdmissionDigest
	request.DeploymentRunID = "456" // a new live operation, with a fresh snapshot
	if err := validatePreparedRehearsal(request); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Profile.Root, "leapview.env"), []byte("new setting"), 0600); err != nil {
		t.Fatal(err)
	}
	if validatePreparedRehearsal(request) == nil {
		t.Fatal("changed runtime configuration accepted")
	}
	if err := os.WriteFile(filepath.Join(request.Profile.Root, "leapview.env"), []byte("EXISTING_SETTING=preserved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request.Profile.RehearsalBinding = "127.0.0.1:19999"
	if validatePreparedRehearsal(request) == nil {
		t.Fatal("different host profile accepted")
	}
	request.Profile.RehearsalBinding = profileFixture().RehearsalBinding
	state.Phase = DetachedRunning
	if err := writeDetachedState(root, state); err != nil {
		t.Fatal(err)
	}
	if validatePreparedRehearsal(request) == nil {
		t.Fatal("incomplete clone accepted")
	}
}

func TestPreparedGeneratedAgentCredentialKeyIsReusedForLiveApply(t *testing.T) {
	prepared := nativeEffectsFixture(t)
	const expectedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	prepared.original.App.Config.Env = []string{"LEAPVIEW_IMAGE=" + prepared.id.Predecessor}
	preparedRoot := filepath.Join(prepared.provider, "upgrade-operations", strings.TrimPrefix(prepared.id.ArtifactAdmissionDigest, "sha256:"))
	if err := os.MkdirAll(filepath.Join(preparedRoot, "original-config"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deployment.env", "leapview.env", ".host-install.json"} {
		contents, err := os.ReadFile(filepath.Join(prepared.operation, "original-config", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = securefs.WritePrivateFileAtomic(filepath.Join(preparedRoot, "original-config", name), contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := securefs.WritePrivateFileAtomic(filepath.Join(preparedRoot, "agent-credential-key"), []byte(expectedKey)); err != nil {
		t.Fatal(err)
	}
	prepared.operation = preparedRoot
	preparedEnvironment, err := prepared.candidateContainerEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	originalRaw, err := json.Marshal(prepared.original)
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(preparedRoot, "original.json"), originalRaw); err != nil {
		t.Fatal(err)
	}
	requestRaw, err := json.Marshal(prepared.request)
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(preparedRoot, "request.json"), requestRaw); err != nil {
		t.Fatal(err)
	}
	state := DetachedRehearsalState{
		Version: 1, Identity: prepared.id, RecoveryDigest: "sha256:" + hex64('b'),
		CandidateEnvironmentDigest: candidateEnvironmentDigest(preparedEnvironment), Phase: DetachedPassed,
	}
	if err = writeDetachedState(preparedRoot, state); err != nil {
		t.Fatal(err)
	}

	applyRequest := prepared.request
	applyRequest.DeploymentRunID = "789"
	applyRequest.PreparationDigest = prepared.id.ArtifactAdmissionDigest
	applyID, err := applyRequest.Identity()
	if err != nil {
		t.Fatal(err)
	}
	applyOperation := filepath.Join(prepared.provider, "upgrade-operations", strings.TrimPrefix(applyID.ArtifactAdmissionDigest, "sha256:"))
	if err = os.MkdirAll(applyOperation, 0o700); err != nil {
		t.Fatal(err)
	}
	apply := &NativeEffects{
		root: prepared.root, provider: prepared.provider, operation: applyOperation,
		request: applyRequest, id: applyID, original: prepared.original,
	}

	environment, err := apply.candidateContainerEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	want := "LEAPVIEW_AGENT_CREDENTIAL_KEY=" + expectedKey
	if !strings.Contains(string(environment), want+"\n") {
		t.Fatalf("live apply did not reuse the key proven by detached rehearsal: %q", environment)
	}
	actual, err := os.ReadFile(filepath.Join(apply.operation, "agent-credential-key"))
	if err != nil || string(actual) != expectedKey {
		t.Fatalf("live operation key differs from rehearsal: %q (%v)", actual, err)
	}
	keyPath := filepath.Join(preparedRoot, "agent-credential-key")
	if err = securefs.WritePrivateFileAtomic(keyPath, []byte("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")); err != nil {
		t.Fatal(err)
	}
	if _, err = apply.candidateContainerEnvironment(); err == nil {
		t.Fatal("a changed prepared encryption key remained bound to the passed candidate environment")
	}
	if err = securefs.WritePrivateFileAtomic(keyPath, []byte("not-hex")); err != nil {
		t.Fatal(err)
	}
	if _, err = apply.candidateContainerEnvironment(); err == nil {
		t.Fatal("an invalid prepared encryption key was accepted")
	}
	if err = securefs.WritePrivateFileAtomic(keyPath, []byte(expectedKey)); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(keyPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = apply.candidateContainerEnvironment(); err == nil {
		t.Fatal("a prepared encryption key with permissive file mode was accepted")
	}
}
