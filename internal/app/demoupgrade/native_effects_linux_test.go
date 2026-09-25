//go:build linux

package demoupgrade

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/buildinfo"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

func TestMigrationIntentRejectsStaleWrongAndPrematureJournals(t *testing.T) {
	r := nativeRequestFixture(t)
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	state := State{Identity: id, Phase: Migrating, RestoreRequired: true, RecoveryDigest: "sha256:" + hex64('f')}
	binary := buildinfo.Identity{Revision: r.CandidateRevision}
	if err = validateMigrationIntent(r, state, state.RecoveryDigest, binary); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{Prepared, Capturing, Verified, Starting, Committed, Restoring, Recovered} {
		changed := state
		changed.Phase = phase
		if validateMigrationIntent(r, changed, state.RecoveryDigest, binary) == nil {
			t.Fatalf("accepted %s", phase)
		}
	}
	changed := state
	changed.Identity.Predecessor = id.Candidate
	if validateMigrationIntent(r, changed, state.RecoveryDigest, binary) == nil {
		t.Fatal("accepted changed predecessor")
	}
	if validateMigrationIntent(r, state, "sha256:"+hex64('e'), binary) == nil {
		t.Fatal("accepted another recovery point")
	}
	binary.Dirty = true
	if validateMigrationIntent(r, state, state.RecoveryDigest, binary) == nil {
		t.Fatal("accepted dirty migrator")
	}
}
func nativeEffectsFixture(t *testing.T) *NativeEffects {
	t.Helper()
	root := t.TempDir()
	provider := t.TempDir()
	operation := filepath.Join(provider, "operation")
	if err := securefs.EnsurePrivateDir(filepath.Join(operation, "original-config")); err != nil {
		t.Fatal(err)
	}
	r := nativeRequestFixture(t)
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"deployment.env": "LEAPVIEW_IMAGE=" + id.Predecessor + "\nCADDY_DOMAIN=demo.leapview.dev\n", "leapview.env": "EXISTING_SETTING=preserved\n", ".host-install.json": "{}"} {
		if err = securefs.WritePrivateFileAtomic(filepath.Join(operation, "original-config", name), []byte(value)); err != nil {
			t.Fatal(err)
		}
		if err = securefs.WritePrivateFileAtomic(filepath.Join(root, name), []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	return &NativeEffects{root: root, provider: provider, operation: operation, id: id, request: r}
}
func TestPrivateTrafficConfigurationCannotExposePublicBindings(t *testing.T) {
	e := nativeEffectsFixture(t)
	if err := e.writeDeployment(e.id.Candidate, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(e.root, "deployment.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"LEAPVIEW_IMAGE=" + e.id.Candidate, "CADDY_HTTP_BIND=127.0.0.1:8082", "CADDY_HTTPS_BIND=127.0.0.1:8443", "CADDY_HTTPS_UDP_BIND=127.0.0.1:8443"} {
		if !strings.Contains(string(raw), line+"\n") {
			t.Fatalf("missing %s", line)
		}
	}
	if err = e.writeDeployment(e.id.Predecessor, false); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(e.root, "deployment.env"))
	original, _ := os.ReadFile(filepath.Join(e.operation, "original-config", "deployment.env"))
	if string(raw) != string(original) {
		t.Fatal("original bindings not restored")
	}
	if _, err = setDeploymentValue([]byte("KEY=1\nKEY=2\n"), "KEY", "3"); err == nil {
		t.Fatal("accepted ambiguous duplicate key")
	}
}
func TestAgentEncryptionKeyIsProvisionedOnceAndExistingConfigurationPreserved(t *testing.T) {
	e := nativeEffectsFixture(t)
	if err := e.ensureAgentKey(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(e.root, "leapview.env"))
	if !strings.Contains(string(first), "EXISTING_SETTING=preserved\n") {
		t.Fatal("changed existing config")
	}
	if err := e.ensureAgentKey(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(e.root, "leapview.env"))
	if string(first) != string(second) {
		t.Fatal("rotated encryption key")
	}
	info, _ := os.Stat(filepath.Join(e.root, "leapview.env"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestRecoveryStopsOrphanedMigratorBeforeDatabaseAndRestore(t *testing.T) {
	e := nativeEffectsFixture(t)
	var calls []string
	e.execute = func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if args[0] == "ps" {
			if strings.Contains(call, "-migrator$") {
				return e.clonePrefix() + "-migrator", nil
			}
			return "", nil
		}
		if args[0] == "inspect" {
			raw, _ := json.Marshal([]dockerInspection{{}})
			return string(raw), nil
		}
		return "", nil
	}
	if err := e.StopCandidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	index := func(fragment string) int {
		for i, call := range calls {
			if strings.Contains(call, fragment) {
				return i
			}
		}
		return -1
	}
	killed := index("rm -f -v " + e.clonePrefix() + "-migrator")
	stopped := index("stop --time 120 " + nativePG)
	if killed < 0 || stopped <= killed {
		t.Fatal(calls)
	}
	e.execute = func(_ context.Context, args ...string) (string, error) { return "", errors.New("Docker unavailable") }
	if e.StopCandidate(context.Background()) == nil {
		t.Fatal("recovery ignored failed fencing")
	}
}

func TestReopeningAfterRebootRestartsPostgresBeforeExposingTraffic(t *testing.T) {
	e := nativeEffectsFixture(t)
	var calls []string
	e.execute = func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if args[0] == "inspect" {
			info := dockerInspection{}
			info.Config.Image = e.id.Predecessor
			info.State.Running = true
			raw, _ := json.Marshal([]dockerInspection{info})
			return string(raw), nil
		}
		if strings.Contains(call, "pg_isready") {
			return "accepting connections", nil
		}
		if strings.Contains(call, "max(version_id)") {
			return "28", nil
		}
		if strings.Contains(call, "version --json") {
			raw, _ := json.Marshal(buildinfo.Identity{Revision: e.request.PredecessorRevision})
			return string(raw), nil
		}
		return "", nil
	}
	if err := e.ExposePredecessor(context.Background(), e.id); err != nil {
		t.Fatal(err)
	}
	find := func(fragment string) int {
		for i, call := range calls {
			if strings.Contains(call, fragment) {
				return i
			}
		}
		return -1
	}
	if find("start "+nativePG) < 0 || find("start "+nativePG) >= find("up -d --no-deps caddy") {
		t.Fatal("reopening did not ensure database/runtime first:", calls)
	}
}

type admittedNativeFixture struct{ *NativeEffects }

func (e admittedNativeFixture) Admit(context.Context, Identity) error { return nil }

// Exercise the concrete native file/configuration effects and durable journal
// together. Docker/SQL execution is deterministic here; the separate physical
// PostgreSQL qualification exercises the actual database engine and SQL.
func TestNativeFailedCandidateRestoresPairedFilesAndConfiguration(t *testing.T) {
	e := nativeEffectsFixture(t)
	e.reader = bufio.NewReader(strings.NewReader("commit\n")) // restore browser passes; candidate browser disconnects
	e.stdout = io.Discard
	e.original.Volumes = map[string]string{}
	for _, name := range []string{"postgres", "home", "caddy-data", "caddy-config"} {
		root := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		value := "before"
		if name == "postgres" {
			value = "28"
		}
		if err := os.WriteFile(filepath.Join(root, "state"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		e.original.Volumes[name] = root
	}
	e.original.Current = "releases/previous"
	running := map[string]bool{nativeApp: true, nativePG: true, nativeCaddy: true}
	images := map[string]string{nativeApp: e.id.Predecessor}
	pgRoots := map[string]string{nativePG: e.original.Volumes["postgres"]}
	e.original.Postgres.Config.Image = nativePGImage
	e.original.App.Config.Image = e.id.Predecessor
	e.original.Caddy.Config.Image = "test-caddy"
	e.original.App.Config.Env = []string{"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgresql://test-only"}
	e.original.Postgres.Mounts = append(e.original.Postgres.Mounts, struct {
		Type, Name, Source, Destination string
		RW                              bool
	}{Type: "volume", Source: e.original.Volumes["postgres"], Destination: "/var/lib/postgresql", RW: true})
	e.execute = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "inspect":
			info := dockerInspection{}
			info.State.Running = running[args[1]]
			info.Config.Image = images[args[1]]
			raw, _ := json.Marshal([]dockerInspection{info})
			return string(raw), nil
		case "stop":
			running[args[len(args)-1]] = false
		case "start":
			running[args[1]] = true
		case "ps":
			return "", nil
		case "network":
			return "", nil
		case "compose":
			name := args[len(args)-1]
			if name == "leapview" {
				raw, _ := os.ReadFile(filepath.Join(e.root, "deployment.env"))
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "LEAPVIEW_IMAGE=") {
						images[nativeApp] = strings.TrimPrefix(line, "LEAPVIEW_IMAGE=")
					}
				}
				running[nativeApp] = true
			} else if name == "caddy" {
				running[nativeCaddy] = true
			}
		case "exec":
			call := strings.Join(args, " ")
			if strings.Contains(call, "pg_isready") {
				return "accepting connections", nil
			}
			if strings.Contains(call, "count(*)") {
				return "0", nil
			}
			if strings.Contains(call, "max(version_id)") {
				raw, err := os.ReadFile(filepath.Join(pgRoots[args[1]], "state"))
				return string(raw), err
			}
			if strings.Contains(call, "version --json") {
				revision := e.request.PredecessorRevision
				if images[args[1]] == e.id.Candidate {
					revision = e.request.CandidateRevision
				}
				raw, _ := json.Marshal(buildinfo.Identity{Revision: revision})
				return string(raw), nil
			}
		case "run":
			if slices.Contains(args, "migrate") {
				if err := os.WriteFile(filepath.Join(e.original.Volumes["postgres"], "state"), []byte("30"), 0600); err != nil {
					return "", err
				}
				return "", os.WriteFile(filepath.Join(e.original.Volumes["home"], "state"), []byte("failed candidate"), 0600)
			}
			name := ""
			for i, arg := range args {
				if arg == "--name" {
					name = args[i+1]
				}
			}
			running[name] = true
			images[name] = e.id.Predecessor
			for i, arg := range args {
				if arg == "--mount" && strings.HasSuffix(args[i+1], ",dst=/var/lib/postgresql") {
					mount := strings.TrimSuffix(strings.TrimPrefix(args[i+1], "type=bind,src="), ",dst=/var/lib/postgresql")
					pgRoots[name] = mount
				}
			}
		}
		return "", nil
	}
	journal, err := OpenJournal(e.provider, e.id)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	coordinator := Coordinator{Journal: journal, Effects: admittedNativeFixture{e}}
	if err = coordinator.Run(t.Context(), e.id); err == nil {
		t.Fatal("candidate browser disconnect was approved")
	}
	state, err := journal.Load(t.Context())
	if err != nil || state.Phase != Validating || !state.RestoreRequired {
		t.Fatalf("missing recovery intent: %+v %v", state, err)
	}
	if err = coordinator.Recover(t.Context(), e.id); err != nil {
		t.Fatal(err)
	}
	for name, root := range e.original.Volumes {
		raw, err := os.ReadFile(filepath.Join(root, "state"))
		want := "before"
		if name == "postgres" {
			want = "28"
		}
		if err != nil || string(raw) != want {
			t.Fatalf("%s not restored: %s %v", name, raw, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(e.root, "leapview.env"))
	if strings.Contains(string(raw), "AGENT_CREDENTIAL_KEY") {
		t.Fatal("predecessor configuration not restored")
	}
	if images[nativeApp] != e.id.Predecessor || !running[nativePG] || !running[nativeApp] || !running[nativeCaddy] {
		t.Fatal("predecessor not restarted")
	}
	state, err = journal.Load(t.Context())
	if err != nil || state.Phase != Recovered {
		t.Fatalf("recovery not durable: %+v %v", state, err)
	}
}
