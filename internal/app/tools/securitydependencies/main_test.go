package main

import (
	"bytes"
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
	if got := strings.Count(mustRead(t, log), "bun|"); got != 1 {
		t.Fatalf("malformed Bun output was retried: %d invocations", got)
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

func TestCoveredBunRetriesTransientTransportOnce(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "bun-transient-once")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err != nil {
		t.Fatalf("transient Bun audit failure was not recovered: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := strings.Count(mustRead(t, log), "bun|"); got != 2 {
		t.Fatalf("transient Bun audit failure attempts = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "retrying once") {
		t.Fatalf("retry diagnostic is missing: %s", stderr.String())
	}

	setFakeScannerMode(t, "bun-finding-with-transport-error")
	stdout.Reset()
	stderr.Reset()
	before := strings.Count(mustRead(t, log), "bun|")
	if err := r.scanBun(filepath.Join(root, "bun.lock"), &contract); err == nil {
		t.Fatal("decoded critical finding was accepted")
	}
	if got := strings.Count(mustRead(t, log), "bun|"); got != before+1 {
		t.Fatalf("decoded critical finding was retried: %d new invocations", got-before)
	}
}

func TestNPMRetriesTransientTransportOnce(t *testing.T) {
	statusJSON := `{"statusCode":503,"message":"` + npmAuditBulk503Message + `","error":{"code":"E503"}}`
	stderrText := "npm warn audit 503 Service Unavailable\nnpm error audit endpoint returned an error"
	result := commandResult{
		status: 1,
		stdout: []byte(statusJSON),
		stderr: []byte(stderrText),
	}
	if !retryableNPMAuditTransport(result) {
		t.Fatal("observed npm audit transport failure was not classified as retryable")
	}
	for _, tc := range []struct {
		name   string
		stdout string
		stderr string
	}{
		{name: "missing status code", stdout: `{"message":"` + npmAuditBulk503Message + `"}`, stderr: stderrText},
		{name: "missing endpoint URL", stdout: `{"statusCode":503,"message":"503 Service Unavailable"}`, stderr: stderrText},
		{name: "missing npm diagnostic", stdout: statusJSON, stderr: "npm warn audit request failed"},
		{name: "nested old shape", stdout: `{"error":{"statusCode":503,"message":"` + npmAuditBulk503Message + `"}}`, stderr: stderrText},
		{name: "malformed JSON", stdout: `{"statusCode":503,"message":"` + npmAuditBulk503Message + `"`, stderr: stderrText},
		{name: "trailing garbage", stdout: statusJSON + " trailing", stderr: stderrText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if retryableNPMAuditTransport(commandResult{status: 1, stdout: []byte(tc.stdout), stderr: []byte(tc.stderr)}) {
				t.Fatal("incomplete npm audit transport failure was classified as retryable")
			}
		})
	}

	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "npm-transient-once")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
	if err := r.scanNPM(filepath.Join(root, "typespec", "package-lock.json"), &contract); err != nil {
		t.Fatalf("transient npm audit failure was not recovered: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if got := strings.Count(mustRead(t, log), "npm|"); got != 2 {
		t.Fatalf("transient npm audit failure attempts = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "retrying once") {
		t.Fatalf("retry diagnostic is missing: %s", stderr.String())
	}
}

func TestNPMDoesNotRetryValidFindings(t *testing.T) {
	contract := exceptionContract{}
	root, bin, log := scannerFixture(t)
	setFakeScannerEnv(t, bin, log, "npm-finding")
	var stdout, stderr bytes.Buffer
	r := &runner{root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr}
	if err := r.scanNPM(filepath.Join(root, "typespec", "package-lock.json"), &contract); err == nil {
		t.Fatal("critical npm finding was accepted")
	}
	if got := strings.Count(mustRead(t, log), "npm|"); got != 1 {
		t.Fatalf("valid npm finding was retried: %d invocations", got)
	}

	setFakeScannerMode(t, "npm-finding-with-transport-error")
	stdout.Reset()
	stderr.Reset()
	before := strings.Count(mustRead(t, log), "npm|")
	if err := r.scanNPM(filepath.Join(root, "typespec", "package-lock.json"), &contract); err == nil {
		t.Fatal("critical npm finding with transport diagnostics was accepted")
	}
	if got := strings.Count(mustRead(t, log), "npm|"); got != before+1 {
		t.Fatalf("valid npm finding with transport diagnostics was retried: %d new invocations", got-before)
	}

	setFakeScannerMode(t, "npm-malformed")
	stdout.Reset()
	stderr.Reset()
	before = strings.Count(mustRead(t, log), "npm|")
	if err := r.scanNPM(filepath.Join(root, "typespec", "package-lock.json"), &contract); err == nil {
		t.Fatal("malformed npm output was accepted")
	}
	if got := strings.Count(mustRead(t, log), "npm|"); got != before+1 {
		t.Fatalf("malformed npm output was retried: %d new invocations", got-before)
	}
}

func TestRetryableBunAuditTransportRejectsUnrelatedMalformedOutput(t *testing.T) {
	result := commandResult{status: 1, stderr: []byte("audit request failed while parsing timeout metadata")}
	if retryableBunAuditTransport(result) {
		t.Fatal("unrelated malformed output was classified as a transport failure")
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
if [[ "$tool" == "go" ]]; then exit 0; fi
if [[ "$tool" == "npm" ]]; then
  case "${SECURITY_TEST_MODE:-}" in
  npm-transient-once)
    attempts="$(grep -c '^npm|' "$SECURITY_TEST_LOG" || true)"
    if [[ "$attempts" == "1" ]]; then
      printf '{"statusCode":503,"message":"503 Service Unavailable - POST https://registry.npmjs.org/-/npm/v1/security/advisories/bulk - Service Unavailable","error":{"code":"E503"}}\n'
      printf 'npm warn audit 503 Service Unavailable\n' >&2
      printf 'npm error audit endpoint returned an error\n' >&2
      exit 1
    fi
    printf '{}\n'
    ;;
  npm-finding)
    printf '{"vulnerabilities":{"example-package":{"severity":"critical","via":[{"source":"GHSA-test-1","severity":"critical"}]}}}\n'
    exit 1
    ;;
  npm-finding-with-transport-error)
    printf '{"vulnerabilities":{"example-package":{"severity":"critical","via":[{"source":"GHSA-test-1","severity":"critical"}]}},"statusCode":503,"message":"503 Service Unavailable - POST https://registry.npmjs.org/-/npm/v1/security/advisories/bulk - Service Unavailable","error":{"code":"E503"}}\n'
    printf 'npm error audit endpoint returned an error\n' >&2
    exit 1
    ;;
  npm-malformed)
    printf 'npm audit returned malformed output\n'
    exit 1
    ;;
  esac
  exit 0
fi
case "${SECURITY_TEST_MODE:-}" in
vulnerable)
  if [[ "$tool" == "bun" ]]; then printf 'critical dependency finding\n' >&2; exit 1; fi ;;
bun-malformed)
  if [[ "$tool" == "bun" ]]; then printf 'malformed scanner output\n'; exit 1; fi ;;
bun-nonblocking)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'; exit 1; fi ;;
bun-outage)
  if [[ "$tool" == "bun" ]]; then printf '{}\n'; printf 'bun scanner unavailable\n' >&2; exit 70; fi ;;
bun-transient-once)
  if [[ "$tool" == "bun" ]]; then
    attempts="$(grep -c '^bun|' "$SECURITY_TEST_LOG" || true)"
    if [[ "$attempts" == "1" ]]; then printf 'error: ConnectionClosed: audit request failed (status 503)\n' >&2; exit 1; fi
    printf '{}\n'
  fi ;;
bun-finding-with-transport-error)
  if [[ "$tool" == "bun" ]]; then
    printf '{"example-package":[{"id":"GHSA-test-1","severity":"critical"}]}\n'
    printf 'Timeout: audit request failed\n' >&2
    exit 1
  fi ;;
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
