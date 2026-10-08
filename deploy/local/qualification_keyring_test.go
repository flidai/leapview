package local

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestReleasedAuthoringQualificationKeyringStopsDescendants(t *testing.T) {
	root := repositoryRoot(t)
	python := `import importlib.util, pathlib, sys, tempfile
spec = importlib.util.spec_from_file_location("with_keyring", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory() as temporary:
    root = pathlib.Path(temporary)
    descendant = root / "descendant.py"
    descendant.write_text('''import pathlib, signal, sys, time
root = pathlib.Path(sys.argv[1])
def finish(signum, frame):
    (root / "terminated").touch()
    sys.exit(0)
signal.signal(signal.SIGTERM, finish)
(root / "ready").touch()
while True: time.sleep(1)
''')
    leader = root / "leader.py"
    leader.write_text('''import pathlib, subprocess, sys, time
root = pathlib.Path(sys.argv[1])
subprocess.Popen([sys.executable, str(root / "descendant.py"), str(root)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
while not (root / "ready").exists(): time.sleep(0.01)
''')
    assert module.run_qualification([sys.executable, str(leader), str(root)], root / "interrupt") == 0
    assert (root / "terminated").exists(), "descendant survived its reaped command leader"
`
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-c", python, filepath.Join(root, "deploy/local/qualification/with_keyring.py"))
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("command descendant cleanup failed: %v\n%s", err, output)
	}
}

func TestReleasedAuthoringQualificationIsolatedKeyring(t *testing.T) {
	root := repositoryRoot(t)
	for _, exitCode := range []int{0, 23, 1, 130, 143} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			requireWriteFile(t, filepath.Join(bin, "dbus-run-session"), `#!/usr/bin/env python3
import os, sys
assert sys.argv[1] == '--'
assert 'DBUS_SESSION_BUS_ADDRESS' not in os.environ
os.environ['DBUS_SESSION_BUS_ADDRESS'] = 'unix:path=private-qualification-bus'
os.execvp(sys.argv[2], sys.argv[2:])
`, 0o700)
			requireWriteFile(t, filepath.Join(bin, "gnome-keyring-daemon"), `#!/usr/bin/env python3
import json, os, pathlib, sys, time
control = pathlib.Path(sys.argv[sys.argv.index('--control-directory') + 1])
assert 'GNOME_KEYRING_CONTROL' not in os.environ
assert os.environ['DBUS_SESSION_BUS_ADDRESS'] == 'unix:path=private-qualification-bus'
if '--unlock' in sys.argv:
    assert '--foreground' in sys.argv
    assert len(sys.stdin.read()) >= 32
    pathlib.Path(os.environ['DAEMON_STATE']).write_text(json.dumps({'pid': os.getpid(), 'home': os.environ['HOME']}))
    (control / 'control').touch()
    while True: time.sleep(1)
assert '--start' in sys.argv and '--components=secrets' in sys.argv
assert (control / 'control').exists()
if os.environ['PROBE_EXIT'] == '1': sys.exit(17)
`, 0o700)
			probe := filepath.Join(dir, "probe.py")
			requireWriteFile(t, probe, `import json, os, pathlib, sys, time
root = pathlib.Path(os.environ['HOME'])
assert root != pathlib.Path(os.environ['OPERATOR_HOME'])
assert root.stat().st_mode & 0o777 == 0o700
for name in ('XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR'):
    assert pathlib.Path(os.environ[name]).is_relative_to(root)
assert os.environ['DBUS_SESSION_BUS_ADDRESS'] == 'unix:path=private-qualification-bus'
assert 'GNOME_KEYRING_CONTROL' not in os.environ
assert sys.argv[1:] == ['argument with spaces', '--required']
if os.environ['PROBE_EXIT'] in ('130', '143'):
    pathlib.Path(os.environ['READY']).touch()
    try:
        while True: time.sleep(1)
    finally:
        # The harness reset/evidence finally block still needs the keyring.
        daemon = json.loads(pathlib.Path(os.environ['DAEMON_STATE']).read_text())
        os.kill(daemon['pid'], 0)
        time.sleep(0.2)
        os.kill(daemon['pid'], 0)
        pathlib.Path(os.environ['OBSERVATION']).write_text(json.dumps({'home': str(root), 'cwd': os.getcwd()}))
else:
    pathlib.Path(os.environ['OBSERVATION']).write_text(json.dumps({'home': str(root), 'cwd': os.getcwd()}))
sys.exit(int(os.environ['PROBE_EXIT']))
`, 0o600)
			observation := filepath.Join(dir, "observation.json")
			daemonState := filepath.Join(dir, "daemon.json")
			ready := filepath.Join(dir, "ready")
			command := exec.Command("python3", filepath.Join(root, "deploy/local/qualification/with_keyring.py"), "python3", probe, "argument with spaces", "--required")
			command.Dir = dir
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "DBUS_SESSION_BUS_ADDRESS=operator-bus", "GNOME_KEYRING_CONTROL=operator-keyring", "OPERATOR_HOME="+os.Getenv("HOME"), "OBSERVATION="+observation, "DAEMON_STATE="+daemonState, "PROBE_EXIT="+strconv.Itoa(exitCode), "READY="+ready)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			var err error
			if exitCode == 130 || exitCode == 143 {
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = command.Process.Kill() })
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("qualification command did not become ready for interruption")
					}
					time.Sleep(10 * time.Millisecond)
				}
				signum := syscall.SIGINT
				if exitCode == 143 {
					signum = syscall.SIGTERM
				}
				if err := command.Process.Signal(signum); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("interruption did not finish bounded cleanup")
				}
			} else {
				err = command.Run()
			}
			actual := 0
			if err != nil {
				if exited, ok := err.(*exec.ExitError); ok {
					actual = exited.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			wantExit := exitCode
			if exitCode == 143 {
				wantExit = 130
			}
			if actual != wantExit {
				t.Fatalf("exit=%d want %d: %s", actual, wantExit, output.String())
			}
			var daemon struct {
				PID  int    `json:"pid"`
				Home string `json:"home"`
			}
			state, err := os.ReadFile(daemonState)
			if err != nil {
				t.Fatalf("daemon did not start: %v\n%s", err, output.String())
			}
			if err := json.Unmarshal(state, &daemon); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(daemon.PID, 0); err != syscall.ESRCH {
				t.Fatalf("daemon %d was not reaped: %v", daemon.PID, err)
			}
			if _, err := os.Stat(daemon.Home); !os.IsNotExist(err) {
				t.Fatalf("temporary keyring state remains: %v", err)
			}
			if exitCode == 1 {
				if _, err := os.Stat(observation); !os.IsNotExist(err) {
					t.Fatalf("qualification ran despite failed keyring initialization: %v", err)
				}
				return
			}
			var observed struct {
				Home string `json:"home"`
				Cwd  string `json:"cwd"`
			}
			body, err := os.ReadFile(observation)
			if err != nil {
				t.Fatalf("probe did not execute: %v\n%s", err, output.String())
			}
			if err := json.Unmarshal(body, &observed); err != nil {
				t.Fatal(err)
			}
			if observed.Cwd != dir {
				t.Fatalf("wrapper changed cwd to %q", observed.Cwd)
			}
			if _, err := os.Stat(observed.Home); !os.IsNotExist(err) {
				t.Fatalf("temporary keyring state remains: %v", err)
			}
		})
	}
}
