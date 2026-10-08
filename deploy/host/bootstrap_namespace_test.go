package host_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This integration test needs an isolated mount namespace, never host mount changes.
// Run the compiled test binary as root to exercise hardened /run mount behavior.
func TestBootstrapWithNoexecRuntimeDirectory(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("requires Linux root and mount namespaces")
	}
	if output, err := exec.Command("unshare", "--mount", "--propagation", "private", "true").CombinedOutput(); err != nil {
		t.Skipf("mount namespace unavailable: %v: %s", err, output)
	}
	bootstrap, err := filepath.Abs("bootstrap-linux.sh")
	if err != nil {
		t.Fatal(err)
	}
	// NixOS exposes its tools through /run; retain their store paths before
	// hiding that mount with the isolated guest runtime directory.
	var commandPath []string
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		resolved, err := filepath.EvalSymlinks(entry)
		if err == nil && !strings.HasPrefix(resolved, "/run/") {
			commandPath = append(commandPath, resolved)
		}
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	bash, err = filepath.EvalSymlinks(bash)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		status     string
		controller bool
		diagnostic string
	}{
		{"success", "0", true, ""},
		{"controller-failure", "47", true, ""},
		{"copy-failure", "23", false, ""},
		{"non-executable", "1", false, "LeapView deployment controller is not executable on the payload filesystem"},
		{"noexec-payload", "1", false, "LeapView deployment controller is not executable on the payload filesystem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, value string, mode os.FileMode) string {
				t.Helper()
				value = strings.Replace(value, "#!/bin/bash", "#!"+bash, 1)
				path := filepath.Join(root, name)
				if err := os.WriteFile(path, []byte(value), mode); err != nil {
					t.Fatal(err)
				}
				return path
			}
			write("os-release", "ID=debian\nVERSION_ID=13\n", 0600)
			write("dpkg", "#!/bin/sh\nprintf 'amd64\\n'\n", 0700)
			write("systemctl", "#!/bin/sh\nexit 0\n", 0700)
			controller := write("controller", `#!/bin/bash
printf 'controller:%s\n' "$*" >>"$TEST_LOG"
[[ "$TEST_CASE" != controller-failure ]] || exit 47
`, 0500)
			write("docker", `#!/bin/bash
set -eu
case "$1" in
  compose|pull) ;;
  create) printf 'fixture-container\n' ;;
  cp)
    [[ "$TEST_CASE" != copy-failure ]] || exit 23
    cp "$TEST_CONTROLLER" "$3/leapviewctl"
    chmod 0500 "$3/leapviewctl"
    [[ "$TEST_CASE" != non-executable ]] || chmod 0400 "$3/leapviewctl"
    ;;
  rm) printf 'container-removed:%s\n' "$*" >>"$TEST_LOG" ;;
  *) exit 99 ;;
esac
`, 0700)
			fixture := write("fixture", `#!/bin/bash
set -euo pipefail
mount -t tmpfs -o noexec,nodev,nosuid tmpfs /run
mount -t tmpfs -o nodev,nosuid tmpfs /opt
mount --bind "$TEST_ROOT/os-release" "$(readlink -f /etc/os-release)"
if [[ "$TEST_CASE" == noexec-payload ]]; then
  mount -o remount,noexec /opt
fi
mkdir -p /run/leapview
printf 'ghcr.io/flidai/leapview@sha256:%064d\n' 0 >/run/leapview/image-reference
printf '{}\n' >/run/leapview/bootstrap.json
printf '{}\n' >/run/leapview/operator-bootstrap.json
cp "$TEST_CONTROLLER" /run/noexec-probe
chmod 0500 /run/noexec-probe
if test -x /run/noexec-probe; then exit 98; fi
status=0
bash "$TEST_BOOTSTRAP" install || status=$?
printf 'status:%s\n' "$status"
if compgen -G '/opt/leapview-payload.*' >/dev/null || compgen -G '/run/leapview-payload.*' >/dev/null; then
  printf 'payload-leaked\n'
  exit 97
fi
`, 0700)
			log := filepath.Join(root, "calls")
			cmd := exec.Command("unshare", "--mount", "--propagation", "private", "bash", fixture)
			cmd.Env = append(os.Environ(), "PATH="+root+":"+strings.Join(commandPath, ":"), "TEST_ROOT="+root,
				"TEST_BOOTSTRAP="+bootstrap, "TEST_CONTROLLER="+controller, "TEST_LOG="+log, "TEST_CASE="+tc.name)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("namespace fixture: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "status:"+tc.status+"\n") {
				t.Fatalf("unexpected installer status, want %s: %s", tc.status, output)
			}
			if tc.diagnostic != "" && !strings.Contains(string(output), tc.diagnostic) {
				t.Fatalf("missing bounded executable diagnostic: %s", output)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "container-removed:rm --force fixture-container\n") {
				t.Fatalf("temporary container was not removed: %s", calls)
			}
			if got := strings.Contains(string(calls), "controller:host install --config /run/leapview/bootstrap.json --payload /opt/leapview-payload."); got != tc.controller {
				t.Fatalf("controller invocation=%t, want %t: %s", got, tc.controller, calls)
			}
		})
	}
}
