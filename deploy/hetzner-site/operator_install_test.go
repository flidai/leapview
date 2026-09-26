package hetznersite_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the remote installer locally with disposable paths and commands. A
// partial install must restore the whole script set before the timer resumes.
func TestOperatorInstallerRestoresFailedInstall(t *testing.T) {
	for _, name := range []string{"success", "partial_failure", "rollback_failure"} {
		fail := name != "success"
		rollbackFailure := name == "rollback_failure"
		t.Run(name, func(t *testing.T) {
			contents, err := os.ReadFile("../../scripts/deploy_site.sh")
			if err != nil {
				t.Fatal(err)
			}
			sections := strings.Split(string(contents), "<<'REMOTE_INSTALL'\n")
			if len(sections) != 2 {
				t.Fatal("missing isolated remote installer")
			}
			script := strings.SplitN(sections[1], "\nREMOTE_INSTALL", 2)[0]
			root := t.TempDir()
			site, units, stage, bin := filepath.Join(root, "site"), filepath.Join(root, "units"), filepath.Join(root, "stage"), filepath.Join(root, "bin")
			for _, dir := range []string{site, units, stage, bin} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			script = strings.NewReplacer("/opt/leapview-site", site, "/etc/systemd/system", units, "/root/", stage+"/").Replace(script)
			originals := map[string]string{}
			for _, file := range []string{"compose.yaml", "deploy.sh", "provision.sh", "reconcile.sh"} {
				p := filepath.Join(site, file)
				originals[p] = "old-" + file
				writeFile(t, p, originals[p], 0600)
			}
			for _, file := range []string{"leapview-site-reconcile.service", "leapview-site-reconcile.timer"} {
				p := filepath.Join(units, file)
				originals[p] = "old-" + file
				writeFile(t, p, originals[p], 0600)
			}
			for _, file := range []string{"compose", "deploy", "provision", "reconcile", "reconcile-service", "reconcile-timer"} {
				writeFile(t, filepath.Join(stage, map[string]string{"compose": "compose.yaml", "deploy": "deploy.sh", "provision": "provision.sh", "reconcile": "reconcile.sh", "reconcile-service": "leapview-site-reconcile.service", "reconcile-timer": "leapview-site-reconcile.timer"}[file]), "# new "+file+"\n", 0600)
			}
			writeFile(t, filepath.Join(stage, "site_image_retention.py"), "# new helper\n", 0600)
			writeExecutable(t, filepath.Join(bin, "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALL_LOG\"\n")
			writeExecutable(t, filepath.Join(bin, "install"), `#!/usr/bin/env bash
set -euo pipefail
args=()
while [[ $# -gt 0 ]]; do
 case "$1" in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
done
target="${args[${#args[@]}-1]}"
if [[ "${FAIL_INSTALL:-}" == yes && "$target" == */reconcile.sh ]]; then exit 7; fi
/usr/bin/install "${args[@]}"
`)
			writeExecutable(t, filepath.Join(bin, "cp"), `#!/usr/bin/env bash
if [[ "${FAIL_RESTORE:-}" == yes && "${2:-}" == *operator-install.* ]]; then exit 8; fi
exec /bin/cp "$@"
`)
			path := filepath.Join(root, "install.sh")
			writeFile(t, path, script, 0600)
			hash := exec.Command("bash", "-c", "sha256sum ./* > SHA256SUMS")
			hash.Dir = stage
			if out, err := hash.CombinedOutput(); err != nil {
				t.Fatalf("checksum: %v %s", err, out)
			}
			command := exec.Command("bash", path, stage)
			log := filepath.Join(root, "calls")
			command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "CALL_LOG="+log)
			if fail {
				command.Env = append(command.Env, "FAIL_INSTALL=yes")
			}
			if rollbackFailure {
				command.Env = append(command.Env, "FAIL_RESTORE=yes")
			}
			output, err := command.CombinedOutput()
			if fail && err == nil {
				t.Fatal("partial installation unexpectedly succeeded")
			}
			if !fail && err != nil {
				t.Fatalf("install: %v\n%s", err, output)
			}
			if rollbackFailure {
				if _, err := os.Stat(filepath.Join(stage, "resume-safe")); !os.IsNotExist(err) {
					t.Fatal("incomplete rollback marked safe to resume")
				}
				calls, err := os.ReadFile(log)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(calls), "start leapview-site-reconcile.timer") {
					t.Fatal("timer resumed after incomplete rollback")
				}
				return
			}
			if _, err := os.Stat(filepath.Join(stage, "resume-safe")); err != nil {
				t.Fatal("complete installer missing safe-resume marker:", err)
			}
			for path, before := range originals {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if fail && string(data) != before {
					t.Errorf("did not restore %s", path)
				}
				if !fail && !strings.HasPrefix(string(data), "# new") {
					t.Errorf("did not install %s", path)
				}
			}
			_, err = os.Stat(filepath.Join(site, "site_image_retention.py"))
			if fail && !os.IsNotExist(err) {
				t.Fatal("new helper remained after rollback")
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if fail && !strings.Contains(string(calls), "start leapview-site-reconcile.timer") {
				t.Fatalf("timer not resumed: %s", calls)
			}
			if strings.Contains(string(calls), "stop leapview-site-reconcile.service") {
				t.Fatal("installer terminated an active deployment")
			}
		})
	}
}
