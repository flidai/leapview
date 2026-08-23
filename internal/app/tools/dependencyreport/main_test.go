package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeScan struct {
	commit string
	status string
	mode   string
}

func (f fakeScan) runner(_ context.Context, _ string, command string, args ...string) (CommandResult, error) {
	switch command {
	case "git":
		if len(args) > 0 && args[0] == "rev-parse" {
			return CommandResult{Stdout: []byte(f.commit + "\n")}, nil
		}
		return CommandResult{Stdout: []byte(f.status)}, nil
	case "node":
		return CommandResult{Stdout: []byte("v24.19.0\n")}, nil
	case "task":
		return CommandResult{Stdout: []byte("Task version: v3.45.4\n")}, nil
	case "bun":
		if len(args) == 1 {
			return CommandResult{Stdout: []byte("1.3.7\n")}, nil
		}
		if f.mode == "scanner" {
			return CommandResult{}, errors.New("bun executable missing")
		}
		if f.mode == "malformed" {
			return CommandResult{Stdout: []byte("not-json")}, nil
		}
		if f.mode == "vulnerable" {
			return CommandResult{ExitCode: 1, Stdout: []byte(`{"image-size":[{"id":1138808,"severity":"high","title":"bad parser","url":"https://example.test/advisory"}]}`)}, nil
		}
		return CommandResult{Stdout: []byte(`{}`)}, nil
	case "npm":
		if len(args) == 1 {
			return CommandResult{Stdout: []byte("11.17.0\n")}, nil
		}
		return CommandResult{Stdout: []byte(`{"metadata":{"dependencies":{"total":12}},"vulnerabilities":{}}`)}, nil
	case "go":
		return CommandResult{Stdout: []byte(`{"config":{"scanner_name":"govulncheck","scanner_version":"v1.5.0","db_last_modified":"2026-08-21T20:38:00Z","go_version":"go1.25.13"}}
{"SBOM":{"go_version":"go1.25.13","modules":[{"path":"github.com/flidai/leapview"}]}}`)}, nil
	default:
		return CommandResult{}, errors.New("unexpected command " + command)
	}
}

func newFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, file := range requiredFiles {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture-"+file+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"package.json", "desktop/package.json", "pkg/apigen/typespec/package.json"} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func fixtureConfig(root string) Config {
	return Config{Root: root, Output: filepath.Join(root, "report.json"), Waivers: defaultWaiver}
}

func fixtureDeps(scan fakeScan) Dependencies {
	return Dependencies{Run: scan.runner, Now: func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }}
}

func cleanReport(t *testing.T, scan fakeScan) (string, Report) {
	t.Helper()
	root := newFixture(t)
	report, err := collectReport(context.Background(), fixtureConfig(root), fixtureDeps(scan))
	if err != nil {
		t.Fatal(err)
	}
	return root, report
}

func TestCollectAndValidateCleanReport(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	if !report.Clearance.Cleared {
		t.Fatalf("clean report not cleared: %#v", report.Clearance)
	}
	if report.Graphs[0].Identity.RuntimeVersion != "1.3.7" || report.Graphs[3].Identity.ScannerVersion != "v1.5.0" {
		t.Fatalf("scanner identities were not normalized: %#v", report.Graphs)
	}
	if got := report.Graphs[3].Identity.Environment; len(got) != 1 || got[0] != "GOMEMLIMIT=4GiB" {
		t.Fatalf("Go scanner environment = %#v", got)
	}
	if got := strings.Join(report.Graphs[2].Identity.Command, " "); got != "npm --prefix pkg/apigen/typespec audit --json" {
		t.Fatalf("APIGen audit command = %q", got)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)})); err != nil {
		t.Fatalf("clean report rejected: %v", err)
	}
	if len(data) == 0 || report.Digests["bun.lock"] == "" {
		t.Fatal("report omitted content digests")
	}
}

func TestCheckRejectsReportOnlyWaiver(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	report.Waivers = []Waiver{{
		Advisory:            "1138808",
		Dependency:          "image-size",
		Owner:               "security",
		Reachability:        "not reachable",
		CompensatingControl: "pinned transitive version",
		Created:             time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Expiry:              time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}}
	report.Clearance = Clearance{Cleared: true}
	err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "waiver") {
		t.Fatalf("report-only waiver accepted: %v", err)
	}
}

func TestRejectsWaiverSourceOutsideRepository(t *testing.T) {
	root := newFixture(t)
	outside := filepath.Join(t.TempDir(), "dependency-waivers.json")
	if err := os.WriteFile(outside, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := collectReport(context.Background(), Config{Root: root, Output: filepath.Join(root, "report.json"), Waivers: outside}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "waiver source must be repository-approved") {
		t.Fatalf("outside waiver source accepted: %v", err)
	}
}

func TestRejectsSymlinkedWaiverSource(t *testing.T) {
	root := newFixture(t)
	outside := filepath.Join(t.TempDir(), "dependency-waivers.json")
	if err := os.WriteFile(outside, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waiverPath := filepath.Join(root, defaultWaiver)
	if err := os.MkdirAll(filepath.Dir(waiverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, waiverPath); err != nil {
		t.Fatal(err)
	}
	_, err := collectReport(context.Background(), fixtureConfig(root), fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked waiver source accepted: %v", err)
	}
}

func TestVulnerableGraphRequiresWaiver(t *testing.T) {
	root := newFixture(t)
	err := generate(context.Background(), Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), mode: "vulnerable"}))
	if err == nil || !strings.Contains(err.Error(), "clearance failed") {
		t.Fatalf("vulnerable report unexpectedly succeeded: %v", err)
	}
}

func TestScannerFailurePersistsUnclearedReport(t *testing.T) {
	root := newFixture(t)
	output := filepath.Join(root, "report.json")
	if err := os.WriteFile(output, []byte(`{"clearance":{"cleared":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(context.Background(), Config{Root: root, Output: output}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), mode: "scanner"}))
	if err == nil || !strings.Contains(err.Error(), "scanner failed") {
		t.Fatalf("scanner failure = %v, want fail-closed error", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatalf("uncleared report was not persisted: %v", readErr)
	}
	report, decodeErr := decodeReport(data)
	if decodeErr != nil {
		t.Fatalf("persisted scanner report malformed: %v", decodeErr)
	}
	if report.Clearance.Cleared {
		t.Fatalf("scanner report unexpectedly cleared: %#v", report.Clearance)
	}
	if len(report.Graphs) == 0 || report.Graphs[0].Result.Status != "scanner_error" {
		t.Fatalf("scanner error was not retained in report: %#v", report.Graphs)
	}
}

func TestVulnerablePersistsUnclearedReport(t *testing.T) {
	root := newFixture(t)
	output := filepath.Join(root, "report.json")
	err := generate(context.Background(), Config{Root: root, Output: output}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), mode: "vulnerable"}))
	if err == nil || !strings.Contains(err.Error(), "clearance failed") {
		t.Fatalf("vulnerable report unexpectedly succeeded: %v", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatalf("uncleared vulnerable report was not persisted: %v", readErr)
	}
	report, decodeErr := decodeReport(data)
	if decodeErr != nil {
		t.Fatalf("persisted vulnerable report malformed: %v", decodeErr)
	}
	if report.Clearance.Cleared || len(report.Graphs[0].Result.Findings) != 1 {
		t.Fatalf("vulnerable report did not retain uncleared finding: %#v", report)
	}
}

func TestMalformedScannerOutputFailsClosed(t *testing.T) {
	root := newFixture(t)
	err := generate(context.Background(), Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), mode: "malformed"}))
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed scanner output = %v", err)
	}
}

func TestDirtyCheckoutRejectedByDefault(t *testing.T) {
	root := newFixture(t)
	deps := fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), status: " M go.sum\n"})
	if _, err := collectReport(context.Background(), fixtureConfig(root), deps); err == nil || !strings.Contains(err.Error(), "clean checkout") {
		t.Fatalf("dirty checkout error = %v", err)
	}
	if _, err := collectReport(context.Background(), Config{Root: root, Output: filepath.Join(root, "report.json"), AllowDirty: true}, deps); err == nil || !strings.Contains(err.Error(), "clearance failed") {
		t.Fatalf("allow-dirty report unexpectedly succeeded: %v", err)
	}
}

func TestEditedLockDigestRejected(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	if err := os.WriteFile(filepath.Join(root, "bun.lock"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("edited lock accepted: %v", err)
	}
}

func TestCommitMismatchRejected(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("b", 40)}))
	if err == nil || !strings.Contains(err.Error(), "source commit mismatch") {
		t.Fatalf("commit mismatch accepted: %v", err)
	}
}

func TestMissingGraphRejected(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	report.Graphs = report.Graphs[:len(report.Graphs)-1]
	err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "missing dependency scan graphs") {
		t.Fatalf("missing graph accepted: %v", err)
	}
}

func TestTamperedScanResultRejected(t *testing.T) {
	root, report := cleanReport(t, fakeScan{commit: strings.Repeat("a", 40)})
	report.Graphs[0].Result.Status = "vulnerable"
	report.Graphs[0].Result.SeverityCounts = map[string]int{}
	report.Counts = summarize(report.Graphs)
	report.Clearance = Clearance{Cleared: true}
	err := validateReport(context.Background(), report, Config{Root: root, Output: filepath.Join(root, "report.json")}, fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "changed since report generation") {
		t.Fatalf("tampered scan result accepted: %v", err)
	}
}

func TestExpiredWaiverRejected(t *testing.T) {
	root := newFixture(t)
	waiverPath := filepath.Join(root, defaultWaiver)
	if err := os.MkdirAll(filepath.Dir(waiverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	waiver := []Waiver{{Advisory: "GHSA-test", Dependency: "example", Owner: "security", Reachability: "not reachable", CompensatingControl: "pin", Created: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Expiry: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	data, _ := json.Marshal(waiver)
	if err := os.WriteFile(waiverPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := collectReport(context.Background(), fixtureConfig(root), fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired waiver accepted: %v", err)
	}
}

func TestUnusedWaiverRejected(t *testing.T) {
	root := newFixture(t)
	waiverPath := filepath.Join(root, defaultWaiver)
	if err := os.MkdirAll(filepath.Dir(waiverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	waiver := []Waiver{{Advisory: "GHSA-unused", Dependency: "example", Owner: "security", Reachability: "not reachable", CompensatingControl: "pin", Created: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Expiry: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
	data, _ := json.Marshal(waiver)
	if err := os.WriteFile(waiverPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := collectReport(context.Background(), fixtureConfig(root), fixtureDeps(fakeScan{commit: strings.Repeat("a", 40)}))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unused waiver accepted: %v", err)
	}
}

func TestValidWaiverClearsObservedFinding(t *testing.T) {
	root := newFixture(t)
	waiverPath := filepath.Join(root, defaultWaiver)
	if err := os.MkdirAll(filepath.Dir(waiverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	waiver := []Waiver{{Advisory: "1138808", Dependency: "image-size", Owner: "security", Reachability: "not reachable", CompensatingControl: "pinned transitive version", Created: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Expiry: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}}
	data, _ := json.Marshal(waiver)
	if err := os.WriteFile(waiverPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := collectReport(context.Background(), fixtureConfig(root), fixtureDeps(fakeScan{commit: strings.Repeat("a", 40), mode: "vulnerable"}))
	if err != nil || !report.Clearance.Cleared {
		t.Fatalf("valid waiver did not clear report: %#v, %v", report.Clearance, err)
	}
}

func TestDecodeMalformedReport(t *testing.T) {
	if _, err := decodeReport([]byte(`{"schema_version":`)); err == nil {
		t.Fatal("malformed report decoded")
	}
}

func TestParseGoPrettyJSONStream(t *testing.T) {
	data := []byte(`{
  "config": {
    "scanner_name": "govulncheck",
    "scanner_version": "v1.5.0",
    "db_last_modified": "2026-08-21T20:38:00Z",
    "go_version": "go1.25.13"
  }
}
{
  "SBOM": {
    "go_version": "go1.25.13",
    "modules": [{"path": "example.test/one"}, {"path": "example.test/two"}]
  }
}
{
  "finding": {
    "osv": "GO-2026-1234",
    "trace": [{"module": "example.test/one", "version": "v1.2.3", "package": "example.test/one/pkg", "function": "Vulnerable"}]
  }
}`)
	findings, notices, packages, identity, err := parseGo(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Advisory != "GO-2026-1234" || findings[0].Reachability != "called" || len(notices) != 0 || packages != 2 {
		t.Fatalf("parsed govulncheck stream = %#v, notices %#v, packages %d", findings, notices, packages)
	}
	if identity.DatabaseLastModified == "" || identity.Command[0] != "go" {
		t.Fatalf("missing govulncheck identity: %#v", identity)
	}
}

func TestParseGoRetainsNonReachableAdvisoriesAsNotices(t *testing.T) {
	data := []byte(`{"config":{"scanner_name":"govulncheck","scanner_version":"v1.5.0","go_version":"go1.25.13"}}
{"SBOM":{"go_version":"go1.25.13","modules":[{"path":"example.test/one"}]}}
{"finding":{"osv":"GO-2026-1234","trace":[{"module":"example.test/one","version":"v1.2.3"}]}}
{"finding":{"osv":"GO-2026-1234","trace":[{"module":"example.test/one","version":"v1.2.3","package":"example.test/one/pkg"}]}}`)
	findings, notices, _, _, err := parseGo(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 || len(notices) != 1 || notices[0].Reachability != "imported" {
		t.Fatalf("findings = %#v, notices = %#v", findings, notices)
	}
}

func TestParseNPMRequiresAuditEnvelope(t *testing.T) {
	if _, _, _, err := parseNPM([]byte(`{"vulnerabilities":{}}`)); err == nil {
		t.Fatal("npm output without metadata decoded")
	}
}
