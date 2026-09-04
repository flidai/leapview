package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

func TestDiscoverMaintainedFilesAndExclusions(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"go.mod", "nested/go.mod", "bun.lock", "desktop/bun.lock", "typespec/package-lock.json",
		".data/ignored/go.mod", ".tmp/ignored/bun.lock", "node_modules/ignored/package-lock.json", "testdata/ignored/go.mod",
	} {
		writeFixture(t, root, path, "{}\n")
	}
	modules, buns, npms, err := discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := relPaths(root, modules), []string{"go.mod", "nested/go.mod"}; !equalStrings(got, want) {
		t.Fatalf("modules = %v, want %v", got, want)
	}
	if got, want := relPaths(root, buns), []string{"bun.lock", "desktop/bun.lock"}; !equalStrings(got, want) {
		t.Fatalf("bun locks = %v, want %v", got, want)
	}
	if got, want := relPaths(root, npms), []string{"typespec/package-lock.json"}; !equalStrings(got, want) {
		t.Fatalf("npm locks = %v, want %v", got, want)
	}
}

func TestRunnerUsesPinnedScannersAndRejectsFindings(t *testing.T) {
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
	if err := r.run(); err != nil {
		t.Fatalf("run() error = %v\nstdout=%s\nstderr=%s\nlog=%s", err, stdout.String(), stderr.String(), mustRead(t, log))
	}
	logs := mustRead(t, log)
	for _, fragment := range []string{
		"go|" + root + "|run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...",
		"go|" + filepath.Join(root, "nested") + "|run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...",
		"bun|" + root + "|audit --audit-level critical",
		"bun|" + filepath.Join(root, "desktop") + "|audit --audit-level critical",
		"npm|" + filepath.Join(root, "typespec") + "|audit --package-lock-only --audit-level=critical --ignore-scripts",
	} {
		if !strings.Contains(logs, fragment) {
			t.Errorf("scanner log does not contain %q:\n%s", fragment, logs)
		}
	}

	setFakeScannerMode(t, "vulnerable")
	stdout.Reset()
	stderr.Reset()
	if err := r.run(); err == nil || !strings.Contains(stderr.String(), "critical dependency finding") {
		t.Fatalf("vulnerable scanner was not rejected: err=%v stderr=%q", err, stderr.String())
	}
}

func TestCoveredBunFailsClosedAndAcceptsOnlyNonblockingStatusOne(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-malformed")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil || !strings.Contains(stderr.String(), "not valid JSON") {
		t.Fatalf("malformed Bun output was not rejected: err=%v stderr=%q stdout=%q", err, stderr.String(), stdout.String())
	}

	setFakeScannerMode(t, "bun-nonblocking")
	stdout.Reset()
	stderr.Reset()
	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err != nil {
		t.Fatalf("nonblocking Bun status 1 was rejected: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "no Critical findings (1 below threshold)") {
		t.Fatalf("missing nonblocking diagnostic: %s", stdout.String())
	}

	setFakeScannerMode(t, "bun-outage")
	stdout.Reset()
	stderr.Reset()
	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil || !strings.Contains(stderr.String(), "scanner failed without decoded blocking findings") {
		t.Fatalf("Bun outage was not rejected: err=%v stderr=%q stdout=%q", err, stderr.String(), stdout.String())
	}
}

func TestBunAuditRetriesOnlyBlankTransportFailures(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		wantErr         bool
		wantInvocations int
		wantRetryNotice bool
	}{
		{name: "transient transport failure then success", mode: "bun-transport-transient", wantInvocations: 2, wantRetryNotice: true},
		{name: "HTTP 503 transport failure then success", mode: "bun-transport-http503", wantInvocations: 2, wantRetryNotice: true},
		{name: "permanent transport failure", mode: "bun-transport-permanent", wantErr: true, wantInvocations: 2, wantRetryNotice: true},
		{name: "critical JSON with transport stderr", mode: "bun-transport-critical", wantErr: true, wantInvocations: 1},
		{name: "noncritical JSON with transport stderr", mode: "bun-transport-noncritical", wantInvocations: 1},
		{name: "partial JSON with transport stderr", mode: "bun-transport-partial", wantErr: true, wantInvocations: 1},
		{name: "malformed JSON with transport stderr", mode: "bun-transport-malformed", wantErr: true, wantInvocations: 1},
		{name: "unrelated blank-output error", mode: "bun-transport-unrelated", wantErr: true, wantInvocations: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, bin, log := scannerFixture(t)
			setFakeScannerEnv(t, bin, log, test.mode)
			if test.mode == "bun-transport-transient" || test.mode == "bun-transport-http503" {
				t.Setenv("SECURITY_TEST_STATE", filepath.Join(root, "transport.state"))
			}
			var stdout, stderr bytes.Buffer
			r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
			err := r.scanBun(filepath.Join(root, "bun.lock"), &exceptionContract{})
			if test.wantErr && err == nil {
				t.Fatalf("transport result was accepted: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if !test.wantErr && err != nil {
				t.Fatalf("transport result failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			if got := strings.Count(mustRead(t, log), "bun|"); got != test.wantInvocations {
				t.Fatalf("Bun was invoked %d times, want %d; log=%s", got, test.wantInvocations, mustRead(t, log))
			}
			notice := fmt.Sprintf("bun audit %s: transient transport failure; retrying once", root)
			if got := strings.Count(stdout.String(), notice); test.wantRetryNotice && got != 1 {
				t.Fatalf("retry diagnostic count = %d, want 1; stdout=%q", got, stdout.String())
			} else if !test.wantRetryNotice && got != 0 {
				t.Fatalf("unexpected retry diagnostic: stdout=%q", stdout.String())
			}
		})
	}
}

func TestExceptionMatchingIsExactAndCriticalFindingsAreNeverWaived(t *testing.T) {
	contract := securitypolicy.Exceptions{Exceptions: []securitypolicy.Exception{{Scanner: "bun-audit", Rule: "GHSA-test-1", Resource: "pkg"}}}
	if !matches(contract, findingIdentity{Scanner: "bun-audit", Rule: "GHSA-test-1", Resource: "pkg", Severity: "moderate"}) {
		t.Fatal("exact below-threshold finding did not match")
	}
	for _, finding := range []findingIdentity{
		{Scanner: "bun-audit", Rule: "GHSA-test-1", Resource: "other", Severity: "moderate"},
		{Scanner: "bun-audit", Rule: "GHSA-test-1", Resource: "pkg", Severity: "critical"},
		{Scanner: "bun-audit", Rule: "GHSA-test-1", Resource: "pkg"},
		{Scanner: "provenance", Rule: "GHSA-test-1", Resource: "pkg", Severity: "moderate"},
	} {
		if matches(contract, finding) {
			t.Fatalf("unsafe finding matched exception: %+v", finding)
		}
	}
}

func TestTypedJSONParsersRejectMalformedFindings(t *testing.T) {
	if count, critical, err := bunFindingCounts([]byte(`{"pkg":[{"severity":"moderate"}]}`)); err != nil || count != 1 || critical != 0 {
		t.Fatalf("valid Bun result = (%d, %d, %v)", count, critical, err)
	}
	for _, data := range []string{`[]`, `{"pkg":{}}`, `{"pkg":null}`, `{"pkg":[{"severity":3}]}`, `{"pkg":[null]}`} {
		if _, _, err := bunFindingCounts([]byte(data)); err == nil {
			t.Errorf("bunFindingCounts(%s) accepted malformed JSON", data)
		}
	}
	if got, ok := decodeGovulnFindings([]byte(`{"finding":{}} trailing`)); ok || got != nil {
		t.Fatalf("malformed govulncheck stream was accepted: %#v, %v", got, ok)
	}
}

func TestDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	output := commandOutput([]byte("TOKEN=sentinel_value password: \"secret-value\""))
	if strings.Contains(string(output), "sentinel_value") || strings.Contains(string(output), "secret-value") {
		t.Fatalf("sensitive diagnostics were not redacted: %q", output)
	}
	if !strings.Contains(string(output), "[REDACTED]") {
		t.Fatalf("redaction marker is missing: %q", output)
	}
	large := commandOutput(bytes.Repeat([]byte("x"), maxDiagnosticBytes+1))
	if len(large) <= maxDiagnosticBytes || !strings.Contains(string(large), "scanner output truncated") {
		t.Fatalf("large diagnostics were not bounded: len=%d", len(large))
	}
}

func scannerFixture(t *testing.T) (root, bin, log string) {
	t.Helper()
	root = t.TempDir()
	for _, path := range []string{"go.mod", "nested/go.mod", "bun.lock", "desktop/bun.lock", "typespec/package-lock.json"} {
		writeFixture(t, root, path, "{}\n")
	}
	bin = filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(root, "scanner.log")
	shim := `#!/usr/bin/env bash
set -eu
tool="$(basename "$0")"
printf '%s|%s|%s\n' "$tool" "$PWD" "$*" >> "$SECURITY_TEST_LOG"
if [[ "$tool" == "go" || "$tool" == "npm" ]]; then exit 0; fi
case "${SECURITY_TEST_MODE:-}" in
vulnerable)
  if [[ "$tool" == "bun" ]]; then printf 'critical dependency finding\n' >&2; exit 1; fi ;;
bun-malformed)
  if [[ "$tool" == "bun" ]]; then printf 'malformed scanner output\n'; exit 1; fi ;;
bun-nonblocking)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'; exit 1; fi ;;
bun-outage)
  if [[ "$tool" == "bun" ]]; then printf '{}\n'; printf 'bun scanner unavailable\n' >&2; exit 70; fi ;;
bun-transport-transient)
  if [[ "$tool" == "bun" ]]; then
    if [[ ! -e "$SECURITY_TEST_STATE" ]]; then
      : > "$SECURITY_TEST_STATE"
      printf '\033[33mBun 1.3.14\033[0m\nConnectionClosed: audit request failed\n' >&2
      printf ' \t\n'
      exit 1
    fi
    printf '{}\n'
    exit 0
  fi ;;
bun-transport-http503)
  if [[ "$tool" == "bun" ]]; then
    if [[ ! -e "$SECURITY_TEST_STATE" ]]; then
      : > "$SECURITY_TEST_STATE"
      printf '\033[31merror:\033[0m audit request failed (status 503)\n' >&2
      printf '\n'
      exit 1
    fi
    printf '{}\n'
    exit 0
  fi ;;
bun-transport-permanent)
  if [[ "$tool" == "bun" ]]; then printf ' \t\n'; printf 'banner: Timeout: audit request failed\n' >&2; exit 70; fi ;;
bun-transport-critical)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"severity":"critical"}]}\n'; printf 'ConnectionClosed: audit request failed\n' >&2; exit 1; fi ;;
bun-transport-noncritical)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"severity":"moderate"}]}\n'; printf 'Timeout: audit request failed\n' >&2; exit 1; fi ;;
bun-transport-partial)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":\n'; printf 'Timeout: audit request failed\n' >&2; exit 1; fi ;;
bun-transport-malformed)
  if [[ "$tool" == "bun" ]]; then printf 'not JSON\n'; printf 'ConnectionClosed: audit request failed\n' >&2; exit 1; fi ;;
bun-transport-unrelated)
  if [[ "$tool" == "bun" ]]; then printf ' \t\n'; printf 'bun scanner unavailable\n' >&2; exit 70; fi ;;
esac
exit 0
`
	for _, tool := range []string{"go", "bun", "npm"} {
		writeExecutable(t, filepath.Join(bin, tool), shim)
	}
	return
}

func setFakeScannerEnv(t *testing.T, bin, log, mode string) {
	t.Helper()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SECURITY_TEST_LOG", log)
	t.Setenv("SECURITY_TEST_MODE", mode)
}

func setFakeScannerMode(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("SECURITY_TEST_MODE", mode)
}

func writeFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func relPaths(root string, paths []string) []string {
	result := make([]string, len(paths))
	for index, path := range paths {
		result[index], _ = filepath.Rel(root, path)
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
