//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

func transitionEffectsFixture(t *testing.T) (*NativeEffects, string, string) {
	t.Helper()
	e := nativeEffectsFixture(t)
	e.request.AccessTransition = accessTransitionRequestFixture(t).AccessTransition
	liveHome := filepath.Join(e.operation, "live-home")
	cloneHome := filepath.Join(e.operation, "rehearsal", "home")
	for _, home := range []string{liveHome, cloneHome} {
		if err := os.MkdirAll(filepath.Join(home, "certs"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "certs", "ca.pem"), []byte("test ca"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.original.Volumes = map[string]string{"home": liveHome, "postgres": filepath.Join(e.operation, "pg-data")}
	e.original.App.Config.Env = []string{
		"LEAPVIEW_IMAGE=" + e.id.Predecessor,
		"LEAPVIEW_AGENT_CREDENTIAL_KEY=" + strings.Repeat("0", 64),
		"LEAPVIEW_POSTGRES_CONTROL_URL=postgres://app:test@" + nativePG + "/leapview_control?sslmode=verify-full&sslrootcert=/var/lib/leapview/certs/ca.pem",
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://app:test@" + nativePG + "/leapview_ducklake?sslmode=verify-full",
		"LEAPVIEW_PUBLIC_URL=https://demo.leapview.dev",
	}
	e.original.App.Mounts = append(e.original.App.Mounts, struct {
		Type, Name, Source, Destination string
		RW                              bool
	}{Type: "volume", Name: e.request.Profile.Volumes["home"], Source: liveHome,
		Destination: "/var/lib/leapview", RW: true})
	for name, contents := range map[string]string{
		"request.json":     "{\"request\":\"test\"}",
		"reviewer.secret":  "reviewer-secret",
		"publisher.secret": "publisher-secret",
	} {
		if err := securefs.WritePrivateFileAtomic(filepath.Join(e.operation, name), []byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := json.Marshal(map[string]any{"version": 1, "state": map[string]any{"phase": "migrating"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = securefs.WritePrivateFileAtomic(filepath.Join(e.root, JournalName), journal); err != nil {
		t.Fatal(err)
	}
	return e, liveHome, cloneHome
}

func TestTransitionOnWithoutIntentDoesNothing(t *testing.T) {
	e := nativeEffectsFixture(t)
	calls := 0
	e.execute = func(context.Context, ...string) (string, error) {
		calls++
		return "", nil
	}
	if err := e.transitionOn(context.Background(), e.id, "bad-digest", "", false); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("no-intent transition made %d Docker calls", calls)
	}
}

func TestTransitionOnStagesPrivateInputsAndRunsExactCandidateAsRuntimeUser(t *testing.T) {
	e, liveHome, _ := transitionEffectsFixture(t)
	uid, gid := os.Getuid(), os.Getgid()
	var calls []string
	seenTransition := false
	seenOrphanCleanup := false
	e.execute = func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if args[0] == "run" && len(args) > 1 && args[1] == "--rm" && strings.Contains(call, "--entrypoint id") {
			if args[len(args)-1] == "-u" {
				return fmt.Sprint(uid), nil
			}
			return fmt.Sprint(gid), nil
		}
		if args[0] == "ps" {
			if !seenOrphanCleanup {
				seenOrphanCleanup = true
				return e.clonePrefix() + "-transition", nil
			}
			return "", nil
		}
		if args[0] == "rm" {
			return "", nil
		}
		if args[0] != "run" || !strings.Contains(call, " admin transition-access ") {
			return "", fmt.Errorf("unexpected Docker call: %s", call)
		}
		seenTransition = true
		if !strings.Contains(call, "--user "+fmt.Sprintf("%d:%d", uid, gid)) ||
			!strings.Contains(call, "--network "+nativeNetwork) ||
			!strings.Contains(call, "--entrypoint /usr/local/bin/leapview "+e.id.Candidate) ||
			!strings.Contains(call, "--mode live") ||
			!strings.Contains(call, "--recovery-digest sha256:"+hex64('f')) {
			t.Fatalf("wrong transition command: %s", call)
		}
		for _, expected := range []string{
			"--tmpfs /tmp:rw,nosuid,nodev,size=536870912,mode=1777",
			"--publisher-credential-file /upgrade/publisher.secret",
			"--reviewer-credential-file /upgrade/reviewer.secret",
			"dst=/upgrade/request.json,readonly",
			"dst=/upgrade-journal.json,readonly",
			"dst=/upgrade/publisher.secret,readonly",
			"dst=/upgrade/reviewer.secret,readonly",
			"dst=/var/lib/leapview",
		} {
			if !strings.Contains(call, expected) {
				t.Errorf("missing %q from command: %s", expected, call)
			}
		}
		if strings.Contains(call, "pg-data") || strings.Contains(call, "postgresql/data") {
			t.Fatalf("transition unexpectedly mounted database storage: %s", call)
		}
		if !strings.Contains(call, filepath.Join(e.operation, "transition.env")) {
			t.Fatalf("runtime environment file was not supplied: %s", call)
		}
		for _, name := range []string{"transition-request.json", "transition-journal.json", "transition-publisher.secret", "transition-reviewer.secret"} {
			path := filepath.Join(e.operation, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("staged %s unavailable: %v", name, err)
			}
			owner := info.Sys().(*syscall.Stat_t)
			if info.Mode().Perm() != 0o400 || owner.Uid != uint32(uid) || owner.Gid != uint32(gid) {
				t.Errorf("staged %s mode/owner = %v %d:%d", name, info.Mode().Perm(), owner.Uid, owner.Gid)
			}
		}
		env, err := os.ReadFile(filepath.Join(e.operation, "transition.env"))
		if err != nil || strings.Contains(string(env), "LEAPVIEW_AGENT_CREDENTIAL_KEY="+strings.Repeat("0", 64)) {
			t.Errorf("candidate agent credential key was not selected: %q (%v)", env, err)
		}
		if !strings.Contains(string(env), "LEAPVIEW_IMAGE="+e.id.Candidate+"\n") || strings.Contains(string(env), e.id.Predecessor) {
			t.Error("candidate application retained the predecessor image in its runtime configuration")
		}
		return transitionResultFixture(t, e), nil
	}

	if err := e.transitionOn(context.Background(), e.id, "sha256:"+hex64('f'), nativeNetwork, false); err != nil {
		t.Fatal(err)
	}
	if !seenTransition || !seenOrphanCleanup {
		t.Fatalf("transition/orphan cleanup missing: %v", calls)
	}
	cleanup, invoke := -1, -1
	for i, call := range calls {
		if strings.HasPrefix(call, "rm -f "+e.clonePrefix()+"-transition") {
			cleanup = i
		}
		if strings.Contains(call, " admin transition-access ") {
			invoke = i
		}
	}
	if cleanup < 0 || invoke <= cleanup {
		t.Fatalf("orphan transition was not removed before launch: %v", calls)
	}
	for _, name := range []string{"transition-request.json", "transition-journal.json", "transition-publisher.secret", "transition-reviewer.secret", "transition.env"} {
		if _, err := os.Lstat(filepath.Join(e.operation, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary transition input %s was not removed: %v", name, err)
		}
	}
	if liveHome == "" {
		t.Fatal("missing test home volume")
	}
}

func TestTransitionOnRehearsalUsesCloneHomeAndDetachedFence(t *testing.T) {
	e, liveHome, cloneHome := transitionEffectsFixture(t)
	const generatedKey = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	configuration := []byte("LEAPVIEW_AGENT_CREDENTIAL_KEY=" + generatedKey + "\n")
	for _, path := range []string{
		filepath.Join(e.root, "leapview.env"),
		filepath.Join(e.operation, "original-config", "leapview.env"),
	} {
		if err := securefs.WritePrivateFileAtomic(path, configuration); err != nil {
			t.Fatal(err)
		}
	}
	preparedEnvironment, err := e.candidateContainerEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	e.detached = true
	state := DetachedRehearsalState{
		Version: 1, Identity: e.id, RecoveryDigest: "sha256:" + hex64('b'),
		CandidateEnvironmentDigest: candidateEnvironmentDigest(preparedEnvironment), Phase: DetachedRunning,
	}
	if err := writeDetachedState(e.operation, state); err != nil {
		t.Fatal(err)
	}
	e.execute = func(_ context.Context, args ...string) (string, error) {
		call := strings.Join(args, " ")
		if args[0] == "run" && strings.Contains(call, "--entrypoint id") {
			if args[len(args)-1] == "-u" {
				return fmt.Sprint(os.Getuid()), nil
			}
			return fmt.Sprint(os.Getgid()), nil
		}
		if args[0] == "ps" {
			return "", nil
		}
		if args[0] != "run" || !strings.Contains(call, " admin transition-access ") {
			return "", nil
		}
		if !strings.Contains(call, "--mode detached") || !strings.Contains(call, cloneHome) || strings.Contains(call, liveHome) {
			t.Fatalf("detached transition did not use captured home/fence: %s", call)
		}
		if !strings.Contains(call, "dst=/upgrade-journal.json,readonly") {
			t.Fatalf("detached journal is not mounted read-only: %s", call)
		}
		return transitionResultFixture(t, e), nil
	}
	if err := e.transitionOn(context.Background(), e.id, "sha256:"+hex64('f'), "rehearsal-network", true); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionOnFailsClosedWhenSecretsOrCandidateIdentityAreInvalid(t *testing.T) {
	t.Run("missing reviewer credential", func(t *testing.T) {
		e, _, _ := transitionEffectsFixture(t)
		if err := os.Remove(filepath.Join(e.operation, "reviewer.secret")); err != nil {
			t.Fatal(err)
		}
		calls := 0
		e.execute = func(_ context.Context, args ...string) (string, error) {
			calls++
			if strings.Contains(strings.Join(args, " "), "--entrypoint id") {
				return fmt.Sprint(os.Getuid()), nil
			}
			return "", nil
		}
		if err := e.transitionOn(context.Background(), e.id, "sha256:"+hex64('f'), nativeNetwork, false); err == nil {
			t.Fatal("accepted a missing reviewer credential")
		}
		if calls < 2 {
			t.Fatal("candidate UID/GID were not resolved before validating staged inputs")
		}
	})
	t.Run("different operation identity", func(t *testing.T) {
		e, _, _ := transitionEffectsFixture(t)
		calls := 0
		e.execute = func(context.Context, ...string) (string, error) { calls++; return "", nil }
		changed := e.id
		changed.Candidate = e.id.Predecessor
		if err := e.transitionOn(context.Background(), changed, "sha256:"+hex64('f'), nativeNetwork, false); err == nil {
			t.Fatal("accepted another host operation identity")
		}
		if calls != 0 {
			t.Fatalf("invalid operation made Docker calls: %d", calls)
		}
	})
}

func TestParseCandidateRuntimeIDBounds(t *testing.T) {
	maxUint32 := uint64(^uint32(0))
	maxInt := uint64(^uint(0) >> 1)
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "zero", value: "0", want: 0},
		{name: "ordinary ID", value: "12345", want: 12345},
		{name: "negative ID", value: "-1", wantErr: true},
		{name: "signed ID", value: "+1", wantErr: true},
		{name: "noncanonical leading zero", value: "01", wantErr: true},
		{name: "above uint32", value: strconv.FormatUint(maxUint32+1, 10), wantErr: true},
		{name: "above host int", value: strconv.FormatUint(maxInt+1, 10), wantErr: true},
	}
	maxUint32Case := struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{name: "uint32 maximum", value: strconv.FormatUint(maxUint32, 10), wantErr: true}
	if maxInt >= maxUint32 {
		maxUint32Case.want = int(maxUint32)
		maxUint32Case.wantErr = false
	}
	tests = append(tests, maxUint32Case)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCandidateRuntimeID(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseCandidateRuntimeID(%q) = %d, want an error", test.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCandidateRuntimeID(%q) returned error: %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseCandidateRuntimeID(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestCandidateRuntimeIdentityRejectsIDsAboveUint32(t *testing.T) {
	tests := []struct {
		name string
		uid  string
		gid  string
	}{
		{name: "uid", uid: "4294967296", gid: "123"},
		{name: "gid", uid: "123", gid: "4294967296"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			e := nativeEffectsFixture(t)
			e.execute = func(_ context.Context, args ...string) (string, error) {
				switch args[len(args)-1] {
				case "-u":
					return test.uid, nil
				case "-g":
					return test.gid, nil
				default:
					return "", fmt.Errorf("unexpected id argument %q", args[len(args)-1])
				}
			}
			if uid, gid, err := e.candidateRuntimeIdentity(context.Background(), "candidate"); err == nil {
				t.Fatalf("candidateRuntimeIdentity accepted out-of-range identity %d:%d", uid, gid)
			}
		})
	}
}

// Docker Compose may reorder Config.Env when reopening the predecessor after
// capture. The same effective settings must remain bound to the passed receipt.
func TestCandidateEnvironmentRehearsalBinding(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, change := range []string{"reordered", "value", "added", "removed", "duplicate", "conflicting duplicate", "malformed"} {
			t.Run(fmt.Sprintf("detached=%t/%s", detached, change), func(t *testing.T) {
				e, _, _ := transitionEffectsFixture(t)
				config := []byte("LEAPVIEW_AGENT_CREDENTIAL_KEY=" + strings.Repeat("a", 64) + "\n")
				for _, path := range []string{filepath.Join(e.root, "leapview.env"), filepath.Join(e.operation, "original-config", "leapview.env")} {
					if err := securefs.WritePrivateFileAtomic(path, config); err != nil {
						t.Fatal(err)
					}
				}
				before, err := e.candidateContainerEnvironment()
				if err != nil {
					t.Fatal(err)
				}
				state := DetachedRehearsalState{Version: 1, Identity: e.id, RecoveryDigest: "sha256:" + hex64('b'), CandidateEnvironmentDigest: candidateEnvironmentDigest(before), Phase: DetachedPassed}
				receiptRoot := filepath.Join(e.provider, "upgrade-operations", strings.TrimPrefix(e.id.ArtifactAdmissionDigest, "sha256:"))
				if detached {
					e.detached = true
					state.Phase = DetachedRunning
					receiptRoot = e.operation
				} else {
					e.request.PreparationDigest = e.id.ArtifactAdmissionDigest
				}
				if err := os.MkdirAll(receiptRoot, 0700); err != nil {
					t.Fatal(err)
				}
				if err := writeDetachedState(receiptRoot, state); err != nil {
					t.Fatal(err)
				}
				env := e.original.App.Config.Env
				for left, right := 0, len(env)-1; left < right; left, right = left+1, right-1 {
					env[left], env[right] = env[right], env[left]
				}
				switch change {
				case "value":
					env[0] = "LEAPVIEW_PUBLIC_URL=https://different.example"
				case "added":
					env = append(env, "NEW_SETTING=changed")
				case "removed":
					env = env[1:]
				case "duplicate":
					env = append(env, env[0])
				case "conflicting duplicate":
					env = append(env, "LEAPVIEW_PUBLIC_URL=https://different.example")
				case "malformed":
					env = append(env, "INVALID_ENTRY")
				}
				e.original.App.Config.Env = env
				after, err := e.candidateContainerEnvironment()
				if change == "reordered" {
					if err != nil {
						t.Fatalf("unchanged effective configuration rejected after Docker reordered it: %v", err)
					}
					if string(before) != string(after) {
						t.Fatal("same effective environment produced different candidate configuration")
					}
				} else if err == nil {
					t.Fatalf("accepted %s candidate configuration", change)
				}
			})
		}
	}
}

func TestCandidateEnvironmentRejectsAmbiguousEntriesBeforeCapture(t *testing.T) {
	for _, entry := range []string{"LEAPVIEW_PUBLIC_URL=https://demo.leapview.dev", "LEAPVIEW_PUBLIC_URL=https://different.example", "INVALID_ENTRY", "=empty-key"} {
		t.Run(entry, func(t *testing.T) {
			e, _, _ := transitionEffectsFixture(t)
			e.original.App.Config.Env = append(e.original.App.Config.Env, entry)
			if _, err := e.candidateContainerEnvironment(); err == nil {
				t.Fatal("accepted ambiguous candidate environment before capture")
			}
		})
	}
}
