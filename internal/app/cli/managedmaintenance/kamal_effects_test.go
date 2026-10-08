//go:build linux

package managedmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

func adapterFixture(t *testing.T) (*KamalEffects, containerInfo, containerInfo) {
	t.Helper()
	// Unix socket names have a small platform limit; avoid nested test names.
	root, err := os.MkdirTemp("", "lv-kamal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := HostProfile{Version: 1, Target: "fixture", Root: filepath.Join(root, "operator"), StateRoot: filepath.Join(root, "controller"), Home: filepath.Join(root, "home"), Service: "leapview", Hostname: "dash.example.com", ProxyImage: "basecamp/kamal-proxy@sha256:" + strings.Repeat("c", 64), AdmissionRoot: filepath.Join(root, "admission")}
	p.Socket = filepath.Join(p.Home, "maintenance.sock")
	p.Capacity = &CapacityPolicy{DockerRootDir: filepath.Join(root, "docker"), Home: CapacityReserve{1, 1}, StateRoot: CapacityReserve{1, 1}, Docker: CapacityReserve{1, 1}}
	for _, path := range []string{p.Root, p.StateRoot, p.Home, p.AdmissionRoot, p.Capacity.DockerRootDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.Root, "environment.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	r := requestFixture()
	app := containerInfo{ID: "app-id", Name: "/leapview-web-" + r.Predecessor.Revision, Image: "sha256:retained-content"}
	app.Config.Image = r.Predecessor.Image
	app.Config.Labels = map[string]string{"service": p.Service, "role": "web"}
	app.Config.Env = []string{"LEAPVIEW_HOME=" + p.Home, "LEAPVIEW_MAINTENANCE_SOCKET=" + p.Socket, "LEAPVIEW_CSRF_KEY=private-fixture"}
	app.HostConfig.NetworkMode = "kamal"
	app.Mounts = []mountInfo{{Type: "bind", Source: p.Home, Destination: p.Home, RW: true}}
	app.State.Running = true
	r.Predecessor.CredentialDigest = environmentIdentity(app.Config.Env)
	r.Candidate.CredentialDigest = r.Predecessor.CredentialDigest
	proxy := containerInfo{ID: "proxy-id", Name: "/kamal-proxy"}
	proxy.Config.Image, proxy.HostConfig.NetworkMode, proxy.State.Running = p.ProxyImage, "kamal", true
	return &KamalEffects{Profile: p, Request: r}, app, proxy
}

func privateControl(t *testing.T, k *KamalEffects, handler http.HandlerFunc) {
	t.Helper()
	listener, err := net.Listen("unix", k.Profile.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(k.Profile.Socket, 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestKamalPrivateControlBindsRevisionOperationAndLease(t *testing.T) {
	for _, variant := range []string{"valid", "wrong-revision", "wrong-operation", "rejected"} {
		t.Run(variant, func(t *testing.T) {
			k, _, _ := adapterFixture(t)
			operation, err := k.Request.Digest()
			if err != nil {
				t.Fatal(err)
			}
			privateControl(t, k, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/open" {
					t.Errorf("unexpected private request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Revision, Operation string
					LeaseMilliseconds   int64
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Revision != k.Request.Candidate.Revision || body.Operation != operation || body.LeaseMilliseconds != k.Request.Budgets.Phase.Milliseconds() {
					t.Errorf("unbound maintenance request: %+v", body)
				}
				status := controlStatus{Revision: body.Revision, Operation: operation, State: "provisional"}
				switch variant {
				case "wrong-revision":
					status.Revision = k.Request.Predecessor.Revision
				case "wrong-operation":
					status.Operation = "sha256:" + strings.Repeat("f", 64)
				case "rejected":
					http.Error(w, "private diagnostic credential", http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(status)
			})
			err = k.OpenWork(t.Context(), k.Request.Candidate)
			if (err == nil) != (variant == "valid") {
				t.Fatalf("OpenWork: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private diagnostic credential") {
				t.Fatal("private control diagnostic leaked")
			}
		})
	}
}

type inventoryDouble struct {
	t          *testing.T
	items      []containerInfo
	stopCount  int
	exitCode   int
	oom        bool
	commands   []string
	dockerRoot string
}

func (d *inventoryDouble) docker(args []string) ([]byte, error) {
	d.t.Helper()
	if len(args) < 4 || !reflect.DeepEqual(args[:2], []string{"--host", "unix:///var/run/docker.sock"}) {
		d.t.Fatalf("Docker daemon is not pinned: %v", args)
	}
	args = args[2:]
	d.commands = append(d.commands, strings.Join(args, " "))
	switch {
	case reflect.DeepEqual(args, []string{"info", "--format", "{{json .DockerRootDir}}"}):
		return json.Marshal(d.dockerRoot)
	case reflect.DeepEqual(args, []string{"container", "ls", "--all", "--quiet", "--no-trunc"}):
		var ids []string
		for _, item := range d.items {
			ids = append(ids, item.ID)
		}
		return []byte(strings.Join(ids, "\n")), nil
	case args[0] == "container" && args[1] == "inspect":
		var found []containerInfo
		for _, id := range args[2:] {
			for _, item := range d.items {
				if item.ID == id {
					found = append(found, item)
				}
			}
		}
		return json.Marshal(found)
	case len(args) == 5 && reflect.DeepEqual(args[:4], []string{"container", "stop", "--time", "-1"}):
		d.stopCount++
		for index := range d.items {
			if d.items[index].ID == args[4] {
				d.items[index].State.Running = false
				d.items[index].State.ExitCode = d.exitCode
				d.items[index].State.OOMKilled = d.oom
			}
		}
		return nil, nil
	case len(args) == 3 && args[0] == "image" && args[1] == "inspect":
		return json.Marshal([]imageInfo{{ID: "sha256:retained-content", RepoDigests: []string{args[2]}}})
	default:
		d.t.Fatalf("unexpected Docker operation: %v", args)
		return nil, errors.New("unexpected Docker operation")
	}
}

func TestKamalDrainRequiresCompletedDrainAndCleanExit(t *testing.T) {
	cases := []struct {
		name, state                     string
		drained                         bool
		exitCode                        int
		oom, heldLock, wantStop, wantOK bool
	}{
		{name: "still-draining", state: "draining"},
		{name: "failed-undrained", state: "failed"},
		{name: "closed-undrained", state: "closed"},
		{name: "open", state: "provisional", drained: true},
		{name: "clean", state: "closed", drained: true, wantStop: true, wantOK: true},
		{name: "nonzero-exit", state: "closed", drained: true, exitCode: 137, wantStop: true},
		{name: "oom", state: "closed", drained: true, oom: true, wantStop: true},
		{name: "home-still-owned", state: "closed", drained: true, heldLock: true, wantStop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, app, proxy := adapterFixture(t)
			d := &inventoryDouble{t: t, items: []containerInfo{app, proxy}, exitCode: tc.exitCode, oom: tc.oom}
			k.run = func(_ context.Context, bin string, args, _ []string, _ string) ([]byte, error) {
				if bin != "docker" {
					t.Fatalf("unexpected process: %s", bin)
				}
				return d.docker(args)
			}
			privateControl(t, k, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/status" {
					t.Errorf("unexpected drain request: %s %s", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(controlStatus{Revision: app.Name[len("/leapview-web-"):], State: tc.state, Drained: tc.drained})
			})
			if tc.heldLock {
				lock, err := instancelock.Acquire(k.Profile.Home)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			}
			err := k.DrainAndStop(t.Context())
			if (err == nil) != tc.wantOK {
				t.Fatalf("DrainAndStop: %v", err)
			}
			if (d.stopCount > 0) != tc.wantStop {
				t.Fatalf("Docker stop count %d", d.stopCount)
			}
		})
	}
}

func TestKamalCloseIngressRecreatesMissingProxyWithInheritedLock(t *testing.T) {
	k, _, proxy := adapterFixture(t)
	journal, err := OpenJournal(k.Profile.StateRoot, k.Request)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	k.lockFile = journal.InheritedLockFile()
	d := &inventoryDouble{t: t}
	called := false
	k.run = func(ctx context.Context, bin string, args, env []string, dir string) ([]byte, error) {
		if bin == "docker" {
			return d.docker(args)
		}
		want := []string{"exec", "ruby", "-r", "./maintenance_adapter.rb", "-S", "kamal", "proxy", "reboot", "--confirmed", "--config-file", "deploy.yml", "--version", k.Request.Predecessor.Revision, "--skip-hooks"}
		if bin != "bundle" || !reflect.DeepEqual(args, want) {
			t.Fatalf("unexpected maintenance command: %s %v", bin, args)
		}
		gate, err := os.ReadFile(filepath.Join(k.Profile.StateRoot, "ingress.json"))
		if err != nil || string(gate) != `{"publish":false}` {
			t.Fatalf("proxy mutation preceded durable closure: %s %v", gate, err)
		}
		output, err := runCommandWithLock(ctx, os.Args[0], []string{"-test.run=^TestManagedAdapterInheritedLockProcess$"}, append(env, "LEAPVIEW_TEST_INHERITED_LOCK=1"), dir, k.lockFile)
		if err != nil || !strings.Contains(string(output), "inherited-lock-confirmed") {
			t.Fatalf("child lock evidence: %s %v", output, err)
		}
		called = true
		d.items = []containerInfo{proxy}
		return nil, nil
	}
	if err := k.CloseIngress(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("missing proxy was not recreated")
	}
}

func TestManagedAdapterInheritedLockProcess(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_INHERITED_LOCK") != "1" {
		return
	}
	if os.Getenv("LEAPVIEW_MANAGED_LOCK_FD") != "3" {
		t.Fatal("controller lock descriptor was not supplied")
	}
	file := os.NewFile(3, "inherited-controller-lock")
	actual, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(os.Getenv("LEAPVIEW_MANAGED_LOCK_PATH"))
	if err != nil || !os.SameFile(actual, expected) {
		t.Fatal("inherited descriptor differs from controller lock")
	}
	if err := syscall.Flock(3, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("inherited lock is not shared: %v", err)
	}
	fmt.Println("inherited-lock-confirmed")
}

func TestKamalRollbackBootUsesRetainedPredecessorAndVerifiesPreparedIdentity(t *testing.T) {
	k, app, proxy := adapterFixture(t)
	d := &inventoryDouble{t: t, items: []containerInfo{proxy}, dockerRoot: k.Profile.Capacity.DockerRootDir}
	booted := false
	k.run = func(_ context.Context, bin string, args, env []string, _ string) ([]byte, error) {
		if bin == "docker" {
			return d.docker(args)
		}
		want := []string{"exec", "ruby", "-r", "./maintenance_adapter.rb", "-S", "kamal", "app", "boot", "--config-file", "deploy.yml", "--version", k.Request.Predecessor.Revision, "--skip-hooks"}
		if bin != "bundle" || !reflect.DeepEqual(args, want) {
			t.Fatalf("rollback attempted unsupported mutation: %s %v", bin, args)
		}
		values := strings.Join(env, "\n")
		if !strings.Contains(values, "LEAPVIEW_MANAGED_IMAGE="+k.Request.Predecessor.Image+"\n") || !strings.Contains(values, "LEAPVIEW_MANAGED_REVISION="+k.Request.Predecessor.Revision+"\n") {
			t.Fatal("rollback did not select retained predecessor identity")
		}
		booted = true
		d.items = []containerInfo{app, proxy}
		return nil, nil
	}
	operation, err := k.Request.Digest()
	if err != nil {
		t.Fatal(err)
	}
	privateControl(t, k, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prepare" {
			t.Errorf("unexpected rollback control: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(controlStatus{Revision: k.Request.Predecessor.Revision, Operation: operation, State: "prepared"})
	})
	if err := k.StartPrepared(t.Context(), k.Request.Predecessor); err != nil {
		t.Fatal(err)
	}
	if err := k.VerifyPrepared(t.Context(), k.Request.Predecessor); err != nil {
		t.Fatal(err)
	}
	if !booted {
		t.Fatal("retained predecessor was not booted")
	}
	for _, command := range d.commands {
		if strings.Contains(command, "pull") || strings.Contains(command, "migrat") {
			t.Fatalf("rollback mutated retained artifacts/state: %s", command)
		}
	}
	// An image substitution after boot cannot pass the private prepare boundary.
	d.items[0].Image = "sha256:unexpected-content"
	if err := k.VerifyPrepared(t.Context(), k.Request.Predecessor); err == nil {
		t.Fatal("prepared image substitution accepted")
	}
}
