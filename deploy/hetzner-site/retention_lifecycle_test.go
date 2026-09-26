package hetznersite_test

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type retentionFixture struct{ root, bin, log, current, candidate, previous string }

func newRetentionFixture(t *testing.T) retentionFixture {
	t.Helper()
	f := retentionFixture{root: t.TempDir(), current: siteImage("1"), candidate: siteImage("2"), previous: siteImage("3")}
	f.bin = filepath.Join(f.root, "bin")
	f.log = filepath.Join(f.root, "calls")
	if err := os.Mkdir(f.bin, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.root, "deployment.env"), "LEAPVIEW_SITE_IMAGE="+f.current+"\nCADDY_IMAGE=caddy@sha256:"+strings.Repeat("4", 64)+"\n", 0600)
	writeFile(t, filepath.Join(f.root, "deployed-image"), f.current+"\n", 0644)
	writeFile(t, filepath.Join(f.root, "previous-image"), f.previous+"\n", 0644)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(f.bin, "python3"), `#!/usr/bin/env bash
if [[ "$*" != *site_image_retention.py* ]]; then exec `+python+` "$@"; fi
printf 'retention %s\n' "$*" >> "$CALL_LOG"
if [[ "${POST_CLEANUP_FAILURE:-}" == yes && "$(cat "$FIXTURE_ROOT/deployed-image")" == "$CANDIDATE" ]]; then printf 'post-cleanup-failed\n' >> "$CALL_LOG"; exit 70; fi
exit "${RETENTION_STATUS:-0}"
`)
	writeExecutable(t, filepath.Join(f.bin, "docker"), `#!/usr/bin/env bash
printf 'docker %s\n' "$*" >> "$CALL_LOG"
if [[ "$*" == *http://leapview-site:8081/healthz* && -n "${PROBE_FAILURE:-}" ]]; then printf '%s\n' "$PROBE_FAILURE" >&2; exit 1; fi
if [[ "$*" == *"ps -q leapview-site"* ]]; then printf 'fixture-container\n'; exit 0; fi
if [[ "$*" == *".State.Running"* ]]; then printf 'true\n'; exit 0; fi
if [[ "$*" == *"image inspect"* && -n "${MISSING_IMAGE:-}" && "$*" == *"$MISSING_IMAGE"* ]]; then exit 1; fi
if [[ "$1" == pull ]]; then exit "${PULL_STATUS:-0}"; fi
if [[ "$*" == *inspect* ]]; then
 if [[ "$*" == *--format* ]]; then printf '%s\n' "$CANDIDATE"; else
 printf '[{"Id":"sha256:%s","RepoDigests":["%s"],"Descriptor":{"digest":"sha256:%s"}}]\n' "${CANDIDATE##*:}" "$CANDIDATE" "${CANDIDATE##*:}"
 fi
fi
`)
	writeExecutable(t, filepath.Join(f.bin, "sleep"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(f.bin, "systemctl"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(f.root, "provision.sh"), `#!/usr/bin/env bash
printf 'provision\n' >> "$CALL_LOG"
source "$FIXTURE_ROOT/deployment.env"
if [[ "$LEAPVIEW_SITE_IMAGE" == "$CANDIDATE" && "${CANDIDATE_STATUS:-0}" != 0 ]]; then exit "$CANDIDATE_STATUS"; fi
printf '%s\n' "$LEAPVIEW_SITE_IMAGE" > "$FIXTURE_ROOT/deployed-image"
`)
	return f
}
func (f retentionFixture) run(t *testing.T, script string, args []string, env ...string) (int, string) {
	t.Helper()
	body := strings.NewReplacer("/opt/leapview-site", f.root, "/var/lib/leapview-site", filepath.Join(f.root, "data")).Replace(readFile(t, filepath.Join("files", script)))
	path := filepath.Join(f.root, script)
	writeExecutable(t, path, body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", append([]string{path}, args...)...)
	command.Env = append(os.Environ(), "PATH="+f.bin+":"+os.Getenv("PATH"), "CALL_LOG="+f.log, "FIXTURE_ROOT="+f.root, "CANDIDATE="+f.candidate)
	command.Env = append(command.Env, env...)
	out, err := command.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode(), string(out)
	}
	t.Fatalf("run %s: %v", script, err)
	return -1, string(out)
}
func TestRetentionStopsDeploymentBeforePullWhenDiskIsLow(t *testing.T) {
	f := newRetentionFixture(t)
	status, out := f.run(t, "deploy.sh", []string{f.candidate}, "RETENTION_STATUS=71")
	if status != 71 {
		t.Fatalf("status=%d want71: %s", status, out)
	}
	calls := readPath(t, f.log)
	if strings.Contains(calls, "docker pull") || strings.Contains(calls, "provision") {
		t.Fatalf("mutated before headroom check: %s", calls)
	}
	if !strings.Contains(readPath(t, filepath.Join(f.root, "deployment.env")), f.current) {
		t.Fatal("active image changed")
	}
}
func TestRetentionPullFailureLeavesDeploymentRetryable(t *testing.T) {
	f := newRetentionFixture(t)
	status, out := f.run(t, "deploy.sh", []string{f.candidate}, "PULL_STATUS=69")
	if status == 0 {
		t.Fatalf("pull failure ignored: %s", out)
	}
	calls := readPath(t, f.log)
	if strings.Index(calls, "retention ") < 0 || strings.Index(calls, "retention ") > strings.Index(calls, "docker pull") {
		t.Fatalf("cleanup did not precede pull: %s", calls)
	}
	if strings.Contains(calls, "provision") {
		t.Fatal("activated after failed pull")
	}
	if !strings.Contains(readPath(t, filepath.Join(f.root, "deployment.env")), f.current) {
		t.Fatal("active image changed")
	}
	if _, err := os.Stat(filepath.Join(f.root, "failed-desired-image")); !os.IsNotExist(err) {
		t.Fatal("resource failure was permanently suppressed")
	}
}
func TestRetentionNoOpPreservesRollbackImage(t *testing.T) {
	f := newRetentionFixture(t)
	status, out := f.run(t, "deploy.sh", []string{f.current})
	if status != 0 {
		t.Fatalf("no-op failed: %d %s", status, out)
	}
	if strings.TrimSpace(readPath(t, filepath.Join(f.root, "previous-image"))) != f.previous {
		t.Fatal("no-op overwrote rollback image")
	}
}
func TestRetentionPostActivationFailureDoesNotRollbackHealthySite(t *testing.T) {
	f := newRetentionFixture(t)
	status, out := f.run(t, "deploy.sh", []string{f.candidate}, "POST_CLEANUP_FAILURE=yes")
	if status != 0 {
		t.Fatalf("maintenance rolled back healthy site: %d %s", status, out)
	}
	if !strings.Contains(readPath(t, f.log), "post-cleanup-failed") {
		t.Fatal("post-activation maintenance not attempted")
	}
	if strings.TrimSpace(readPath(t, filepath.Join(f.root, "deployed-image"))) != f.candidate {
		t.Fatal("healthy candidate not active")
	}
	if strings.TrimSpace(readPath(t, filepath.Join(f.root, "previous-image"))) != f.current {
		t.Fatal("current image not recorded for rollback")
	}
}
func TestReconcilerOnlySuppressesConfirmedApplicationFailure(t *testing.T) {
	for _, failure := range []int{69, 71, 74, 75, 76} {
		t.Run(strconv.Itoa(failure), func(t *testing.T) {
			f := newRetentionFixture(t)
			writeExecutable(t, filepath.Join(f.root, "deploy.sh"), "#!/usr/bin/env bash\nif [[ \"${1:-}\" == --recover ]]; then exit 0; fi\nexit "+strconv.Itoa(failure)+"\n")
			_, out := f.run(t, "reconcile.sh", nil)
			data, err := os.ReadFile(filepath.Join(f.root, "failed-desired-image"))
			if failure == 76 {
				if err != nil || strings.TrimSpace(string(data)) != f.candidate {
					t.Fatalf("confirmed failure not recorded: %v %s", err, out)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("retryable status%d suppressed: %s", failure, out)
			}
		})
	}
}
func TestRetentionEntrypointsRespectSharedMutationLock(t *testing.T) {
	for _, script := range []string{"deploy.sh", "reconcile.sh", "provision.sh"} {
		t.Run(script, func(t *testing.T) {
			f := newRetentionFixture(t)
			locker := exec.Command("flock", filepath.Join(f.root, "site-mutation.lock"), "sh", "-c", "echo locked; cat >/dev/null")
			input, err := locker.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := locker.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := locker.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = locker.Wait() }()
			scanner := bufio.NewScanner(output)
			if !scanner.Scan() || scanner.Text() != "locked" {
				t.Fatal("failed to acquire fixture lock")
			}
			var args []string
			if script == "deploy.sh" {
				args = []string{f.candidate}
			}
			status, out := f.run(t, script, args)
			if script != "reconcile.sh" && status != 75 {
				t.Fatalf("busy deploy status=%d: %s", status, out)
			}
			calls, err := os.ReadFile(f.log)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(calls) != 0 {
				t.Fatalf("mutated while lock held: %s", calls)
			}
		})
	}
}

func TestRetentionRecoveryFailureDoesNotReportSuccess(t *testing.T) {
	f := newRetentionFixture(t)
	writeFile(t, filepath.Join(f.root, "deployment-in-progress"), "version=broken\n", 0600)
	status, out := f.run(t, "deploy.sh", []string{"--recover"})
	if status != 65 {
		t.Fatalf("invalid recovery status=%d want65: %s", status, out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "deployment-in-progress")); err != nil {
		t.Fatal("invalid recovery record removed")
	}
}

func TestRetentionCandidateFailureClassificationSurvivesRollback(t *testing.T) {
	for _, failure := range []int{74, 76} {
		t.Run(strconv.Itoa(failure), func(t *testing.T) {
			f := newRetentionFixture(t)
			status, out := f.run(t, "deploy.sh", []string{f.candidate}, "CANDIDATE_STATUS="+strconv.Itoa(failure))
			if status != failure {
				t.Fatalf("candidate status%d became%d after rollback: %s", failure, status, out)
			}
			if strings.TrimSpace(readPath(t, filepath.Join(f.root, "deployed-image"))) != f.current {
				t.Fatal("rollback not verified")
			}
			if strings.TrimSpace(readPath(t, filepath.Join(f.root, "previous-image"))) != f.previous {
				t.Fatal("failed deployment changed previous successful rollback")
			}
		})
	}
}

func TestRetentionProvisionUsesCachedRollbackWithoutRegistry(t *testing.T) {
	f := newRetentionFixture(t)
	status, out := f.run(t, "provision.sh", nil, "PULL_STATUS=69")
	if status != 0 {
		t.Fatalf("cached rollback required registry: %d %s", status, out)
	}
	if strings.Contains(readPath(t, f.log), "docker pull") {
		t.Fatal("cached immutable rollback unnecessarily fetched from registry")
	}
}

func TestRetentionHealthFailureRequiresApplicationResponse(t *testing.T) {
	for _, tc := range []struct {
		name, probe string
		want        int
	}{
		{"proxy_stopped", "Error response from daemon: container caddy is not running", 74},
		{"dns_failure", "wget: bad address 'leapview-site:8081'", 74},
		{"connection_refused", "wget: can't connect to remote host: Connection refused", 74},
		{"http_failure", "  HTTP/1.1 503 Service Unavailable", 76},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRetentionFixture(t)
			status, out := f.run(t, "provision.sh", nil, "PROBE_FAILURE="+tc.probe)
			if status != tc.want {
				t.Fatalf("probe classified %d want%d: %s", status, tc.want, out)
			}
		})
	}
}

func TestRetentionProvisionReadsSelectedImageUnderLock(t *testing.T) {
	f := newRetentionFixture(t)
	writeExecutable(t, filepath.Join(f.bin, "flock"), `#!/usr/bin/env bash
/usr/bin/flock "$@" || exit "$?"
printf 'LEAPVIEW_SITE_IMAGE=%s\nCADDY_IMAGE=caddy@sha256:%s\n' "$CANDIDATE" "4444444444444444444444444444444444444444444444444444444444444444" > "$FIXTURE_ROOT/deployment.env"
`)
	status, out := f.run(t, "provision.sh", nil)
	if status != 0 {
		t.Fatalf("provision: %d %s", status, out)
	}
	if strings.TrimSpace(readPath(t, filepath.Join(f.root, "deployed-image"))) != f.candidate {
		t.Fatal("provision used state read before acquiring mutation lock")
	}
}

func writeRetentionTransaction(t *testing.T, f retentionFixture, phase string) {
	t.Helper()
	writeFile(t, filepath.Join(f.root, "deployment-in-progress"), "version=1\nprevious_image="+f.current+"\ncandidate_image="+f.candidate+"\nphase="+phase+"\n", 0600)
}
func TestRetentionRecoveryRejectsContradictoryRollbackPhase(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "rollback-failed")
	p := filepath.Join(f.root, "deployment.env")
	writeFile(t, p, strings.ReplaceAll(readPath(t, p), f.current, f.candidate), 0600)
	writeFile(t, filepath.Join(f.root, "deployed-image"), f.candidate+"\n", 0644)
	status, out := f.run(t, "deploy.sh", []string{"--recover"})
	if status != 65 {
		t.Fatalf("contradictory rollback phase became success: %d %s", status, out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "deployment-in-progress")); err != nil {
		t.Fatal("contradictory recovery record removed")
	}
}
func TestRetentionRecoveryRejectsMalformedDeployedReference(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "prepared")
	writeFile(t, filepath.Join(f.root, "deployed-image"), f.current[:40]+" \n"+f.current[40:]+"\n", 0644)
	status, out := f.run(t, "deploy.sh", []string{"--recover"})
	if status != 65 {
		t.Fatalf("malformed record normalized: %d %s", status, out)
	}
}
func TestRetentionRecoveryPreservesRecordWhenRollbackImageMissing(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "activating")
	p := filepath.Join(f.root, "deployment.env")
	writeFile(t, p, strings.ReplaceAll(readPath(t, p), f.current, f.candidate), 0600)
	writeFile(t, filepath.Join(f.root, "deployed-image"), f.candidate+"\n", 0644)
	status, out := f.run(t, "deploy.sh", []string{"--recover"}, "MISSING_IMAGE="+f.current)
	if status == 0 {
		t.Fatalf("missing rollback accepted: %s", out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "deployment-in-progress")); err != nil {
		t.Fatal("recovery record removed without rollback image")
	}
}
func TestRetentionRecoveryClearsResolvedTransactionScratch(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "prepared")
	scratch := filepath.Join(f.root, "deployment.env.next.fixture")
	body := strings.ReplaceAll(readPath(t, filepath.Join(f.root, "deployment.env")), f.current, f.candidate)
	writeFile(t, scratch, body, 0600)
	status, out := f.run(t, "deploy.sh", []string{"--recover"})
	if status != 0 {
		t.Fatalf("prepared recovery: %d %s", status, out)
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatal("resolved transaction scratch still pins candidate")
	}
}

func assertRetentionRollbackRecovered(t *testing.T, f retentionFixture) {
	t.Helper()
	status, out := f.run(t, "deploy.sh", []string{"--recover"})
	if status != 0 {
		t.Fatalf("recovery after clearing the fault: %d %s", status, out)
	}
	if !strings.Contains(readPath(t, filepath.Join(f.root, "deployment.env")), f.current) ||
		strings.TrimSpace(readPath(t, filepath.Join(f.root, "deployed-image"))) != f.current {
		t.Fatal("recovery did not restore and verify the previous active image")
	}
	if strings.TrimSpace(readPath(t, filepath.Join(f.root, "previous-image"))) != f.previous {
		t.Fatal("recovery changed the previous successful rollback image")
	}
	if _, err := os.Stat(filepath.Join(f.root, "deployment-in-progress")); !os.IsNotExist(err) {
		t.Fatal("verified recovery did not clear its transaction")
	}
}

func TestRetentionRecoveryRetriesFailedRollbackPhaseWrite(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "activating")
	envPath := filepath.Join(f.root, "deployment.env")
	writeFile(t, envPath, strings.ReplaceAll(readPath(t, envPath), f.current, f.candidate), 0600)
	writeExecutable(t, filepath.Join(f.bin, "mv"), `#!/usr/bin/env bash
if [[ "${FAIL_PHASE_WRITE:-}" == yes && "${@: -1}" == "$FIXTURE_ROOT/deployment-in-progress" ]]; then exit 1; fi
exec /usr/bin/mv "$@"
`)
	status, out := f.run(t, "deploy.sh", []string{"--recover"}, "FAIL_PHASE_WRITE=yes")
	if status != 74 {
		t.Fatalf("phase-write failure: %d %s", status, out)
	}
	if !strings.Contains(readPath(t, envPath), f.candidate) {
		t.Fatal("environment changed before rollback intent was recorded")
	}
	assertRetentionRollbackRecovered(t, f)
}

func TestRetentionRecoverySurvivesInterruptedEnvironmentRestore(t *testing.T) {
	for _, fault := range []string{"write_failure", "interruption_after_rename"} {
		t.Run(fault, func(t *testing.T) {
			f := newRetentionFixture(t)
			writeRetentionTransaction(t, f, "activating")
			envPath := filepath.Join(f.root, "deployment.env")
			writeFile(t, envPath, strings.ReplaceAll(readPath(t, envPath), f.current, f.candidate), 0600)
			writeExecutable(t, filepath.Join(f.bin, "mv"), `#!/usr/bin/env bash
if [[ -n "${RESTORE_FAULT:-}" && "${@: -1}" == "$FIXTURE_ROOT/deployment.env" ]]; then
 if [[ "$RESTORE_FAULT" == write_failure ]]; then exit 1; fi
 /usr/bin/mv "$@" || exit "$?"
 kill -TERM "$PPID"
 exit 0
fi
exec /usr/bin/mv "$@"
`)
			status, out := f.run(t, "deploy.sh", []string{"--recover"}, "RESTORE_FAULT="+fault)
			if status == 0 {
				t.Fatalf("injected recovery failure was ignored: %s", out)
			}
			if !strings.Contains(readPath(t, filepath.Join(f.root, "deployment-in-progress")), "phase=rollback-started\n") {
				t.Fatal("environment restoration left an incompatible transaction phase")
			}
			assertRetentionRollbackRecovered(t, f)
		})
	}
}

func TestRetentionDeploymentRetriesFailedRollbackEnvironmentRestore(t *testing.T) {
	for _, operation := range []string{"cp", "chmod", "mv"} {
		t.Run(operation, func(t *testing.T) {
			f := newRetentionFixture(t)
			writeExecutable(t, filepath.Join(f.bin, operation), `#!/usr/bin/env bash
if [[ "${FAIL_RESTORE:-}" == yes && "$*" == *deployment.env.restore.* ]]; then exit 1; fi
exec /usr/bin/`+operation+` "$@"
`)
			status, out := f.run(t, "deploy.sh", []string{f.candidate}, "CANDIDATE_STATUS=76", "FAIL_RESTORE=yes")
			if status != 77 {
				t.Fatalf("rollback environment failure: %d %s", status, out)
			}
			assertRetentionRollbackRecovered(t, f)
		})
	}
}

func TestRetentionRecoveryHonorsRecordedRollbackIntent(t *testing.T) {
	f := newRetentionFixture(t)
	writeRetentionTransaction(t, f, "rollback-started")
	envPath := filepath.Join(f.root, "deployment.env")
	writeFile(t, envPath, strings.ReplaceAll(readPath(t, envPath), f.current, f.candidate), 0600)
	writeFile(t, filepath.Join(f.root, "deployed-image"), f.candidate+"\n", 0644)
	assertRetentionRollbackRecovered(t, f)
}
