package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

func TestCommandMarksTimeoutAsLifecycleFailure(t *testing.T) {
	r := &runner{timeout: 10 * time.Millisecond}
	result := r.command(t.TempDir(), "sh", "-c", "sleep 1")
	if !result.timedOut || !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("timeout result = %+v, want deadline lifecycle markers", result)
	}
}

func TestScanGoRejectsLifecycleDiagnosticsAndIncompleteStreams(t *testing.T) {
	contract := exceptionContract{Exceptions: []securitypolicy.Exception{{
		Scanner: "govulncheck", Rule: "GO-2026-test", Resource: "example/module",
	}}}
	complete := []byte(govulnConfigMessage + "\n" + govulnSBOMMessage + "\n" + `{"osv":{"id":"GO-2026-test"}}
{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module"}]}}`)
	tests := map[string]commandResult{
		"timeout with partial JSON": {stdout: []byte(`{"finding":`), status: 1, timedOut: true},
		"canceled":                  {stdout: complete, status: 1, canceled: true},
		"signaled":                  {stdout: complete, status: 1, signaled: true},
		"stderr diagnostic":         {stdout: complete, stderr: []byte("network provider unavailable\n"), status: 1},
		"unexpected command error":  {stdout: complete, status: 1, err: errors.New("provider transport failed")},
		"partial JSON":              {stdout: []byte(`{"finding":`), status: 1},
		"malformed status zero":     {stdout: []byte(`{"finding":`), status: 0},
	}
	for name, result := range tests {
		t.Run(name, func(t *testing.T) {
			var calls int
			r := &runner{
				stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
				goCommand: func(string, ...string) commandResult { calls++; return result },
			}
			if err := r.scanGo("/fixture/go.mod", &contract); err == nil {
				t.Fatal("unsafe govulncheck result was accepted")
			}
			if calls != 1 {
				t.Fatalf("govulncheck was invoked %d times, want 1", calls)
			}
		})
	}
}

const govulnConfigMessage = `{"config":{"protocol_version":"v1.0.0","scanner_name":"govulncheck","scanner_version":"v1.6.0","db":"https://vuln.go.dev","db_last_modified":"2026-09-04T00:00:00Z","scan_level":"symbol","scan_mode":"source"}}`
const govulnSBOMMessage = `{"SBOM":{"go_version":"go1.25.0","modules":[{"path":"example/root"}],"roots":["example/root"]}}`
const cleanGovulnStream = govulnConfigMessage + "\n" + govulnSBOMMessage

func cleanGovulnCommandResult() commandResult {
	return commandResult{stdout: []byte(cleanGovulnStream)}
}

const cleanGovulnVersionOutput = "Go: go1.25.0\nScanner: govulncheck@v1.6.0\nDB: https://vuln.go.dev\nDB updated: 2026-09-04T00:00:00Z\n\n"

func cleanGovulnCommandResultForArgs(args ...string) commandResult {
	if containsString(args, "install") {
		return commandResult{}
	}
	if containsString(args, "-version") {
		return commandResult{stdout: []byte(cleanGovulnVersionOutput)}
	}
	return cleanGovulnCommandResult()
}

func TestPrepareGovulncheckFailsClosedOnProvisioningContract(t *testing.T) {
	tests := []struct {
		name       string
		install    commandResult
		version    commandResult
		wantErr    string
		wantBinary bool
	}{
		{name: "clean", install: commandResult{}, version: commandResult{stdout: []byte(cleanGovulnVersionOutput)}, wantBinary: true},
		{name: "permitted download progress", install: commandResult{stderr: []byte("go: downloading golang.org/x/vuln v1.6.0\ngo: downloading golang.org/x/tools v0.49.0\n")}, version: commandResult{stdout: []byte(cleanGovulnVersionOutput)}, wantBinary: true},
		{name: "unknown stderr", install: commandResult{stderr: []byte("network provider unavailable\n")}, version: commandResult{stdout: []byte("Scanner: govulncheck@v1.6.0\n")}, wantErr: "unknown diagnostics"},
		{name: "wrong identity", install: commandResult{}, version: commandResult{stdout: []byte("Scanner: govulncheck@v1.5.0\n")}, wantErr: "identity"},
		{name: "missing identity", install: commandResult{}, version: commandResult{}, wantErr: "identity"},
		{name: "install stdout", install: commandResult{stdout: []byte("installed\n")}, version: commandResult{stdout: []byte(cleanGovulnVersionOutput)}, wantErr: "stdout"},
		{name: "version stderr", install: commandResult{}, version: commandResult{stdout: []byte(cleanGovulnVersionOutput), stderr: []byte("go: downloading example.com/module v1.0.0\n")}, wantErr: "stderr"},
		{name: "duplicate identity", install: commandResult{}, version: commandResult{stdout: []byte(cleanGovulnVersionOutput + "Scanner: govulncheck@v1.6.0\n")}, wantErr: "identity"},
		{name: "extraneous identity output", install: commandResult{}, version: commandResult{stdout: []byte("diagnostic\n" + cleanGovulnVersionOutput)}, wantErr: "identity"},
		{name: "nonzero status", install: commandResult{status: 1}, version: commandResult{stdout: []byte("Scanner: govulncheck@v1.6.0\n")}, wantErr: "status 1"},
		{name: "lifecycle error", install: commandResult{timedOut: true}, version: commandResult{stdout: []byte("Scanner: govulncheck@v1.6.0\n")}, wantErr: "lifecycle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var installCalls, versionCalls int
			r := &runner{
				root: t.TempDir(), timeout: time.Second, stdout: &stdout, stderr: &stderr,
				goInstallCommand: func(string, string, ...string) commandResult { installCalls++; return test.install },
				govulnCommand: func(_ string, _ string, args ...string) commandResult {
					if containsString(args, "-version") {
						versionCalls++
					}
					return test.version
				},
			}
			binary, cleanup, err := r.prepareGovulncheck()
			if test.wantErr == "" {
				if err != nil || !test.wantBinary || binary == "" {
					t.Fatalf("prepareGovulncheck() = %q, %v; want binary", binary, err)
				}
				cleanup()
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("prepareGovulncheck() error = %v, want %q", err, test.wantErr)
			}
			if installCalls != 1 {
				t.Fatalf("install calls = %d, want 1", installCalls)
			}
			wantVersionCalls := 1
			if test.wantErr == "status 1" || test.wantErr == "lifecycle" || test.name == "unknown stderr" || test.name == "install stdout" {
				wantVersionCalls = 0
			}
			if versionCalls != wantVersionCalls {
				t.Fatalf("version calls = %d, want %d", versionCalls, wantVersionCalls)
			}
		})
	}
}

func TestRunBootstrapsGovulncheckOnceForMultipleModules(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example/root\n\ngo 1.25\n")
	writeFixture(t, root, "nested/go.mod", "module example/nested\n\ngo 1.25\n")
	var installCalls, versionCalls, scanCalls int
	var installedGOBIN string
	var stdout, stderr bytes.Buffer
	r := &runner{
		root: root, timeout: time.Second, stdout: &stdout, stderr: &stderr,
		goInstallCommand: func(_ string, gobin string, args ...string) commandResult {
			installCalls++
			if gobin == "" || len(args) != 2 || args[0] != "install" || args[1] != "golang.org/x/vuln/cmd/govulncheck@v1.6.0" {
				t.Fatalf("bootstrap command = gobin %q args %v", gobin, args)
			}
			installedGOBIN = gobin
			return commandResult{}
		},
		govulnCommand: func(_ string, binary string, args ...string) commandResult {
			if !filepath.IsAbs(binary) || filepath.Dir(binary) != installedGOBIN {
				t.Fatalf("govulncheck binary = %q, want absolute path in %q", binary, installedGOBIN)
			}
			if containsString(args, "-version") {
				versionCalls++
				return commandResult{stdout: []byte(cleanGovulnVersionOutput)}
			}
			scanCalls++
			return cleanGovulnCommandResult()
		},
	}
	if err := r.run(); err == nil || !strings.Contains(err.Error(), "checked-in JavaScript") {
		t.Fatalf("run() error = %v, want evidence failure after Go scans", err)
	}
	if installCalls != 1 || versionCalls != 1 || scanCalls != 2 {
		t.Fatalf("bootstrap/scanner calls = install:%d version:%d scan:%d, want 1/1/2", installCalls, versionCalls, scanCalls)
	}
	if _, err := os.Stat(installedGOBIN); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private govulncheck directory was not removed: %v", err)
	}
}

func TestScanGoEvaluatesStatusZeroJSONFindings(t *testing.T) {
	vulnerable := []byte(cleanGovulnStream + "\n" +
		`{"osv":{"id":"GO-2026-test"}}` + "\n" +
		`{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module","package":"example/module/pkg","function":"Vulnerable"}]}}`)
	moduleOnly := []byte(cleanGovulnStream + "\n" +
		`{"osv":{"id":"GO-2026-test"}}` + "\n" +
		`{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module"}]}}`)
	packageOnly := []byte(cleanGovulnStream + "\n" +
		`{"osv":{"id":"GO-2026-test"}}` + "\n" +
		`{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module","package":"example/module/pkg"}]}}`)
	called := []byte(cleanGovulnStream + "\n" +
		`{"osv":{"id":"GO-2026-test"}}` + "\n" +
		`{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module","package":"example/module/pkg","function":"Vulnerable"},{"module":"example/root","package":"example/root","function":"main"}]}}`)
	tests := []struct {
		name    string
		result  commandResult
		wantErr string
	}{
		{name: "clean", result: commandResult{stdout: []byte(cleanGovulnStream)}},
		{name: "finding with documented json status", result: commandResult{stdout: vulnerable}, wantErr: "reported finding GO-2026-test in example/module"},
		{name: "multi-frame called finding", result: commandResult{stdout: called}, wantErr: "reported finding GO-2026-test in example/module"},
		{name: "module finding is informational", result: commandResult{stdout: moduleOnly}},
		{name: "package finding is informational", result: commandResult{stdout: packageOnly}},
		{name: "unknown nonzero status", result: commandResult{stdout: []byte(cleanGovulnStream), status: 2}, wantErr: "status 2"},
		{name: "progress without config", result: commandResult{stdout: []byte(`{"progress":{"message":"checking"}}`)}, wantErr: "config must be the first message"},
		{name: "empty envelope", result: commandResult{stdout: []byte(`{}`)}, wantErr: "exactly one field"},
		{name: "wrong scanner version", result: commandResult{stdout: []byte(strings.Replace(cleanGovulnStream, `"scanner_version":"v1.6.0"`, `"scanner_version":"v1.5.0"`, 1))}, wantErr: "config identity is unsupported"},
		{name: "missing scan level", result: commandResult{stdout: []byte(strings.Replace(cleanGovulnStream, `,"scan_level":"symbol"`, "", 1))}, wantErr: "config identity is unsupported"},
		{name: "wrong scan level", result: commandResult{stdout: []byte(strings.Replace(cleanGovulnStream, `"scan_level":"symbol"`, `"scan_level":"package"`, 1))}, wantErr: "config identity is unsupported"},
		{name: "config only", result: commandResult{stdout: []byte(govulnConfigMessage)}, wantErr: "source SBOM is missing"},
		{name: "empty source sbom", result: commandResult{stdout: []byte(govulnConfigMessage + "\n" + `{"SBOM":{}}`)}, wantErr: "source SBOM is incomplete"},
		{name: "function without package", result: commandResult{stdout: []byte(cleanGovulnStream + "\n" + `{"osv":{"id":"GO-2026-test"}}` + "\n" + `{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module","function":"Vulnerable"}]}}`)}, wantErr: "finding trace is malformed"},
		{name: "mixed package and call trace", result: commandResult{stdout: []byte(cleanGovulnStream + "\n" + `{"osv":{"id":"GO-2026-test"}}` + "\n" + `{"finding":{"osv":"GO-2026-test","trace":[{"module":"example/module","package":"example/module/pkg"},{"module":"example/root","package":"example/root","function":"main"}]}}`)}, wantErr: "finding trace is malformed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotArgs []string
			r := &runner{
				stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
				goCommand: func(_ string, args ...string) commandResult {
					gotArgs = append([]string(nil), args...)
					return test.result
				},
			}
			err := r.scanGo("/fixture/go.mod", nil)
			if test.wantErr == "" && err != nil {
				t.Fatalf("clean stream rejected: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("scanGo error = %v, want %q", err, test.wantErr)
			}
			if !containsString(gotArgs, "-json") {
				t.Fatalf("govulncheck args = %v, want JSON mode", gotArgs)
			}
		})
	}
}

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
	if err := r.runRefresh(); err != nil {
		t.Fatalf("run() error = %v\nstdout=%s\nstderr=%s\nlog=%s", err, stdout.String(), stderr.String(), mustRead(t, log))
	}
	logs := mustRead(t, log)
	for _, fragment := range []string{
		"bun|" + root + "|audit --audit-level critical --json",
		"bun|" + filepath.Join(root, "desktop") + "|audit --audit-level critical --json",
		"npm|" + filepath.Join(root, "typespec") + "|audit --package-lock-only --audit-level=critical --ignore-scripts --json",
	} {
		if !strings.Contains(logs, fragment) {
			t.Errorf("scanner log does not contain %q:\n%s", fragment, logs)
		}
	}

	setFakeScannerMode(t, "vulnerable")
	stdout.Reset()
	stderr.Reset()
	if err := r.runRefresh(); err == nil || !strings.Contains(err.Error(), "Critical JavaScript dependency finding") {
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
	if count, critical, err := bunFindingCounts([]byte(`{"pkg":[{"severity":"low"},{"severity":"critical"}]}`)); err != nil || count != 2 || critical != 1 {
		t.Fatalf("valid low and critical Bun results = (%d, %d, %v)", count, critical, err)
	}
	for _, data := range []string{
		`[]`,
		`{"pkg":{}}`,
		`{"pkg":null}`,
		`{"pkg":[{"severity":3}]}`,
		`{"pkg":[{"severity":null}]}`,
		`{"pkg":[{"severity":""}]}`,
		`{"pkg":[{"severity":" "}]}`,
		`{"pkg":[{"severity":"unknown"}]}`,
		`{"pkg":[null]}`,
	} {
		if _, _, err := bunFindingCounts([]byte(data)); err == nil {
			t.Errorf("bunFindingCounts(%s) accepted malformed JSON", data)
		}
	}
	for _, data := range []string{`{"finding":{}} trailing`, `null`, `[]`, `"diagnostic"`, `{}`, `{"progress":{}}`} {
		if stream, err := parseGovulnStream([]byte(data)); err == nil {
			t.Fatalf("malformed govulncheck stream was accepted: %s -> %#v", data, stream)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
	for _, path := range []string{"go.mod", "nested/go.mod", "bun.lock", "package.json", "desktop/bun.lock", "desktop/package.json", "typespec/package-lock.json", "typespec/package.json"} {
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
if [[ "$1" == "--version" ]]; then printf '1.0.0\n'; exit 0; fi
if [[ "$tool" == "go" ]]; then exit 0; fi
if [[ "$tool" == "npm" ]]; then printf '{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}},"vulnerabilities":{}}\n'; exit 0; fi
case "${SECURITY_TEST_MODE:-}" in
vulnerable)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"id":"GHSA-test-1","severity":"critical"}]}\n'; exit 1; fi ;;
bun-malformed)
  if [[ "$tool" == "bun" ]]; then printf 'malformed scanner output\n'; exit 1; fi ;;
bun-nonblocking)
  if [[ "$tool" == "bun" ]]; then printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'; exit 1; fi ;;
bun-outage)
  if [[ "$tool" == "bun" ]]; then printf '{}\n'; printf 'bun scanner unavailable\n' >&2; exit 70; fi ;;
bun-transport-recovery-then-exhausted)
  if [[ "$tool" == "bun" ]]; then
    if [[ "$PWD" == */desktop ]]; then
      printf 'malformed scanner output\n'
      printf 'Timeout: audit request failed\n' >&2
      exit 70
    fi
    if [[ ! -f "$SECURITY_TEST_LOG.attempt" ]]; then
      : > "$SECURITY_TEST_LOG.attempt"
      printf 'Timeout: audit request failed\n' >&2
      exit 70
    fi
    printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'
    exit 1
  fi ;;
bun-transport-exhausted)
  if [[ "$tool" == "bun" ]]; then
    printf 'malformed scanner output\n'
    printf 'TOKEN=sentinel_value\n' >&2
    printf 'ConnectionClosed: audit request failed\n' >&2
    exit 70
  fi ;;
bun-valid-transport-recovery)
  if [[ "$tool" == "bun" ]]; then
    if [[ ! -f "$SECURITY_TEST_LOG.valid-attempt" ]]; then
      : > "$SECURITY_TEST_LOG.valid-attempt"
      printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'
      printf 'Timeout: audit request failed\n' >&2
      exit 1
    fi
    printf '{"example-package":[{"id":123,"severity":"moderate"}]}\n'
    exit 1
  fi ;;
bun-unknown-malformed)
  if [[ "$tool" == "bun" ]]; then
    printf 'malformed scanner output\n'
    printf 'unrecognized scanner failure\n' >&2
    exit 1
  fi ;;
bun-critical-transport)
  if [[ "$tool" == "bun" ]]; then
    printf '{"example-package":[{"id":"GHSA-test-1","severity":"critical"}]}\n'
    printf 'Timeout: audit request failed\n' >&2
    exit 1
  fi ;;
esac
if [[ "$tool" == "bun" ]]; then printf '{}\n'; fi
if [[ "$tool" == "npm" ]]; then printf '{"vulnerabilities":{}}\n'; fi
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
