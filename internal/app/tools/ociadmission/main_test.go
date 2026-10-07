package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

const (
	testRevision = "0123456789abcdef0123456789abcdef01234567"
	testDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImage    = "ghcr.io/flidai/leapview@" + testDigest
	testWorkflow = "flidai/leapview/.github/workflows/artifacts.yml"
)

func TestHermeticAdmissionContract(t *testing.T) {
	policyPath, policyHash := testPolicy(t)
	tests := []struct {
		name     string
		image    string
		override func(map[string]any)
		want     string
	}{
		{"mutable image references", "ghcr.io/flidai/leapview:main", nil, "digest"},
		{"wrong attestation identity evidence", testImage, func(e map[string]any) {
			e["attestation"].(map[string]any)["repository"] = "attacker/example"
		}, "hermetic evidence"},
		{"substituted digest evidence", testImage, func(e map[string]any) {
			e["image"] = "ghcr.io/flidai/leapview@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}, "hermetic evidence"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			evidence := writeEvidence(t, policyHash, tc.override)
			var output bytes.Buffer
			err := runAdmission(admissionArgs(policyPath, tc.image, evidence), testEnv(nil), &output, &output)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runAdmission error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestHermeticAdmissionRejectsMismatchedScannerPlatform(t *testing.T) {
	policyPath, policyHash := testPolicy(t)
	evidence := writeEvidence(t, policyHash, func(e map[string]any) {
		e["vulnerabilityPolicy"].(map[string]any)["platform"] = "linux/amd64"
	})
	args := append(admissionArgs(policyPath, testImage, evidence), "--platform", "linux/arm64")
	var output bytes.Buffer
	err := runAdmission(args, testEnv(nil), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "hermetic evidence") {
		t.Fatalf("runAdmission error = %v, want mismatched hermetic scanner platform", err)
	}
}

func TestLiveAdmissionContractWithFakeTools(t *testing.T) {
	policyPath, _ := testPolicy(t)
	tests := []struct {
		name, mode, want string
	}{
		{"valid attestation SBOM scanner and policy", "valid", ""},
		{"wrong repository", "wrong-repository", "identity or source revision"},
		{"wrong workflow", "wrong-workflow", "identity or source revision"},
		{"wrong source revision", "wrong-revision", "identity or source revision"},
		{"missing SBOM", "missing-sbom", "no SPDX SBOM"},
		{"scanner outage", "unavailable", "scan could not complete"},
		{"policy-level CVE", "vulnerable", "exceeds policy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := liveTools(t)
			env := testEnv(map[string]string{
				"PATH": bin, "GH_TOKEN": "fixture-token",
				"GITHUB_REPOSITORY": repositoryIdentity, "OCI_TEST_MODE": tc.mode,
			})
			var output bytes.Buffer
			err := runAdmission(liveArgs(policyPath), env, &output, &output)
			if tc.want == "" {
				if err != nil || !strings.HasSuffix(output.String(), testImage+"\n") {
					t.Fatalf("runAdmission error = %v, output = %q", err, output.String())
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runAdmission error = %v, want %q", err, tc.want)
			}
			if tc.mode == "unavailable" {
				if strings.Contains(err.Error(), "fixture-token") || !strings.Contains(err.Error(), "registry unavailable with token ***") || len(err.Error()) > 512 {
					t.Fatalf("runAdmission diagnostic = %q, want bounded redacted scanner failure", err)
				}
			}
		})
	}
}

func TestVulnerabilityReportParsesAndAccountsForExceptions(t *testing.T) {
	contract := &securitypolicy.Exceptions{Version: 1, Exceptions: []securitypolicy.Exception{{
		Scanner: "trivy", Rule: "CVE-2026-0001", Resource: "openssl",
	}}}
	cleanJSON := `{"Metadata":{"ImageConfig":{"config":{"Labels":{"org.opencontainers.image.revision":"` + testRevision + `"}}}},"Results":[]}`
	clean, err := parseVulnerabilityReport([]byte(cleanJSON), contract, testEnv(nil))
	if err != nil || clean.UnresolvedCount != 0 || len(clean.Findings) != 0 || clean.ImageRevision != testRevision {
		t.Fatalf("clean report = %#v, err = %v", clean, err)
	}

	multiple := []byte(`{"Results":[{"Vulnerabilities":[` +
		`{"VulnerabilityID":"CVE-2026-0001","PkgName":"openssl","InstalledVersion":"3.0.1","FixedVersion":"3.0.2","Severity":"MEDIUM"},` +
		`{"VulnerabilityID":"CVE-2026-0002","PkgName":"curl","InstalledVersion":"8.1.0","FixedVersion":"8.1.1","Severity":"LOW"}` +
		`]}]}`)
	parsed, err := parseVulnerabilityReport(multiple, contract, testEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.UnresolvedCount != 1 || len(parsed.Findings) != 1 {
		t.Fatalf("exception accounting = count %d, findings %#v", parsed.UnresolvedCount, parsed.Findings)
	}
	if got := parsed.Findings[0]; got.CVE != "CVE-2026-0002" || got.Package != "curl" || got.InstalledVersion != "8.1.0" || got.FixedVersion != "8.1.1" {
		t.Fatalf("unresolved finding = %#v", got)
	}
	redacted, err := parseVulnerabilityReport([]byte(`{"Results":[{"Vulnerabilities":[{"VulnerabilityID":"CVE-2026-0003","PkgName":"openssl-fixture-token","InstalledVersion":"3.0-fixture-token","FixedVersion":"3.1"}]}]}`), nil, testEnv(map[string]string{"GH_TOKEN": "fixture-token"}))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(redacted.Findings)
	if err != nil || strings.Contains(string(encoded), "fixture-token") {
		t.Fatalf("sanitized findings leaked credentials: %s, err=%v", encoded, err)
	}
	if _, err := parseVulnerabilityReport([]byte(`{}`), nil, nil); err == nil {
		t.Fatal("report without Results was accepted")
	}

	const excessFindings = maxReportedFindings + 17
	vulnerabilities := make([]map[string]any, excessFindings)
	for index := range vulnerabilities {
		vulnerabilities[index] = map[string]any{
			"VulnerabilityID":  strings.Repeat("c", 128),
			"PkgName":          strings.Repeat("p", 256),
			"InstalledVersion": strings.Repeat("i", 256),
			"FixedVersion":     strings.Repeat("f", 256),
		}
	}
	largeScan, err := json.Marshal(map[string]any{"Results": []any{map[string]any{"Vulnerabilities": vulnerabilities}}})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := parseVulnerabilityReport(largeScan, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.UnresolvedCount != excessFindings || len(bounded.Findings) != maxReportedFindings || !bounded.FindingsTruncated {
		t.Fatalf("finding truncation = count %d, details %d, truncated %t", bounded.UnresolvedCount, len(bounded.Findings), bounded.FindingsTruncated)
	}
	path := filepath.Join(t.TempDir(), "bounded-report.json")
	report := vulnerabilityReport{
		SchemaVersion: vulnerabilityReportSchema, Image: testImage, Revision: testRevision,
		Scanner: reportScanner{Name: "trivy", Version: "0.74.0"}, Outcome: outcomeRejected,
		UnresolvedCount: bounded.UnresolvedCount, Findings: bounded.Findings, FindingsTruncated: bounded.FindingsTruncated,
	}
	if err := writeVulnerabilityReport(path, report); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored vulnerabilityReport
	if err := json.Unmarshal(written, &stored); err != nil {
		t.Fatal(err)
	}
	if len(written) > maxVulnerabilityReportBytes || stored.UnresolvedCount != excessFindings || len(stored.Findings) != maxReportedFindings || !stored.FindingsTruncated {
		t.Fatalf("bounded report is %d bytes with count %d, details %d, truncated %t", len(written), stored.UnresolvedCount, len(stored.Findings), stored.FindingsTruncated)
	}
}

func TestLiveVulnerabilityReport(t *testing.T) {
	policyPath, _ := testPolicy(t)
	for _, tc := range []struct {
		name, mode, outcome, wantError string
		wantCount                      int
		wantSuccess                    bool
		writeFailure                   bool
	}{
		{name: "clean scan", mode: "valid", outcome: "passed", wantSuccess: true},
		{name: "multiple findings reject without success outputs", mode: "multiple", outcome: "rejected", wantError: "exceeds policy", wantCount: 2},
		{name: "malformed report", mode: "malformed", outcome: "invalid-report", wantError: "machine-readable"},
		{name: "scanner outage is redacted", mode: "unavailable", outcome: "scanner-error", wantError: "scan could not complete"},
		{name: "report write failure rejects", mode: "valid", outcome: "", wantError: "write vulnerability report", writeFailure: true},
		{name: "report write failure preserves policy rejection", mode: "multiple", outcome: "", wantError: "exceeds policy", wantCount: 2, writeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := liveTools(t)
			reportPath := filepath.Join(t.TempDir(), "vulnerability-report.json")
			outputPath := filepath.Join(t.TempDir(), "admission.json")
			githubOutput := filepath.Join(t.TempDir(), "github-output")
			if tc.writeFailure {
				reportPath = t.TempDir()
			}
			env := testEnv(map[string]string{
				"PATH": bin, "GH_TOKEN": "fixture-token",
				"GITHUB_REPOSITORY": repositoryIdentity, "OCI_TEST_MODE": tc.mode,
				"GITHUB_OUTPUT": githubOutput,
			})
			args := append(liveArgs(policyPath), "--vulnerability-report", reportPath, "--output", outputPath)
			var output bytes.Buffer
			err := runAdmission(args, env, &output, &output)
			if tc.wantSuccess {
				if err != nil || output.String() != testImage+"\n" {
					t.Fatalf("runAdmission error = %v, output = %q", err, output.String())
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("runAdmission error = %v, want %q", err, tc.wantError)
				}
				if output.Len() != 0 {
					t.Fatalf("rejected admission wrote successful stdout: %q", output.String())
				}
				if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
					t.Fatalf("rejected admission wrote result file: stat error %v", statErr)
				}
				if _, statErr := os.Stat(githubOutput); !os.IsNotExist(statErr) {
					t.Fatalf("rejected admission wrote GitHub outputs: stat error %v", statErr)
				}
				if strings.Contains(err.Error(), "fixture-token") || len(err.Error()) > 512 {
					t.Fatalf("rejection summary is unredacted or unbounded: %q", err.Error())
				}
				if tc.writeFailure && tc.mode == "multiple" && !strings.Contains(err.Error(), "could not write vulnerability report") {
					t.Fatalf("report write failure lost its diagnostic summary: %q", err.Error())
				}
			}
			if tc.writeFailure {
				return
			}
			data, readErr := os.ReadFile(reportPath)
			if readErr != nil {
				t.Fatalf("read vulnerability report: %v", readErr)
			}
			var report struct {
				SchemaVersion          int    `json:"schemaVersion"`
				Image                  string `json:"image"`
				Revision               string `json:"revision"`
				ExpectedSourceRevision string `json:"expectedSourceRevision"`
				Platform               string `json:"platform"`
				Scanner                struct{ Name, Version string }
				Database               struct {
					Vulnerability struct {
						Version, UpdatedAt, NextUpdate, DownloadedAt string
					}
					Java struct {
						Version, UpdatedAt, NextUpdate, DownloadedAt string
					}
				}
				PolicySHA256    string `json:"policySHA256"`
				Outcome         string `json:"outcome"`
				UnresolvedCount int    `json:"unresolvedCount"`
				Findings        []struct {
					Package          string `json:"package"`
					CVE              string `json:"cve"`
					InstalledVersion string `json:"installedVersion"`
					FixedVersion     string `json:"fixedVersion"`
				} `json:"findings"`
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatalf("decode vulnerability report: %v", err)
			}
			if report.SchemaVersion != 1 || report.Image != testImage || report.Revision != "" || report.ExpectedSourceRevision != testRevision || report.Platform != "linux/arm64" || report.Scanner.Name != "trivy" || report.Scanner.Version != "0.74.0" || report.Outcome != tc.outcome || report.UnresolvedCount != tc.wantCount {
				t.Fatalf("vulnerability report = %#v", report)
			}
			if tc.name == "clean scan" && (report.Database.Vulnerability.Version != "2" || report.Database.Vulnerability.DownloadedAt == "") {
				t.Fatalf("vulnerability database metadata missing: %#v", report.Database.Vulnerability)
			}
			if len(report.PolicySHA256) != 64 {
				t.Fatalf("policy hash = %q", report.PolicySHA256)
			}
			if tc.mode == "multiple" && (len(report.Findings) != 2 || report.Findings[0].Package != "openssl" || report.Findings[0].CVE != "CVE-2026-0001" || report.Findings[0].InstalledVersion != "3.0.1" || report.Findings[0].FixedVersion != "3.0.2") {
				t.Fatalf("unresolved findings = %#v", report.Findings)
			}
			if tc.mode == "unavailable" && strings.Contains(string(data), "fixture-token") {
				t.Fatalf("vulnerability report leaked scanner credentials: %s", data)
			}
		})
	}
}

func TestVerifyAttestationAcceptsGitHubCLICertificateSchema(t *testing.T) {
	payload := []map[string]any{{
		"verificationResult": map[string]any{
			"signature": map[string]any{"certificate": map[string]any{
				"sourceRepositoryURI":    "https://github.com/" + repositoryIdentity,
				"buildSignerURI":         "https://github.com/" + testWorkflow + "@refs/heads/main",
				"sourceRepositoryDigest": testRevision,
			}},
		},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyAttestation(data, testWorkflow, testRevision) {
		t.Fatal("verifyAttestation rejected the canonical GitHub CLI certificate schema")
	}
}

func TestLiveAdmissionRejectsMissingVerifier(t *testing.T) {
	policyPath, _ := testPolicy(t)
	var output bytes.Buffer
	err := runAdmission(liveArgs(policyPath), testEnv(map[string]string{"PATH": t.TempDir(), "GITHUB_TOKEN": "test"}), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "verifier") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("runAdmission error = %v", err)
	}
}

func TestAdmissionRejectsInvalidPlatform(t *testing.T) {
	policyPath, _ := testPolicy(t)
	args := append(liveArgs(policyPath), "--platform", "linux/ppc64le")
	var output bytes.Buffer
	err := runAdmission(args, testEnv(nil), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("runAdmission error = %v, want invalid platform", err)
	}
}

func TestAttestationCanonicalClaimsCannotBeBypassedByFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		certificate map[string]any
	}{
		{
			name: "empty canonical repository",
			certificate: map[string]any{
				"sourceRepositoryURI": "", "sourceRepository": repositoryIdentity,
				"buildSignerURI": "https://github.com/" + testWorkflow + "@refs/heads/main", "sourceRepositoryDigest": testRevision,
			},
		},
		{
			name: "empty canonical workflow",
			certificate: map[string]any{
				"sourceRepositoryURI": "https://github.com/" + repositoryIdentity,
				"buildSignerURI":      "", "subjectAlternativeName": "https://github.com/" + testWorkflow + "@refs/heads/main",
				"sourceRepositoryDigest": testRevision,
			},
		},
		{
			name: "empty canonical revision",
			certificate: map[string]any{
				"sourceRepositoryURI":    "https://github.com/" + repositoryIdentity,
				"buildSignerURI":         "https://github.com/" + testWorkflow + "@refs/heads/main",
				"sourceRepositoryDigest": "", "sourceDigest": testRevision,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []map[string]any{{
				"verificationResult": map[string]any{
					"signature": map[string]any{"certificate": tc.certificate},
				},
			}}
			data, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if verifyAttestation(data, testWorkflow, testRevision) {
				t.Fatal("verifyAttestation accepted a fallback behind an explicit empty canonical claim")
			}
		})
	}
}

func TestHermeticAdmissionWritesGitHubOutputs(t *testing.T) {
	policyPath, policyHash := testPolicy(t)
	evidencePath := writeEvidence(t, policyHash, nil)
	outputPath := filepath.Join(t.TempDir(), "result.json")
	githubOutput := filepath.Join(t.TempDir(), "github-output")
	env := testEnv(map[string]string{"GITHUB_OUTPUT": githubOutput})
	args := admissionArgs(policyPath, testImage, evidencePath)
	args = append(args, "--output", outputPath)
	var output bytes.Buffer
	if err := runAdmission(args, env, &output, &output); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outputPath); err != nil || !strings.Contains(string(data), `"schemaVersion":1`) {
		t.Fatalf("output file = %q, err = %v", data, err)
	}
	data, err := os.ReadFile(githubOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "image="+testImage+"\ndigest="+testDigest+"\n" {
		t.Fatalf("GitHub output = %q", data)
	}
}

func TestPolicyRequiresExplicitBooleanIgnoreUnfixed(t *testing.T) {
	basePath, policyHash := testPolicy(t)
	baseData, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "missing"},
		{name: "null", value: nil},
		{name: "string", value: "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var policy map[string]any
			if err := json.Unmarshal(baseData, &policy); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing" {
				delete(policy, "ignoreUnfixed")
			} else {
				policy["ignoreUnfixed"] = tc.value
			}
			data, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			evidence := writeEvidence(t, policyHash, nil)
			var output bytes.Buffer
			err = runAdmission(admissionArgs(path, testImage, evidence), testEnv(nil), &output, &output)
			if err == nil {
				t.Fatal("runAdmission accepted policy without a boolean ignoreUnfixed")
			}
		})
	}
}

func testPolicy(t *testing.T) (string, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "security", "container-vulnerability-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return path, hex.EncodeToString(hash[:])
}

func writeEvidence(t *testing.T, policyHash string, override func(map[string]any)) string {
	t.Helper()
	evidence := map[string]any{
		"schemaVersion": 1, "image": testImage, "digest": testDigest, "registryDigest": testDigest,
		"attestation":         map[string]any{"verified": true, "repository": repositoryIdentity, "workflow": testWorkflow, "sourceRevision": testRevision},
		"sbom":                map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"},
		"vulnerabilityPolicy": map[string]any{"sha256": policyHash, "scanner": "trivy", "passed": true},
	}
	if override != nil {
		override(evidence)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func admissionArgs(policy, image, evidence string) []string {
	return []string{"--image", image, "--repository", "ghcr.io/flidai/leapview", "--expected-workflow", testWorkflow, "--source-revision", testRevision, "--policy", policy, "--mode", "hermetic", "--evidence", evidence}
}

func liveArgs(policy string) []string {
	return []string{"--image", testImage, "--repository", "ghcr.io/flidai/leapview", "--expected-workflow", testWorkflow, "--source-revision", testRevision, "--policy", policy, "--platform", "linux/arm64"}
}

func testEnv(values map[string]string) []string {
	env := []string{}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func liveTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatal(err)
	}
	mkdir, err = filepath.Abs(mkdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mkdir, filepath.Join(dir, "mkdir")); err != nil {
		t.Fatal(err)
	}
	writeTool(t, filepath.Join(dir, "gh"), "#!/bin/sh\nset -eu\nif [ \"$3\" = --help ]; then exit 0; fi\nrepository='https://github.com/"+repositoryIdentity+"'\nworkflow='https://github.com/"+testWorkflow+"@refs/heads/main'\nrevision='"+testRevision+"'\n[ \"$OCI_TEST_MODE\" = wrong-repository ] && repository='https://github.com/attacker/example'\n[ \"$OCI_TEST_MODE\" = wrong-workflow ] && workflow='https://github.com/flidai/leapview/.github/workflows/untrusted.yml@refs/heads/main'\n[ \"$OCI_TEST_MODE\" = wrong-revision ] && revision='ffffffffffffffffffffffffffffffffffffffff'\nprintf '[{\"verificationResult\":{\"signature\":{\"certificate\":{\"sourceRepositoryURI\":\"%s\",\"buildSignerURI\":\"%s\",\"sourceRepositoryDigest\":\"%s\"}}}}]\\n' \"$repository\" \"$workflow\" \"$revision\"\n")
	writeTool(t, filepath.Join(dir, "docker"), "#!/bin/sh\nset -eu\ncase \"$*\" in\n  *'imagetools inspect'*)\n    [ \"$OCI_TEST_MODE\" = missing-sbom ] && printf '{}\\n' || printf '{\"SPDX\":{\"SPDXID\":\"SPDXRef-DOCUMENT\"}}\\n';;\n  *) exit 64;;\nesac\n")
	writeTool(t, filepath.Join(dir, "trivy"), "#!/bin/sh\nset -eu\ncache_dir=''\naction=''\nwhile [ \"$#\" -gt 0 ]; do\n  case \"$1\" in\n    --cache-dir) cache_dir=$2; shift 2;;\n    version|image) action=$1; shift; break;;\n    *) shift;;\n  esac\ndone\nif [ \"$action\" = version ]; then printf '{\"Version\":\"0.74.0\"}\\n'; exit 0; fi\ncase \" $* \" in *' --platform linux/arm64 '*) ;; *) printf 'target platform was not explicit\\n' >&2; exit 71;; esac\nmkdir -p \"$cache_dir/db\" \"$cache_dir/java-db\"\nprintf '{\"Version\":2,\"UpdatedAt\":\"2026-09-30T12:00:00Z\",\"NextUpdate\":\"2026-10-01T12:00:00Z\",\"DownloadedAt\":\"2026-09-30T12:01:00Z\"}\\n' > \"$cache_dir/db/metadata.json\"\nprintf '{\"Version\":1,\"UpdatedAt\":\"2026-09-30T12:00:00Z\",\"NextUpdate\":\"2026-10-01T12:00:00Z\",\"DownloadedAt\":\"2026-09-30T12:01:00Z\"}\\n' > \"$cache_dir/java-db/metadata.json\"\nif [ \"$OCI_TEST_MODE\" = unavailable ]; then printf 'registry unavailable with token %s\\n' \"$GH_TOKEN\" >&2; exit 70; fi\ncase \"$OCI_TEST_MODE\" in\n  malformed) printf '{broken\\n';;\n  multiple) printf '{\"Results\":[{\"Vulnerabilities\":[{\"VulnerabilityID\":\"CVE-2026-0001\",\"PkgName\":\"openssl\",\"InstalledVersion\":\"3.0.1\",\"FixedVersion\":\"3.0.2\",\"Severity\":\"HIGH\"},{\"VulnerabilityID\":\"CVE-2026-0002\",\"PkgName\":\"curl\",\"InstalledVersion\":\"8.1.0\",\"FixedVersion\":\"8.1.1\",\"Severity\":\"MEDIUM\"}]}]}\\n';;\n  vulnerable) printf '{\"Results\":[{\"Vulnerabilities\":[{\"VulnerabilityID\":\"CVE-2026-0001\"}]}]}\\n';;\n  *) printf '{\"Results\":[]}\\n';;\nesac\n")
	return dir
}

func writeTool(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestScannerDiagnosticDiscardsTruncatedCredentials(t *testing.T) {
	runner := commandRunner{env: testEnv(map[string]string{"GH_TOKEN": "sensitive-credential-value"})}
	_, diagnostic, err := runner.runCommandPartsWithDiagnostic([]string{"/bin/sh", "-c", "exec 1>&2; printf '%4080s' ''; printf 'sensitive-credential-value'; exit 1"})
	if err == nil || strings.Contains(diagnostic, "sensitive") || !strings.Contains(diagnostic, "exceeded bounded size") {
		t.Fatalf("truncated scanner diagnostic = %q, err = %v", diagnostic, err)
	}
}

func TestVulnerabilityReportRejectsOutputPathCollisions(t *testing.T) {
	policyPath, _ := testPolicy(t)
	path := filepath.Join(t.TempDir(), "output.json")
	for _, target := range []string{"receipt", "github-output"} {
		t.Run(target, func(t *testing.T) {
			args := append(liveArgs(policyPath), "--vulnerability-report", path)
			env := testEnv(nil)
			if target == "receipt" {
				args = append(args, "--output", path)
			} else {
				env = testEnv(map[string]string{"GITHUB_OUTPUT": path})
			}
			var output bytes.Buffer
			err := runAdmission(args, env, &output, &output)
			if err == nil || !strings.Contains(err.Error(), "separate from admission outputs") || output.Len() != 0 {
				t.Fatalf("colliding %s path error = %v, output = %q", target, err, output.String())
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("colliding output was created: %v", err)
			}
		})
	}
}
