package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				"PATH": bin + ":/usr/bin:/bin", "GH_TOKEN": "fixture-token",
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
	return []string{"--image", testImage, "--repository", "ghcr.io/flidai/leapview", "--expected-workflow", testWorkflow, "--source-revision", testRevision, "--policy", policy}
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
	writeTool(t, filepath.Join(dir, "gh"), "#!/bin/sh\nset -eu\nif [ \"$3\" = --help ]; then exit 0; fi\nrepository='https://github.com/"+repositoryIdentity+"'\nworkflow='https://github.com/"+testWorkflow+"@refs/heads/main'\nrevision='"+testRevision+"'\n[ \"$OCI_TEST_MODE\" = wrong-repository ] && repository='https://github.com/attacker/example'\n[ \"$OCI_TEST_MODE\" = wrong-workflow ] && workflow='https://github.com/flidai/leapview/.github/workflows/untrusted.yml@refs/heads/main'\n[ \"$OCI_TEST_MODE\" = wrong-revision ] && revision='ffffffffffffffffffffffffffffffffffffffff'\nprintf '[{\"verificationResult\":{\"signature\":{\"certificate\":{\"sourceRepositoryURI\":\"%s\",\"buildSignerURI\":\"%s\",\"sourceRepositoryDigest\":\"%s\"}}}}]\\n' \"$repository\" \"$workflow\" \"$revision\"\n")
	writeTool(t, filepath.Join(dir, "docker"), "#!/bin/sh\nset -eu\ncase \"$*\" in\n  *'imagetools inspect'*)\n    [ \"$OCI_TEST_MODE\" = missing-sbom ] && printf '{}\\n' || printf '{\"SPDX\":{\"SPDXID\":\"SPDXRef-DOCUMENT\"}}\\n';;\n  *) exit 64;;\nesac\n")
	writeTool(t, filepath.Join(dir, "trivy"), "#!/bin/sh\nset -eu\nif [ \"$1\" = version ]; then printf '{\"Version\":\"0.74.0\"}\\n'; exit 0; fi\n[ \"$OCI_TEST_MODE\" = unavailable ] && exit 70\n[ \"$OCI_TEST_MODE\" = vulnerable ] && printf '{\"Results\":[{\"Vulnerabilities\":[{\"VulnerabilityID\":\"CVE-2026-0001\"}]}]}\\n' || printf '{\"Results\":[]}\\n'\n")
	return dir
}

func writeTool(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
