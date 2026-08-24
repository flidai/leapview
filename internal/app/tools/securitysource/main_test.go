package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

type scannerFixture struct {
	root string
	bin  string
	log  string
}

func newScannerFixture(t *testing.T) scannerFixture {
	t.Helper()
	root := t.TempDir()
	bin := t.TempDir()
	log := filepath.Join(root, "scanner.log")
	for _, relative := range []string{"tracked.txt", "nested/go.mod"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "--quiet")
	runGit(t, root, "config", "user.email", "security-test@example.test")
	runGit(t, root, "config", "user.name", "Security Test")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "--quiet", "-m", "baseline")
	runGit(t, root, "branch", "-M", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "--quiet", "-m", "candidate")
	runGit(t, root, "branch", "origin/main", "HEAD~1")

	goShim := `#!/bin/sh
printf 'go|%s\n' "$*" >> "$SECURITY_TEST_LOG"
phase=""
case "$*" in
  *" dir "*) phase=current ;;
  *" git "*) phase=history ;;
esac
if [ "$phase" = "${SECURITY_TEST_GO_FAILURE:-}" ]; then
  printf 'gitleaks scanner unavailable [REDACTED]\n' >&2
  exit 127
fi
exit "${SECURITY_TEST_GO_STATUS:-0}"
`
	dockerShim := `#!/bin/sh
printf 'docker|%s\n' "$*" >> "$SECURITY_TEST_LOG"
if [ "$1" = info ]; then exit 0; fi
case "${SECURITY_TEST_SOURCE_FAILURE:-}" in
  secret) printf 'secret finding: [REDACTED]\n' >&2; exit 1 ;;
  misconfiguration) printf 'HIGH infrastructure misconfiguration\n' >&2; exit 1 ;;
  unavailable) printf 'source scanner unavailable\n' >&2; exit 127 ;;
esac
exit 0
`
	trivyShim := `#!/bin/sh
printf 'trivy|%s\n' "$*" >> "$SECURITY_TEST_LOG"
printf '{"Results":[]}\n'
`
	writeExecutable(t, filepath.Join(bin, "go"), goShim)
	writeExecutable(t, filepath.Join(bin, "docker"), dockerShim)
	writeExecutable(t, filepath.Join(bin, "trivy"), trivyShim)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SECURITY_TEST_LOG", log)
	return scannerFixture{root: root, bin: bin, log: log}
}

func TestRunCleanScansCurrentTreeAndCandidateHistory(t *testing.T) {
	fixture := newScannerFixture(t)
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Config{Root: fixture.root, BaseRef: "origin/main", Timeout: 5 * time.Second, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("Run() error = %v\nstderr: %s", err, stderr.String())
	}
	log := readFixtureLog(t, fixture.log)
	if !strings.Contains(log, "go|run github.com/zricethezav/gitleaks/v8@v8.30.1 dir") || !strings.Contains(log, "--redact=100") || !strings.Contains(log, "--timeout=300") {
		t.Fatalf("current-tree gitleaks invocation missing from log:\n%s", log)
	}
	if !strings.Contains(log, "go|run github.com/zricethezav/gitleaks/v8@v8.30.1 git") || !strings.Contains(log, "--log-opts=origin/main..HEAD") {
		t.Fatalf("candidate-history gitleaks invocation missing from log:\n%s", log)
	}
	if !strings.Contains(log, "docker|run --rm") || !strings.Contains(log, trivyImage) || !strings.Contains(log, "--scanners secret,misconfig") || !strings.Contains(log, "--severity HIGH,CRITICAL") {
		t.Fatalf("pinned Trivy invocation missing from log:\n%s", log)
	}
	if strings.Contains(log, "trivy|") {
		t.Fatalf("source gate trusted a mutable local Trivy binary:\n%s", log)
	}
}

func TestRunRejectsSecretFindingWithoutLeakingValue(t *testing.T) {
	fixture := newScannerFixture(t)
	const secret = "sentinel_value_never_logged_123"
	if err := os.WriteFile(filepath.Join(fixture.root, ".env"), []byte("TOKEN="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECURITY_TEST_SOURCE_FAILURE", "secret")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Config{Root: fixture.root, BaseRef: "origin/main", Timeout: 5 * time.Second, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("Run() accepted secret finding")
	}
	if !strings.Contains(stderr.String(), "[REDACTED]") || strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatalf("secret output was not redacted: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsIaCMisconfiguration(t *testing.T) {
	fixture := newScannerFixture(t)
	t.Setenv("SECURITY_TEST_SOURCE_FAILURE", "misconfiguration")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Config{Root: fixture.root, BaseRef: "origin/main", Timeout: 5 * time.Second, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("Run() accepted IaC misconfiguration")
	}
	if !strings.Contains(stderr.String(), "HIGH infrastructure misconfiguration") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFailsClosedWhenTrivyUnavailable(t *testing.T) {
	fixture := newScannerFixture(t)
	t.Setenv("SECURITY_TEST_SOURCE_FAILURE", "unavailable")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Config{Root: fixture.root, BaseRef: "origin/main", Timeout: 5 * time.Second, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("Run() accepted unavailable scanner")
	}
	if !strings.Contains(stderr.String(), "source scanner unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFailsClosedWhenGitleaksCurrentTreeOrHistoryIsUnavailable(t *testing.T) {
	for _, phase := range []string{"current", "history"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newScannerFixture(t)
			t.Setenv("SECURITY_TEST_GO_FAILURE", phase)
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), Config{Root: fixture.root, BaseRef: "origin/main", Timeout: 5 * time.Second, Stdout: &stdout, Stderr: &stderr})
			if err == nil {
				t.Fatal("Run() accepted unavailable Gitleaks scanner")
			}
			if !strings.Contains(stderr.String(), "gitleaks scanner unavailable") || !strings.Contains(stderr.String(), "[REDACTED]") {
				t.Fatalf("stderr = %q", stderr.String())
			}
			if strings.Contains(readFixtureLog(t, fixture.log), "docker|run") {
				t.Fatal("source scan ran after Gitleaks failure")
			}
		})
	}
}

func TestAllTrivyFindingsWaivedRequiresExactLowSeverityIdentity(t *testing.T) {
	contract := securitypolicy.Exceptions{Exceptions: []securitypolicy.Exception{{Scanner: "trivy", Rule: "AVD-001", Resource: "Dockerfile", Owner: "test", Rationale: "fixture", Created: "2026-08-01", Expires: "2026-08-31"}}}
	data := []byte(`{"Results":[{"Misconfigurations":[{"ID":"AVD-001","Target":"Dockerfile","Severity":"MEDIUM"}]}]}`)
	if waived, err := allTrivyFindingsWaived(data, contract); err != nil || !waived {
		t.Fatalf("allTrivyFindingsWaived() = %v, %v", waived, err)
	}
	high := []byte(`{"Results":[{"Misconfigurations":[{"ID":"AVD-001","Target":"Dockerfile","Severity":"HIGH"}]}]}`)
	if waived, err := allTrivyFindingsWaived(high, contract); err != nil || waived {
		t.Fatalf("high severity finding was waived: %v, %v", waived, err)
	}
	if _, err := allTrivyFindingsWaived([]byte("not-json"), contract); err == nil {
		t.Fatal("malformed Trivy output was accepted")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFixtureLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(fmt.Errorf("read scanner log: %w", err))
	}
	return string(data)
}
