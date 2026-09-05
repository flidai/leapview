package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

var evidenceTestNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

const cleanNPMEvidenceJSON = `{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}},"vulnerabilities":{}}`

func TestDefaultRunEvaluatesCompleteEvidenceWithoutLaunchingJavaScript(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	evidence := fixture.evidence(nil)
	fixture.writeEvidence(t, evidence)

	var goCalls, bunCalls, npmCalls int
	var stdout, stderr bytes.Buffer
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &stdout, stderr: &stderr, now: func() time.Time { return evidenceTestNow },
		goCommand: func(_ string, args ...string) commandResult {
			goCalls++
			return cleanGovulnCommandResultForArgs(args...)
		},
		bunCommand: func(string, ...string) commandResult {
			bunCalls++
			return commandResult{err: errors.New("Bun must not run in default mode")}
		},
		npmCommand: func(string, ...string) commandResult {
			npmCalls++
			return commandResult{err: errors.New("npm must not run in default mode")}
		},
	}
	if err := r.run(); err != nil {
		t.Fatalf("default run rejected clean evidence: %v", err)
	}
	if goCalls != 3 || bunCalls != 0 || npmCalls != 0 {
		t.Fatalf("scanner calls = go:%d bun:%d npm:%d, want one Go install, version, and scan", goCalls, bunCalls, npmCalls)
	}
}

func TestCheckedInEvidenceMustBeRegularAndTracked(t *testing.T) {
	t.Run("symlink is rejected", func(t *testing.T) {
		fixture := newJavaScriptEvidenceFixture(t)
		fixture.writeEvidence(t, fixture.evidence(nil))
		evidencePath := filepath.Join(fixture.root, javascriptEvidenceRelativePath)
		outside := filepath.Join(t.TempDir(), "evidence.json")
		if err := os.Rename(evidencePath, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, evidencePath); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		r := &runner{root: fixture.root, now: func() time.Time { return evidenceTestNow }}
		if err := r.evaluateCheckedInJavaScriptEvidence(fixture.buns, fixture.npms, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlink evidence was accepted: %v", err)
		}
	})

	t.Run("untracked file is rejected in checkout", func(t *testing.T) {
		fixture := newJavaScriptEvidenceFixture(t)
		fixture.writeEvidence(t, fixture.evidence(nil))
		command := exec.Command("git", "init", "-q", fixture.root)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, output)
		}
		r := &runner{
			root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
			goCommand: func(_ string, args ...string) commandResult { return cleanGovulnCommandResultForArgs(args...) },
		}
		if err := r.run(); err == nil || !strings.Contains(err.Error(), "must be tracked") {
			t.Fatalf("untracked evidence was accepted: %v", err)
		}
	})
}

func TestCheckedInEvidenceRejectsCriticalAndAcceptsBelowThreshold(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	critical := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "critical"})
	fixture.writeEvidence(t, critical)
	var stdout, stderr bytes.Buffer
	r := &runner{root: fixture.root, timeout: time.Second, stdout: &stdout, stderr: &stderr, now: func() time.Time { return evidenceTestNow }, goCommand: func(_ string, args ...string) commandResult { return cleanGovulnCommandResultForArgs(args...) }}
	if err := r.run(); err == nil || !strings.Contains(err.Error(), "GHSA-evidence-1") || !strings.Contains(err.Error(), "example-package") {
		t.Fatalf("critical evidence was not rejected with identity: %v", err)
	}

	clean := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "low"})
	fixture.writeEvidence(t, clean)
	if err := r.run(); err != nil {
		t.Fatalf("below-threshold evidence was rejected: %v", err)
	}
}

func TestEvidenceUsesExactSecurityPolicyExceptionContract(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	evidence := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "moderate"})
	graph := evidence.Graphs[0]
	finding := graph.Findings[0]
	contract := exceptionContract{Exceptions: []securitypolicy.Exception{{Scanner: graph.Scanner, Rule: finding.Advisory, Resource: finding.Dependency}}}
	if !matches(contract, findingIdentity{Scanner: graph.Scanner, Rule: finding.Advisory, Resource: finding.Dependency, Severity: finding.Severity}) {
		t.Fatal("exact below-threshold exception did not match")
	}
	if matches(contract, findingIdentity{Scanner: graph.Scanner, Rule: finding.Advisory, Resource: "other", Severity: finding.Severity}) {
		t.Fatal("resource mismatch matched exception")
	}
	for _, severity := range []string{"high", "critical"} {
		if matches(contract, findingIdentity{Scanner: graph.Scanner, Rule: finding.Advisory, Resource: finding.Dependency, Severity: severity}) {
			t.Fatalf("protected %s finding matched exception", severity)
		}
	}
}

func TestEvidenceStrictJSONAndSeverityValidation(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	evidence := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "low"})
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string][]byte{
		"unknown field":    []byte(strings.Replace(string(data), `"schema":`, `"unexpected":`, 1)),
		"trailing value":   append(append([]byte(nil), data...), []byte(` {}`)...),
		"null severity":    []byte(strings.Replace(string(data), `"severity":"low"`, `"severity":null`, 1)),
		"missing severity": []byte(strings.Replace(string(data), `,"severity":"low"`, ``, 1)),
		"empty severity":   []byte(strings.Replace(string(data), `"severity":"low"`, `"severity":""`, 1)),
		"invalid severity": []byte(strings.Replace(string(data), `"severity":"low"`, `"severity":"urgent"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			decoded, decodeErr := decodeJavaScriptEvidence(input)
			if decodeErr == nil {
				if validateErr := validateJavaScriptEvidence(decoded, fixture.root, fixture.buns, fixture.npms, evidenceTestNow); validateErr == nil {
					t.Fatalf("accepted malformed evidence")
				}
			}
		})
	}
}

func TestEvidenceAndScannerJSONRejectDuplicateKeys(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	evidence := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "low"})
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	schema := `"schema":"` + javascriptEvidenceSchema + `"`
	for name, input := range map[string][]byte{
		"evidence top-level": []byte(strings.Replace(string(data), schema, schema+","+schema, 1)),
		"evidence nested":    []byte(strings.Replace(string(data), `"severity":"low"`, `"severity":"low","severity":"critical"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeJavaScriptEvidence(input); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
				t.Fatalf("duplicate-key evidence was accepted: %v", err)
			}
		})
	}

	for name, parse := range map[string]func() error{
		"Bun dependency": func() error {
			_, err := parseBunEvidenceFindings([]byte(`{"pkg":[{"id":1,"severity":"low"}],"pkg":[{"id":2,"severity":"critical"}]}`))
			return err
		},
		"Bun severity": func() error {
			_, err := parseBunEvidenceFindings([]byte(`{"pkg":[{"id":1,"severity":"low","severity":"critical"}]}`))
			return err
		},
		"npm vulnerabilities": func() error {
			_, err := parseNPMEvidenceFindings([]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":0,"total":0}},"vulnerabilities":{},"vulnerabilities":{"pkg":{"severity":"critical","via":[]}}}`))
			return err
		},
		"npm dependency": func() error {
			_, err := parseNPMEvidenceFindings([]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":1,"moderate":0,"high":0,"critical":0,"total":1}},"vulnerabilities":{"pkg":{"severity":"low","via":[]},"pkg":{"severity":"critical","via":[]}}}`))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := parse(); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
				t.Fatalf("duplicate-key scanner output was accepted: %v", err)
			}
		})
	}
}

func TestEvidenceRejectsGraphAndFindingSetChanges(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	base := fixture.evidence(&javascriptEvidenceFinding{Advisory: "GHSA-evidence-1", Dependency: "example-package", Severity: "low"})
	tests := map[string]func(*javascriptEvidence){
		"missing graph": func(e *javascriptEvidence) { e.Graphs = e.Graphs[:len(e.Graphs)-1] },
		"extra graph": func(e *javascriptEvidence) {
			e.Graphs[0].ID = "bun:extra.lock"
		},
		"duplicate graph": func(e *javascriptEvidence) { e.Graphs[1].ID = e.Graphs[0].ID },
		"duplicate finding": func(e *javascriptEvidence) {
			e.Graphs[0].Findings = append(e.Graphs[0].Findings, e.Graphs[0].Findings[0])
		},
		"duplicate finding with changed severity": func(e *javascriptEvidence) {
			duplicate := e.Graphs[0].Findings[0]
			duplicate.Severity = "moderate"
			e.Graphs[0].Findings = append(e.Graphs[0].Findings, duplicate)
		},
		"unsorted finding": func(e *javascriptEvidence) {
			e.Graphs[0].Findings = []javascriptEvidenceFinding{
				{Advisory: "GHSA-z", Dependency: "z-package", Severity: "low"},
				{Advisory: "GHSA-a", Dependency: "a-package", Severity: "low"},
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := base
			evidence.Graphs = append([]javascriptEvidenceGraph(nil), base.Graphs...)
			for i := range evidence.Graphs {
				evidence.Graphs[i].Findings = append([]javascriptEvidenceFinding(nil), base.Graphs[i].Findings...)
			}
			mutate(&evidence)
			if err := validateJavaScriptEvidence(evidence, fixture.root, fixture.buns, fixture.npms, evidenceTestNow); err == nil {
				t.Fatalf("accepted changed graph/finding set")
			}
		})
	}
}

func TestEvidenceRejectsGraphIdentityTampering(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	base := fixture.evidence(nil)
	tests := map[string]func(*javascriptEvidenceGraph){
		"manager":         func(graph *javascriptEvidenceGraph) { graph.Manager = "npm" },
		"runtime version": func(graph *javascriptEvidenceGraph) { graph.RuntimeVersion = "" },
		"scanner":         func(graph *javascriptEvidenceGraph) { graph.Scanner = "other" },
		"scanner version": func(graph *javascriptEvidenceGraph) { graph.ScannerVersion = "" },
		"command":         func(graph *javascriptEvidenceGraph) { graph.Command = []string{"bun", "audit"} },
		"manifest":        func(graph *javascriptEvidenceGraph) { graph.Manifest = "other/package.json" },
		"lockfile":        func(graph *javascriptEvidenceGraph) { graph.Lockfile = "other/bun.lock" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := base
			evidence.Graphs = append([]javascriptEvidenceGraph(nil), base.Graphs...)
			mutate(&evidence.Graphs[0])
			if err := validateJavaScriptEvidence(evidence, fixture.root, fixture.buns, fixture.npms, evidenceTestNow); err == nil {
				t.Fatalf("accepted tampered graph %s", name)
			}
		})
	}
}

func TestEvidenceRejectsDigestSchemaProviderAndTimestampTampering(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	base := fixture.evidence(nil)
	tests := map[string]func(*javascriptEvidence){
		"schema":   func(e *javascriptEvidence) { e.Schema = "other/v1" },
		"provider": func(e *javascriptEvidence) { e.Provider = "other" },
		"future":   func(e *javascriptEvidence) { e.GeneratedAt = evidenceTestNow.Add(time.Minute).Format(time.RFC3339Nano) },
		"expired": func(e *javascriptEvidence) {
			e.GeneratedAt = evidenceTestNow.Add(-time.Hour).Format(time.RFC3339Nano)
			e.ExpiresAt = evidenceTestNow.Add(-time.Minute).Format(time.RFC3339Nano)
		},
		"over-age": func(e *javascriptEvidence) {
			e.ExpiresAt = evidenceTestNow.Add(javascriptEvidenceMaxLifetime + time.Second).Format(time.RFC3339Nano)
		},
		"noncanonical timestamp": func(e *javascriptEvidence) { e.GeneratedAt = strings.TrimSuffix(e.GeneratedAt, "Z") + "+00:00" },
		"malformed digest":       func(e *javascriptEvidence) { e.Graphs[0].ManifestSHA256 = strings.ToUpper(e.Graphs[0].ManifestSHA256) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := base
			evidence.Graphs = append([]javascriptEvidenceGraph(nil), base.Graphs...)
			mutate(&evidence)
			if err := validateJavaScriptEvidence(evidence, fixture.root, fixture.buns, fixture.npms, evidenceTestNow); err == nil {
				t.Fatalf("accepted tampered %s evidence", name)
			}
		})
	}
	for name, path := range map[string]string{
		"manifest": filepath.Join(fixture.root, base.Graphs[0].Manifest),
		"lockfile": filepath.Join(fixture.root, base.Graphs[0].Lockfile),
	} {
		t.Run(name+" contents", func(t *testing.T) {
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.WriteFile(path, original, 0o644) }()
			if err := validateJavaScriptEvidence(base, fixture.root, fixture.buns, fixture.npms, evidenceTestNow); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatalf("%s tampering was accepted: %v", name, err)
			}
		})
	}
}

func TestRefreshAtomicPreservationOnMalformedScannerOutput(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	old := fixture.evidence(nil)
	fixture.writeEvidence(t, old)
	before := fixture.readEvidenceBytes(t)
	var stdout, stderr bytes.Buffer
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &stdout, stderr: &stderr, now: func() time.Time { return evidenceTestNow },
		goCommand: func(_ string, args ...string) commandResult { return cleanGovulnCommandResultForArgs(args...) },
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			return commandResult{stdout: []byte("not-json"), status: 1}
		},
		npmCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("10.0.0\n")}
			}
			return commandResult{stdout: []byte(cleanNPMEvidenceJSON)}
		},
	}
	if err := r.runRefresh(); err == nil {
		t.Fatal("malformed refresh unexpectedly succeeded")
	}
	if after := fixture.readEvidenceBytes(t); !bytes.Equal(after, before) {
		t.Fatal("malformed refresh overwrote the previous evidence")
	}
}

func TestRefreshPreservesEvidenceOnOperationalAndPartialFailures(t *testing.T) {
	tests := map[string]commandResult{
		"transport exhaustion": {
			stdout: []byte("not-json"), stderr: []byte("ConnectionClosed: audit request failed\n"), status: 70,
		},
		"partial transport JSON": {
			stdout: []byte(`{"example-package":[{"id":"GHSA-partial-1","severity":"low"}]}`),
			stderr: []byte("Timeout: audit request failed\n"), status: 1,
		},
		"context canceled": {
			stdout: []byte(`{}`), status: 1, err: context.Canceled,
		},
		"deadline exceeded": {
			stdout: []byte(`{}`), status: 1, err: context.DeadlineExceeded,
		},
	}
	for name, auditResult := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newJavaScriptEvidenceFixture(t)
			fixture.writeEvidence(t, fixture.evidence(nil))
			before := fixture.readEvidenceBytes(t)
			r := &runner{
				root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{},
				now: func() time.Time { return evidenceTestNow }, bunRetrySleep: func(time.Duration) {},
				bunCommand: func(_ string, args ...string) commandResult {
					if len(args) == 1 && args[0] == "--version" {
						return commandResult{stdout: []byte("1.2.3\n")}
					}
					return auditResult
				},
			}
			if err := r.runRefresh(); err == nil {
				t.Fatal("failed refresh unexpectedly succeeded")
			}
			if after := fixture.readEvidenceBytes(t); !bytes.Equal(after, before) {
				t.Fatal("failed refresh overwrote the previous evidence")
			}
		})
	}
}

func TestRefreshRejectsGenericBunAndNPMDiagnostics(t *testing.T) {
	t.Run("bun version banner", func(t *testing.T) {
		fixture := newJavaScriptEvidenceFixture(t)
		fixture.writeEvidence(t, fixture.evidence(nil))
		r := &runner{
			root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
			bunCommand: func(_ string, args ...string) commandResult {
				if len(args) == 1 && args[0] == "--version" {
					return commandResult{stdout: []byte("1.3.14\n")}
				}
				return commandResult{stdout: []byte(`{}`), stderr: []byte("\x1b[0m\x1b[1mbun audit \x1b[0m\x1b[2mv1.3.14 (0d9b296a)\x1b[0m\n")}
			},
			npmCommand: func(_ string, args ...string) commandResult {
				if len(args) == 1 && args[0] == "--version" {
					return commandResult{stdout: []byte("11.6.0\n")}
				}
				return commandResult{stdout: []byte(cleanNPMEvidenceJSON)}
			},
		}
		if err := r.runRefresh(); err != nil {
			t.Fatalf("exact Bun version banner was rejected: %v", err)
		}
	})

	t.Run("bun network diagnostic", func(t *testing.T) {
		fixture := newJavaScriptEvidenceFixture(t)
		fixture.writeEvidence(t, fixture.evidence(nil))
		before := fixture.readEvidenceBytes(t)
		var auditCalls int
		r := &runner{
			root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
			bunRetrySleep: func(time.Duration) {},
			bunCommand: func(_ string, args ...string) commandResult {
				if len(args) == 1 && args[0] == "--version" {
					return commandResult{stdout: []byte("1.2.3\n")}
				}
				auditCalls++
				return commandResult{stdout: []byte(`{"example-package":[{"id":"GHSA-network-1","severity":"low"}]}`), stderr: []byte("network request failed: temporary DNS error\n"), status: 1}
			},
		}
		if err := r.runRefresh(); err == nil || !strings.Contains(err.Error(), "diagnostics on stderr") {
			t.Fatalf("generic Bun diagnostic was accepted: %v", err)
		}
		if auditCalls != 1 {
			t.Fatalf("generic Bun diagnostic was retried %d times, want 1", auditCalls)
		}
		if after := fixture.readEvidenceBytes(t); !bytes.Equal(after, before) {
			t.Fatal("generic Bun diagnostic overwrote evidence")
		}
	})

	t.Run("npm network diagnostic", func(t *testing.T) {
		fixture := newJavaScriptEvidenceFixture(t)
		fixture.writeEvidence(t, fixture.evidence(nil))
		before := fixture.readEvidenceBytes(t)
		r := &runner{
			root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
			bunCommand: func(_ string, args ...string) commandResult {
				if len(args) == 1 && args[0] == "--version" {
					return commandResult{stdout: []byte("1.2.3\n")}
				}
				return commandResult{stdout: []byte(`{}`)}
			},
			npmCommand: func(_ string, args ...string) commandResult {
				if len(args) == 1 && args[0] == "--version" {
					return commandResult{stdout: []byte("10.0.0\n")}
				}
				return commandResult{stdout: []byte(cleanNPMEvidenceJSON), stderr: []byte("network request failed: temporary registry outage\n"), status: 0}
			},
		}
		if err := r.runRefresh(); err == nil || !strings.Contains(err.Error(), "diagnostics on stderr") {
			t.Fatalf("generic npm diagnostic was accepted: %v", err)
		}
		if after := fixture.readEvidenceBytes(t); !bytes.Equal(after, before) {
			t.Fatal("generic npm diagnostic overwrote evidence")
		}
	})
}

func TestRefreshBunCriticalTransportCannotBeAcceptedOrRetried(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	fixture.writeEvidence(t, fixture.evidence(nil))
	before := fixture.readEvidenceBytes(t)
	var auditCalls int
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunRetrySleep: func(time.Duration) {},
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			auditCalls++
			return commandResult{stdout: []byte(`{"example-package":[{"id":"GHSA-critical-transport","severity":"critical"}]}`), stderr: []byte("Timeout: audit request failed\n"), status: 1}
		},
	}
	if err := r.runRefresh(); err == nil {
		t.Fatal("critical Bun transport result was accepted")
	}
	if auditCalls != 1 {
		t.Fatalf("critical Bun transport result was retried %d times, want 1", auditCalls)
	}
	if after := fixture.readEvidenceBytes(t); !bytes.Equal(after, before) {
		t.Fatal("critical Bun transport result overwrote evidence")
	}
}

func TestRefreshRejectsLockfileMutationDuringScan(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			if err := os.WriteFile(fixture.buns[0], []byte("changed during audit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return commandResult{stdout: []byte(`{}`)}
		},
	}
	_, err := r.refreshJavaScriptEvidence(fixture.buns, fixture.npms)
	if err == nil || !strings.Contains(err.Error(), "changed during refresh") {
		t.Fatalf("concurrent dependency mutation was accepted: %v", err)
	}
}

func TestRefreshTransportRetryThenCriticalWritesEvidenceAndFails(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	var auditCalls int
	var waits []time.Duration
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunRetrySleep: func(delay time.Duration) { waits = append(waits, delay) },
		goCommand:     func(_ string, args ...string) commandResult { return cleanGovulnCommandResultForArgs(args...) },
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			auditCalls++
			if auditCalls == 1 {
				return commandResult{stdout: []byte(`{"example-package":[{"id":"GHSA-critical-1","severity":"moderate"}]}`), status: 70, stderr: []byte("Timeout: audit request failed\n")}
			}
			if auditCalls == 2 {
				return commandResult{stdout: []byte(`{"example-package":[{"id":"GHSA-critical-1","severity":"critical"}]}`), status: 1}
			}
			return commandResult{stdout: []byte(`{}`)}
		},
		npmCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("10.0.0\n")}
			}
			return commandResult{stdout: []byte(cleanNPMEvidenceJSON)}
		},
	}
	if err := r.runRefresh(); err == nil || !strings.Contains(err.Error(), "Critical JavaScript dependency finding") {
		t.Fatalf("critical refresh did not fail closed: %v", err)
	}
	if len(waits) != 1 || waits[0] != bunRetryBackoff || auditCalls < 2 {
		t.Fatalf("retry behavior = calls %d waits %v, want one %s backoff", auditCalls, waits, bunRetryBackoff)
	}
	data := fixture.readEvidenceBytes(t)
	evidence, err := decodeJavaScriptEvidence(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Graphs[0].Findings) != 1 || evidence.Graphs[0].Findings[0].Severity != "critical" {
		t.Fatalf("critical evidence was not atomically published: %+v", evidence.Graphs[0].Findings)
	}
}

func TestRefreshBunTransportRetryIsProcessWideAcrossGraphs(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	var auditCalls int
	var waits []time.Duration
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunRetrySleep: func(delay time.Duration) { waits = append(waits, delay) },
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			auditCalls++
			switch auditCalls {
			case 1:
				return commandResult{stderr: []byte("Timeout: audit request failed\n"), status: 70}
			case 2:
				return commandResult{stdout: []byte(`{}`)}
			default:
				return commandResult{stderr: []byte("ConnectionClosed: audit request failed\n"), status: 70}
			}
		},
	}
	_, err := r.refreshJavaScriptEvidence(fixture.buns, fixture.npms)
	if err == nil || !strings.Contains(err.Error(), "status 70") {
		t.Fatalf("second graph transport failure did not exhaust the process-wide retry: %v", err)
	}
	if auditCalls != 3 || len(waits) != 1 || waits[0] != bunRetryBackoff {
		t.Fatalf("audit calls=%d waits=%v, want three calls and one %s backoff", auditCalls, waits, bunRetryBackoff)
	}
}

func TestNPMEvidenceRequiresCompleteMetadataAndNormalizesStringVia(t *testing.T) {
	valid := []byte(`{"metadata":{"dependencies":{"total":2},"vulnerabilities":{"info":0,"low":1,"moderate":0,"high":0,"critical":0,"total":1}},"vulnerabilities":{"example-package":{"severity":"low","via":["GHSA-string-1"]}}}`)
	findings, err := parseNPMEvidenceFindings(valid)
	if err != nil || len(findings) != 1 || findings[0].Advisory != "GHSA-string-1" || findings[0].Severity != "low" {
		t.Fatalf("valid npm string via = %+v, %v", findings, err)
	}
	if _, err := parseNPMEvidenceFindings([]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":1,"high":0,"critical":0,"total":1}},"vulnerabilities":{"example-package":{"severity":"moderate"}}}`)); err == nil {
		t.Fatal("npm vulnerability without via was accepted")
	}
	numeric, err := parseNPMEvidenceFindings([]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":1,"critical":0,"total":1}},"vulnerabilities":{"example-package":{"severity":"high","via":[{"source":1158521,"severity":"high"}]}}}`))
	if err != nil || len(numeric) != 1 || numeric[0].Advisory != "1158521" {
		t.Fatalf("numeric npm advisory identity = %+v, %v", numeric, err)
	}
	criticalParent, err := parseNPMEvidenceFindings([]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":1,"total":1}},"vulnerabilities":{"example-package":{"severity":"critical","via":[{"source":1158521,"severity":"low"}]}}}`))
	if err != nil || len(criticalParent) != 1 || criticalParent[0].Severity != "critical" {
		t.Fatalf("npm parent severity was downgraded by advisory detail: %+v, %v", criticalParent, err)
	}
	for _, malformed := range [][]byte{
		[]byte(`{"vulnerabilities":{}}`),
		[]byte(`{"metadata":{"dependencies":{"total":0}},"vulnerabilities":{"pkg":{}}}`),
		[]byte(`{"metadata":{"dependencies":{"total":0}},"vulnerabilities":{"pkg":{"severity":"critical","via":[null]}}}`),
		[]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":0,"high":0,"critical":1,"total":1}},"vulnerabilities":{}}`),
		[]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":1,"high":0,"critical":0,"total":1}},"vulnerabilities":{"pkg":{"severity":"moderate","via":[" "]}}}`),
		[]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":1,"high":0,"critical":0,"total":1}},"vulnerabilities":{"pkg":{"severity":"moderate","via":[{}]}}}`),
		[]byte(`{"metadata":{"dependencies":{"total":1},"vulnerabilities":{"info":0,"low":0,"moderate":1,"high":0,"critical":0,"total":1}},"vulnerabilities":{"pkg":{"severity":"moderate","via":[{"source":" "}]}}}`),
	} {
		if _, err := parseNPMEvidenceFindings(malformed); err == nil {
			t.Fatalf("accepted incomplete npm audit result: %s", malformed)
		}
	}
}

func TestRefreshRejectsUnexpectedScannerStatuses(t *testing.T) {
	fixture := newJavaScriptEvidenceFixture(t)
	r := &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("1.2.3\n")}
			}
			return commandResult{stdout: []byte(`{"example-package":[{"id":"GHSA-status-1","severity":"low"}]}`), status: 70}
		},
	}
	_, err := r.refreshJavaScriptEvidence(fixture.buns, fixture.npms)
	if err == nil || !strings.Contains(err.Error(), "status 70") {
		t.Fatalf("Bun status >1 was accepted: %v", err)
	}

	r = &runner{
		root: fixture.root, timeout: time.Second, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, now: func() time.Time { return evidenceTestNow },
		bunCommand: func(_ string, args ...string) commandResult { return commandResult{stdout: []byte(`{}`)} },
		npmCommand: func(_ string, args ...string) commandResult {
			if len(args) == 1 && args[0] == "--version" {
				return commandResult{stdout: []byte("10.0.0\n")}
			}
			return commandResult{stdout: []byte(`{"metadata":{"dependencies":{"total":0}},"vulnerabilities":{}}`), status: 2}
		},
	}
	_, err = r.refreshJavaScriptEvidence(fixture.buns, fixture.npms)
	if err == nil || !strings.Contains(err.Error(), "status 2") {
		t.Fatalf("npm status >1 was accepted: %v", err)
	}
}

type javascriptEvidenceFixture struct {
	root string
	buns []string
	npms []string
}

func newJavaScriptEvidenceFixture(t *testing.T) javascriptEvidenceFixture {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example\n\ngo 1.23\n")
	buns := []string{
		"bun.lock",
		"desktop/bun.lock",
		"deploy/compose/qualification/bun.lock",
		"internal/app/testing/maliciousinstance/electron/bun.lock",
	}
	for _, lock := range buns {
		writeFixture(t, root, lock, "{}\n")
		writeFixture(t, root, filepath.ToSlash(filepath.Join(filepath.Dir(lock), "package.json")), "{}\n")
	}
	npms := []string{"pkg/apigen/typespec/package-lock.json"}
	writeFixture(t, root, npms[0], "{\"lockfileVersion\":3,\"packages\":{}}\n")
	writeFixture(t, root, "pkg/apigen/typespec/package.json", "{}\n")
	return javascriptEvidenceFixture{root: root, buns: absoluteFixturePaths(root, buns), npms: absoluteFixturePaths(root, npms)}
}

func TestDependencyDiscoveryAndGraphSpecsRejectSymlinks(t *testing.T) {
	t.Run("lockfile", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "package.json", "{}\n")
		writeFixture(t, root, "actual.lock", "{}\n")
		if err := os.Symlink("actual.lock", filepath.Join(root, "bun.lock")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, _, _, err := discover(root); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlink lockfile was accepted: %v", err)
		}
	})

	t.Run("manifest", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "bun.lock", "{}\n")
		writeFixture(t, root, "actual-package.json", "{}\n")
		if err := os.Symlink("actual-package.json", filepath.Join(root, "package.json")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, buns, npms, err := discover(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := expectedJavaScriptGraphSpecs(root, buns, npms); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("symlink manifest was accepted: %v", err)
		}
	})
}

func absoluteFixturePaths(root string, paths []string) []string {
	result := make([]string, len(paths))
	for i, path := range paths {
		result[i] = filepath.Join(root, filepath.FromSlash(path))
	}
	return result
}

func (f javascriptEvidenceFixture) evidence(finding *javascriptEvidenceFinding) javascriptEvidence {
	specs, err := expectedJavaScriptGraphSpecs(f.root, f.buns, f.npms)
	if err != nil {
		panic(err)
	}
	graphs := make([]javascriptEvidenceGraph, len(specs))
	for i, spec := range specs {
		manifestDigest, err := digestFile(filepath.Join(f.root, filepath.FromSlash(spec.manifest)))
		if err != nil {
			panic(err)
		}
		lockDigest, err := digestFile(filepath.Join(f.root, filepath.FromSlash(spec.lockfile)))
		if err != nil {
			panic(err)
		}
		graphs[i] = javascriptEvidenceGraph{
			ID: spec.id, Manager: spec.provider, RuntimeVersion: "1.2.3", Scanner: spec.scanner, ScannerVersion: "1.2.3",
			Command: spec.command, Manifest: spec.manifest, Lockfile: spec.lockfile,
			ManifestSHA256: manifestDigest, LockfileSHA256: lockDigest, Findings: []javascriptEvidenceFinding{},
		}
	}
	if finding != nil {
		graphs[0].Findings = []javascriptEvidenceFinding{*finding}
	}
	return javascriptEvidence{Schema: javascriptEvidenceSchema, Provider: javascriptEvidenceProvider,
		GeneratedAt: evidenceTestNow.Format(time.RFC3339Nano), ExpiresAt: evidenceTestNow.Add(javascriptEvidenceMaxLifetime).Format(time.RFC3339Nano), Graphs: graphs}
}

func (f javascriptEvidenceFixture) writeEvidence(t *testing.T, evidence javascriptEvidence) {
	t.Helper()
	if err := writeJavaScriptEvidenceAtomic(filepath.Join(f.root, javascriptEvidenceRelativePath), evidence); err != nil {
		t.Fatal(err)
	}
}

func (f javascriptEvidenceFixture) readEvidenceBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, javascriptEvidenceRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
